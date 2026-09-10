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

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/consts"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/amdgpu"
	klog "k8s.io/klog/v2"
	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	cdispec "tags.cncf.io/container-device-interface/specs-go"
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
type VfioPciManager struct {
	iommuFDEnabled bool
}

// NewVfioPciManager creates a new VfioPciManager, verifying that IOMMU is
// enabled and the vfio_pci module is available.
func NewVfioPciManager() (*VfioPciManager, error) {
	if !amdgpu.CheckIOMMUEnabled() {
		return nil, fmt.Errorf("IOMMU is not enabled in the kernel")
	}
	if !amdgpu.CheckVFIOModuleLoaded() {
		klog.Warningf("vfio_pci module not loaded; VFIO passthrough will only work for pre-bound devices")
	}
	iommuFD := amdgpu.CheckIommuFDEnabled()
	if iommuFD {
		klog.Infof("IOMMUFD support detected (/dev/iommu present)")
	}
	return &VfioPciManager{iommuFDEnabled: iommuFD}, nil
}

// Configure binds a GIM SR-IOV VF to the vfio-pci driver. Records the
// pre-configure driver so Unconfigure knows whether to rebind.
func (vm *VfioPciManager) Configure(info *AmdGpuVFIOInfo) error {
	gpuMu := perGpuLock.Get(info.PCIAddress)
	gpuMu.Lock()
	defer gpuMu.Unlock()

	currentDriver, err := amdgpu.GetPCIDriver(info.PCIAddress)
	if err != nil {
		return fmt.Errorf("failed to get current driver for %s: %w", info.PCIAddress, err)
	}

	if currentDriver == consts.VFIODriverName {
		klog.Infof("Device %s already bound to vfio-pci", info.PCIAddress)
		if cdev, err := amdgpu.GetIommuFDCdev(info.PCIAddress); err == nil {
			info.IommuFDCdev = cdev
		} else {
			klog.Warningf("IOMMUFD cdev lookup failed for %s: %v", info.PCIAddress, err)
		}
		return nil
	}

	// For PF passthrough, verify no SR-IOV VFs are active. Binding a PF
	// to vfio-pci while VFs exist would break them.
	if !info.IsVF {
		numVFsPath := filepath.Join(amdgpu.PCIDevicePath, info.PCIAddress, "sriov_numvfs")
		if data, err := os.ReadFile(numVFsPath); err == nil {
			numVFs := strings.TrimSpace(string(data))
			if numVFs != "0" && numVFs != "" {
				return fmt.Errorf("cannot passthrough PF %s: %s active VFs must be removed first", info.PCIAddress, numVFs)
			}
		}
	}

	if currentDriver != "" {
		if err := unbindFromDriver(info.PCIAddress); err != nil {
			return fmt.Errorf("failed to unbind %s from %s: %w", info.PCIAddress, currentDriver, err)
		}
	}

	if err := bindToDriver(info.PCIAddress, consts.VFIODriverName); err != nil {
		return fmt.Errorf("failed to bind %s to vfio-pci: %w", info.PCIAddress, err)
	}

	if cdev, err := amdgpu.GetIommuFDCdev(info.PCIAddress); err == nil {
		info.IommuFDCdev = cdev
	} else {
		klog.Warningf("IOMMUFD cdev lookup failed for %s: %v", info.PCIAddress, err)
	}

	klog.Infof("Configured %s for VFIO passthrough (isVF=%v)", info.PCIAddress, info.IsVF)
	return nil
}

// Unconfigure rebinds a VF back to its pre-configure driver. If the VF was
// already on vfio-pci before Configure, it stays on vfio-pci.
func (vm *VfioPciManager) Unconfigure(info *AmdGpuVFIOInfo) error {
	gpuMu := perGpuLock.Get(info.PCIAddress)
	gpuMu.Lock()
	defer gpuMu.Unlock()

	if info.preConfigureDriver == consts.VFIODriverName {
		klog.Infof("Device %s was pre-bound to vfio-pci, leaving on vfio-pci", info.PCIAddress)
		return nil
	}

	// If the device had no driver before Configure (GIM VF), unbind from
	// vfio-pci and clear driver_override to return it to the unbound state.
	if info.preConfigureDriver == "" {
		currentDriver, err := amdgpu.GetPCIDriver(info.PCIAddress)
		if err != nil {
			return fmt.Errorf("failed to get current driver for %s: %w", info.PCIAddress, err)
		}
		if currentDriver == consts.VFIODriverName {
			if err := unbindFromDriver(info.PCIAddress); err != nil {
				return fmt.Errorf("failed to unbind %s from vfio-pci: %w", info.PCIAddress, err)
			}
			if err := clearDriverOverride(info.PCIAddress); err != nil {
				klog.Warningf("Failed to clear driver_override for %s: %v", info.PCIAddress, err)
			}
			klog.Infof("Unconfigured %s: unbound from vfio-pci (was unbound before Configure)", info.PCIAddress)
		}
		return nil
	}

	currentDriver, err := amdgpu.GetPCIDriver(info.PCIAddress)
	if err != nil {
		return fmt.Errorf("failed to get current driver for %s: %w", info.PCIAddress, err)
	}
	if currentDriver == info.preConfigureDriver {
		return nil
	}

	if currentDriver != "" {
		if err := unbindFromDriver(info.PCIAddress); err != nil {
			return fmt.Errorf("failed to unbind %s from %s: %w", info.PCIAddress, currentDriver, err)
		}
	}

	if err := bindToDriver(info.PCIAddress, info.preConfigureDriver); err != nil {
		return fmt.Errorf("failed to bind %s to %s: %w", info.PCIAddress, info.preConfigureDriver, err)
	}

	klog.Infof("Unconfigured %s: rebound to %s", info.PCIAddress, info.preConfigureDriver)
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
	unbindPath := filepath.Join(amdgpu.PCIDriversPath, driverName, "unbind")
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
	bindPath := filepath.Join(amdgpu.PCIDriversPath, driver, "bind")
	if err := os.WriteFile(bindPath, []byte(pciAddr), 0200); err != nil {
		if cleanupErr := os.WriteFile(overridePath, []byte("\n"), 0200); cleanupErr != nil {
			klog.Warningf("Failed to clear driver_override for %s after bind failure: %v", pciAddr, cleanupErr)
		}
		return fmt.Errorf("failed to write to %s: %w", bindPath, err)
	}

	// Clear driver_override after successful bind so the device isn't pinned
	// to this driver across reboots or rescan events.
	if err := os.WriteFile(overridePath, []byte("\n"), 0200); err != nil {
		klog.Warningf("Failed to clear driver_override for %s after successful bind: %v", pciAddr, err)
	}

	klog.Infof("Bound %s to %s", pciAddr, driver)
	return nil
}

func clearDriverOverride(pciAddr string) error {
	overridePath := filepath.Join(amdgpu.PCIDevicePath, pciAddr, "driver_override")
	return os.WriteFile(overridePath, []byte("\n"), 0200)
}

// GetVfioCommonCDIEdits returns CDI edits for the common IOMMU API device.
// With IOMMUFD: /dev/iommu. With legacy: /dev/vfio/vfio.
func GetVfioCommonCDIEdits(usingIommuFD, iommuFDEnabled bool) *cdiapi.ContainerEdits {
	edits := &cdiapi.ContainerEdits{
		ContainerEdits: &cdispec.ContainerEdits{
			DeviceNodes: make([]*cdispec.DeviceNode, 0),
		},
	}
	if usingIommuFD && iommuFDEnabled {
		edits.ContainerEdits.DeviceNodes = append(edits.ContainerEdits.DeviceNodes, &cdispec.DeviceNode{
			Path: amdgpu.IommuDevicePath,
		})
	} else {
		edits.ContainerEdits.DeviceNodes = append(edits.ContainerEdits.DeviceNodes, &cdispec.DeviceNode{
			Path: filepath.Join(amdgpu.VFIODevicesRoot, "vfio"),
		})
	}
	return edits
}

// GetVfioDeviceCDIEdits returns CDI edits for a specific VFIO device.
// With IOMMUFD: /dev/vfio/devices/<cdev>. With legacy: /dev/vfio/<group>.
// Returns (edits, usingIommuFD) so the caller can match the common API device.
func GetVfioDeviceCDIEdits(info *AmdGpuVFIOInfo, preferIommuFD bool) (*cdiapi.ContainerEdits, bool) {
	if preferIommuFD && info.IommuFDCdev != "" {
		return &cdiapi.ContainerEdits{
			ContainerEdits: &cdispec.ContainerEdits{
				DeviceNodes: []*cdispec.DeviceNode{
					{Path: filepath.Join(amdgpu.VFIODevicesPath, info.IommuFDCdev)},
				},
			},
		}, true
	}
	if preferIommuFD {
		klog.Warningf("IOMMUFD preferred but cdev unavailable for %s, falling back to legacy VFIO", info.PCIAddress)
	}
	return &cdiapi.ContainerEdits{
		ContainerEdits: &cdispec.ContainerEdits{
			DeviceNodes: []*cdispec.DeviceNode{
				{Path: filepath.Join(amdgpu.VFIODevicesRoot, info.IOMMUGroup)},
			},
		},
	}, false
}

var validDriverNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func isValidDriverName(name string) bool {
	return name != "" && name != "." && name != ".." && validDriverNameRE.MatchString(name)
}

// getDeviceAttrs is defined in state.go using syscall.Stat_t and unix.Major/Minor.
