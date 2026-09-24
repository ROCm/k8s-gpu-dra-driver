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
	"strconv"
	"strings"
	"sync"

	configapi "github.com/ROCm/k8s-gpu-dra-driver/api/amd.com/resource/gpu/v1alpha1"
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
		vm.recordIommuFDCdev(info)
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

	vm.recordIommuFDCdev(info)

	klog.Infof("Configured %s for VFIO passthrough (isVF=%v)", info.PCIAddress, info.IsVF)
	return nil
}

// Unconfigure rebinds a VF back to its pre-configure driver. If the VF was
// already on vfio-pci before Configure, it stays on vfio-pci.
func (vm *VfioPciManager) Unconfigure(info *AmdGpuVFIOInfo) error {
	gpuMu := perGpuLock.Get(info.PCIAddress)
	gpuMu.Lock()
	defer gpuMu.Unlock()

	// The cdev is only valid while bound to vfio-pci; Configure re-reads it.
	info.IommuFDCdev = ""

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

// recordIommuFDCdev refreshes the device's IOMMUFD cdev name on info. It must
// run after the device is bound to vfio-pci, since vfio-dev only exists then.
// The value is cleared first so a failed lookup never leaves a stale cdev.
//
// info may be a long-lived entry in DeviceState.allocatable; writing to it is
// safe only because DeviceState.Prepare/Unprepare hold the state lock.
func (vm *VfioPciManager) recordIommuFDCdev(info *AmdGpuVFIOInfo) {
	info.IommuFDCdev = ""
	if !vm.iommuFDEnabled {
		return
	}
	cdev, err := amdgpu.GetIommuFDCdev(info.PCIAddress)
	if err != nil {
		klog.Warningf("IOMMUFD cdev lookup failed for %s: %v", info.PCIAddress, err)
		return
	}
	info.IommuFDCdev = cdev
}

// UseIommuFD decides the IOMMU backend for a device allocation. IOMMUFD is
// used only when the policy asks for it, the host exposes /dev/iommu, and the
// device has a vfio cdev. PreferIommuFD falls back to legacy VFIO otherwise;
// RequireIommuFD returns an error instead. The result must drive both the
// per-device and the common CDI edits so the spec never mixes backends.
func UseIommuFD(info *AmdGpuVFIOInfo, policy configapi.IOMMUBackendPolicy, iommuFDEnabled bool) (bool, error) {
	switch policy {
	case configapi.IOMMUBackendPolicyLegacyOnly:
		return false, nil
	case configapi.IOMMUBackendPolicyPreferIommuFD, configapi.IOMMUBackendPolicyRequireIommuFD:
	default:
		return false, fmt.Errorf("unknown IOMMU backend policy %q", policy)
	}

	var reason string
	switch {
	case !iommuFDEnabled:
		reason = "/dev/iommu unavailable on host"
	case info.IommuFDCdev == "":
		reason = "no vfio cdev for device"
	default:
		return true, nil
	}

	if policy == configapi.IOMMUBackendPolicyRequireIommuFD {
		return false, fmt.Errorf("IOMMUFD required for %s but %s", info.PCIAddress, reason)
	}
	klog.Warningf("IOMMUFD preferred for %s but %s, falling back to legacy VFIO", info.PCIAddress, reason)
	return false, nil
}

// vfioDeviceNode builds a CDI device node for path, reading major/minor from
// the host. Falls back to a path-only node if the attrs can't be read.
func vfioDeviceNode(path string) *cdispec.DeviceNode {
	major, minor, devType, permissions, err := getDeviceAttrs(path)
	if err != nil {
		klog.Warningf("Could not read device attrs for %s, using path-only CDI spec: %v", path, err)
		return &cdispec.DeviceNode{Path: path, HostPath: path, Type: "c"}
	}
	return &cdispec.DeviceNode{
		Path:        path,
		HostPath:    path,
		Type:        devType,
		Major:       major,
		Minor:       minor,
		Permissions: permissions,
	}
}

// GetVfioCommonCDIEdits returns CDI edits for the common IOMMU API device.
// With IOMMUFD: /dev/iommu. With legacy: /dev/vfio/vfio.
func GetVfioCommonCDIEdits(useIommuFD bool) *cdiapi.ContainerEdits {
	path := filepath.Join(amdgpu.VFIODevicesRoot, "vfio")
	if useIommuFD {
		path = amdgpu.IommuDevicePath
	}
	return &cdiapi.ContainerEdits{
		ContainerEdits: &cdispec.ContainerEdits{
			DeviceNodes: []*cdispec.DeviceNode{vfioDeviceNode(path)},
		},
	}
}

// GetVfioDeviceCDIEdits returns CDI edits for a specific VFIO device.
// With IOMMUFD: /dev/vfio/devices/<cdev>. With legacy: /dev/vfio/<group>.
func GetVfioDeviceCDIEdits(info *AmdGpuVFIOInfo, useIommuFD bool) (*cdiapi.ContainerEdits, error) {
	var path string
	if useIommuFD {
		path = filepath.Join(amdgpu.VFIODevicesPath, info.IommuFDCdev)
	} else {
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
		path = filepath.Join(amdgpu.VFIODevicesRoot, iommuGroup)
	}
	return &cdiapi.ContainerEdits{
		ContainerEdits: &cdispec.ContainerEdits{
			DeviceNodes: []*cdispec.DeviceNode{vfioDeviceNode(path)},
		},
	}, nil
}

var validDriverNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func isValidDriverName(name string) bool {
	return name != "" && name != "." && name != ".." && validDriverNameRE.MatchString(name)
}

// getDeviceAttrs is defined in state.go using syscall.Stat_t and unix.Major/Minor.
