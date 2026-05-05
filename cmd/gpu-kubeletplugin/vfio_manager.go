/*
Copyright (c) Advanced Micro Devices, Inc. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/amdgpu"
	klog "k8s.io/klog/v2"
	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	cdispec "tags.cncf.io/container-device-interface/specs-go"
)

const (
	amdgpuDriver = "amdgpu"
)

// perGpuLock provides per-PCI-address mutual exclusion for bind/unbind operations.
var perGpuLock = &gpuLockMap{locks: make(map[string]*sync.Mutex)}

type gpuLockMap struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (m *gpuLockMap) Get(pciAddr string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.locks[pciAddr]; !ok {
		m.locks[pciAddr] = &sync.Mutex{}
	}
	return m.locks[pciAddr]
}

// VfioPciManager handles binding and unbinding of AMD GPUs to/from vfio-pci.
type VfioPciManager struct{}

// NewVfioPciManager creates a new VfioPciManager, verifying that IOMMU is
// enabled and the vfio_pci module is available.
func NewVfioPciManager() (*VfioPciManager, error) {
	if !amdgpu.CheckIOMMUEnabled() {
		return nil, fmt.Errorf("IOMMU is not enabled in the kernel")
	}
	if !amdgpu.CheckVFIOModuleLoaded() {
		klog.Warningf("vfio_pci module not loaded; VFIO passthrough will only work for pre-bound devices")
	}
	return &VfioPciManager{}, nil
}

// Configure binds a GPU to the vfio-pci driver. If the device is already
// bound to vfio-pci, this is a no-op.
func (vm *VfioPciManager) Configure(info *AmdGpuVFIOInfo) error {
	gpuMu := perGpuLock.Get(info.PCIAddress)
	gpuMu.Lock()
	defer gpuMu.Unlock()

	currentDriver, err := amdgpu.GetPCIDriver(info.PCIAddress)
	if err != nil {
		return fmt.Errorf("failed to get current driver for %s: %w", info.PCIAddress, err)
	}
	if currentDriver == amdgpu.VFIODriverName {
		klog.Infof("Device %s already bound to vfio-pci", info.PCIAddress)
		return nil
	}

	// If currently on amdgpu, unbind first.
	if currentDriver != "" {
		if err := unbindFromDriver(info.PCIAddress); err != nil {
			return fmt.Errorf("failed to unbind %s from %s: %w", info.PCIAddress, currentDriver, err)
		}
	}

	// Bind to vfio-pci.
	if err := bindToDriver(info.PCIAddress, amdgpu.VFIODriverName); err != nil {
		return fmt.Errorf("failed to bind %s to vfio-pci: %w", info.PCIAddress, err)
	}

	klog.Infof("Configured %s for VFIO passthrough", info.PCIAddress)
	return nil
}

// Unconfigure rebinds a GPU back to the amdgpu driver.
func (vm *VfioPciManager) Unconfigure(info *AmdGpuVFIOInfo) error {
	perGpuLock.Get(info.PCIAddress).Lock()
	defer perGpuLock.Get(info.PCIAddress).Unlock()

	currentDriver, err := amdgpu.GetPCIDriver(info.PCIAddress)
	if err != nil {
		return fmt.Errorf("failed to get current driver for %s: %w", info.PCIAddress, err)
	}
	if currentDriver == amdgpuDriver {
		klog.Infof("Device %s already bound to amdgpu", info.PCIAddress)
		return nil
	}
	if currentDriver == "" {
		// No driver bound, just bind to amdgpu.
		return bindToDriver(info.PCIAddress, amdgpuDriver)
	}

	if err := unbindFromDriver(info.PCIAddress); err != nil {
		return fmt.Errorf("failed to unbind %s from %s: %w", info.PCIAddress, currentDriver, err)
	}

	if err := bindToDriver(info.PCIAddress, amdgpuDriver); err != nil {
		return fmt.Errorf("failed to bind %s to amdgpu: %w", info.PCIAddress, err)
	}

	klog.Infof("Unconfigured %s: rebound to amdgpu", info.PCIAddress)
	return nil
}

// unbindFromDriver unbinds a PCI device from its current driver.
func unbindFromDriver(pciAddr string) error {
	driverLink := filepath.Join(amdgpu.PCIDevicePath, pciAddr, "driver")
	// Resolve the symlink to get the driver name.
	driverRel, err := os.Readlink(driverLink)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No driver bound.
		}
		return err
	}
	// Use absolute sysfs path. The readlink gives a relative path like
	// ../../../../bus/pci/drivers/amdgpu — extract just the driver name.
	driverName := filepath.Base(driverRel)
	if !isValidDriverName(driverName) {
		return fmt.Errorf("invalid driver name for %s: %q", pciAddr, driverName)
	}
	unbindPath := filepath.Join("/sys/bus/pci/drivers", driverName, "unbind")
	if err := os.WriteFile(unbindPath, []byte(pciAddr), 0200); err != nil {
		return fmt.Errorf("failed to write to %s: %w", unbindPath, err)
	}
	klog.Infof("Unbound %s from %s", pciAddr, driverName)
	return nil
}

// bindToDriver binds a PCI device to a specified driver using driver_override.
// This uses the targeted driver/bind approach (like NVIDIA) rather than
// drivers_probe (which can race with other drivers).
func bindToDriver(pciAddr, driver string) error {
	// Set driver_override to ensure only the target driver claims this device.
	overridePath := filepath.Join(amdgpu.PCIDevicePath, pciAddr, "driver_override")
	if err := os.WriteFile(overridePath, []byte(driver), 0200); err != nil {
		return fmt.Errorf("failed to set driver_override to %s: %w", driver, err)
	}

	// Write to the target driver's bind file.
	bindPath := filepath.Join("/sys/bus/pci/drivers", driver, "bind")
	if err := os.WriteFile(bindPath, []byte(pciAddr), 0200); err != nil {
		if cleanupErr := os.WriteFile(overridePath, []byte(""), 0200); cleanupErr != nil {
			klog.Warningf("Failed to clear driver_override for %s after bind failure: %v", pciAddr, cleanupErr)
		}
		return fmt.Errorf("failed to write to %s: %w", bindPath, err)
	}

	klog.Infof("Bound %s to %s", pciAddr, driver)
	return nil
}

// GetVfioCommonCDIContainerEdits returns CDI edits for the /dev/vfio/vfio
// container device, shared across all VFIO allocations.
func GetVfioCommonCDIContainerEdits() *cdiapi.ContainerEdits {
	return &cdiapi.ContainerEdits{
		ContainerEdits: &cdispec.ContainerEdits{
			DeviceNodes: []*cdispec.DeviceNode{
				{
					Path: filepath.Join(amdgpu.VFIODevicesRoot, "vfio"),
				},
			},
		},
	}
}

// GetVfioCDIContainerEdits returns CDI edits for a specific VFIO device,
// identified by its IOMMU group number.
func GetVfioCDIContainerEdits(info *AmdGpuVFIOInfo) (*cdiapi.ContainerEdits, error) {
	iommuGroup := info.IOMMUGroup
	if iommuGroup == "" {
		// Try to read it at prepare time if not set at discovery.
		var err error
		iommuGroup, err = amdgpu.GetIOMMUGroup(info.PCIAddress)
		if err != nil {
			return nil, fmt.Errorf("failed to get IOMMU group for %s: %w", info.PCIAddress, err)
		}
	}

	if _, err := strconv.Atoi(iommuGroup); err != nil {
		return nil, fmt.Errorf("invalid IOMMU group format for %s: %q", info.PCIAddress, iommuGroup)
	}

	vfioDevPath := filepath.Join(amdgpu.VFIODevicesRoot, iommuGroup)

	// Read major/minor for proper CDI spec.
	major, minor, devType, permissions, err := getDeviceAttrs(vfioDevPath)
	if err != nil {
		// Fallback: create spec without major/minor (some runtimes handle this).
		klog.Warningf("Could not read device attrs for %s, using path-only CDI spec: %v", vfioDevPath, err)
		return &cdiapi.ContainerEdits{
			ContainerEdits: &cdispec.ContainerEdits{
				DeviceNodes: []*cdispec.DeviceNode{
					{Path: vfioDevPath, HostPath: vfioDevPath, Type: "c"},
				},
			},
		}, nil
	}

	return &cdiapi.ContainerEdits{
		ContainerEdits: &cdispec.ContainerEdits{
			DeviceNodes: []*cdispec.DeviceNode{
				{
					Path:        vfioDevPath,
					HostPath:    vfioDevPath,
					Type:        devType,
					Major:       major,
					Minor:       minor,
					Permissions: permissions,
				},
			},
		},
	}, nil
}

var validDriverNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func isValidDriverName(name string) bool {
	return name != "" && name != "." && name != ".." && validDriverNameRE.MatchString(name)
}

// getDeviceAttrs is defined in state.go using syscall.Stat_t and unix.Major/Minor.

