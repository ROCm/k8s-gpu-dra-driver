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

package amdgpu

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/golang/glog"
)

const (
	// VFIODriverPath is the sysfs path for the vfio-pci driver.
	VFIODriverPath = "/sys/bus/pci/drivers/vfio-pci"

	// VFIODriverName is the kernel driver name for VFIO PCI passthrough.
	VFIODriverName = "vfio-pci"

	// GIMDriverPath is the sysfs path for the AMD GIM (GPU-IOV Module) driver.
	GIMDriverPath = "/sys/bus/pci/drivers/gim"

	// GIMDriverName is the kernel driver name for AMD SR-IOV.
	GIMDriverName = "gim"

	// GIMModulePath is the sysfs module path for the GIM driver.
	GIMModulePath = "/sys/module/gim"

	// PCIDevicePath is the sysfs path where PCI devices are enumerated.
	PCIDevicePath = "/sys/bus/pci/devices/"

	// AMDVendorID is the PCI vendor ID for AMD.
	AMDVendorID = "0x1002"

	// VFIODevicesRoot is the devfs path for VFIO device files.
	VFIODevicesRoot = "/dev/vfio"

	// KernelIOMMUGroupPath is the sysfs path for IOMMU group enumeration.
	KernelIOMMUGroupPath = "/sys/kernel/iommu_groups"

	// VFIOPCIModule is the kernel module name for vfio-pci.
	VFIOPCIModule = "vfio_pci"

	// VFIOModulePath is the sysfs path to check if vfio_pci module is loaded.
	VFIOModulePath = "/sys/module/vfio_pci"
)

// PFInfo holds metadata for a Physical Function discovered in VFIO passthrough mode.
type PFInfo struct {
	// PCIAddress is the PCI BDF address (e.g. "0000:c0:00.0").
	PCIAddress string
	// DeviceID is the PCI device ID (e.g. "0x7400").
	DeviceID string
	// VendorID is the PCI vendor ID (always "0x1002" for AMD).
	VendorID string
	// IOMMUGroup is the IOMMU group number for this device.
	IOMMUGroup string
	// ProductName is the human-readable device name from sysfs.
	ProductName string
	// NumaNode is the NUMA node affinity.
	NumaNode int
}

// VFInfo holds metadata for a Virtual Function discovered via SR-IOV.
type VFInfo struct {
	// ParentPCIAddress is the PCI address of the parent PF.
	ParentPCIAddress string
	// PCIAddress is the PCI BDF address of the VF.
	PCIAddress string
	// DeviceID is the PCI device ID of the VF.
	DeviceID string
	// VendorID is the PCI vendor ID (always "0x1002" for AMD).
	VendorID string
	// IOMMUGroup is the IOMMU group number for this VF.
	IOMMUGroup string
	// ProductName is the human-readable device name from sysfs.
	ProductName string
	// NumaNode is the NUMA node affinity.
	NumaNode int
}

// GetPFMapping scans PCI devices for AMD GPUs whose Physical Functions are
// bound to the vfio-pci driver (PF passthrough mode). Returns a map keyed by
// IOMMU group ID, with each value being the list of PFs in that group.
//
// This is ported from the AMD k8s-device-plugin gpu-virtualization branch.
func GetPFMapping() (map[string][]PFInfo, error) {
	pfMap := make(map[string][]PFInfo)

	entries, err := os.ReadDir(PCIDevicePath)
	if err != nil {
		return nil, fmt.Errorf("error reading %s: %v", PCIDevicePath, err)
	}

	for _, entry := range entries {
		pciAddr := entry.Name()
		pciPath := filepath.Join(PCIDevicePath, pciAddr)

		// Only consider AMD devices.
		vendor, err := readSysfsFile(filepath.Join(pciPath, "vendor"))
		if err != nil {
			continue
		}
		if vendor != AMDVendorID {
			continue
		}

		// Check if this device is bound to vfio-pci.
		driverLink := filepath.Join(pciPath, "driver")
		driver, err := os.Readlink(driverLink)
		if err != nil {
			continue
		}
		if filepath.Base(driver) != VFIODriverName {
			continue
		}

		// Get IOMMU group.
		iommuGroup, err := GetIOMMUGroup(pciAddr)
		if err != nil {
			glog.Warningf("Failed to get IOMMU group for %s: %v", pciAddr, err)
			continue
		}

		deviceID, _ := readSysfsFile(filepath.Join(pciPath, "device"))
		productName := readProductName(pciAddr)
		numaNode := readNumaNode(pciPath)

		pfInfo := PFInfo{
			PCIAddress:  pciAddr,
			DeviceID:    deviceID,
			VendorID:    vendor,
			IOMMUGroup:  iommuGroup,
			ProductName: productName,
			NumaNode:    numaNode,
		}
		pfMap[iommuGroup] = append(pfMap[iommuGroup], pfInfo)

		glog.Infof("VFIO PF: %s IOMMU group: %s device: %s", pciAddr, iommuGroup, deviceID)
	}
	return pfMap, nil
}

// GetVFMapping scans PCI devices for AMD GPUs whose Physical Functions are
// bound to the GIM driver (SR-IOV) and discovers their Virtual Functions.
// Returns a map keyed by IOMMU group ID, with each value being the list of
// VFs in that group.
//
// This is ported from the AMD k8s-device-plugin gpu-virtualization branch.
func GetVFMapping() (map[string][]VFInfo, error) {
	vfMap := make(map[string][]VFInfo)

	entries, err := os.ReadDir(PCIDevicePath)
	if err != nil {
		return nil, fmt.Errorf("error reading %s: %v", PCIDevicePath, err)
	}

	for _, entry := range entries {
		pfAddr := entry.Name()
		pciPath := filepath.Join(PCIDevicePath, pfAddr)

		// Only consider AMD devices.
		vendor, err := readSysfsFile(filepath.Join(pciPath, "vendor"))
		if err != nil {
			continue
		}
		if vendor != AMDVendorID {
			continue
		}

		// Check if this PF is managed by the GIM driver.
		driverLink := filepath.Join(pciPath, "driver")
		driver, err := os.Readlink(driverLink)
		if err != nil {
			continue
		}
		if filepath.Base(driver) != GIMDriverName {
			continue
		}

		// Look for SR-IOV VFs (symlinks named "virtfn*" under the PF).
		vfPattern := filepath.Join(pciPath, "virtfn*")
		vfPaths, err := filepath.Glob(vfPattern)
		if err != nil || len(vfPaths) == 0 {
			continue
		}

		pfProductName := readProductName(pfAddr)

		for _, vfPath := range vfPaths {
			vfTarget, err := os.Readlink(vfPath)
			if err != nil {
				continue
			}
			vfAddr := filepath.Base(vfTarget)
			vfFullPath := filepath.Join(PCIDevicePath, vfAddr)

			iommuGroup, err := GetIOMMUGroup(vfAddr)
			if err != nil {
				continue
			}

			deviceID, _ := readSysfsFile(filepath.Join(vfFullPath, "device"))
			vendorID, _ := readSysfsFile(filepath.Join(vfFullPath, "vendor"))
			numaNode := readNumaNode(vfFullPath)

			vfInfo := VFInfo{
				ParentPCIAddress: pfAddr,
				PCIAddress:       vfAddr,
				DeviceID:         deviceID,
				VendorID:         vendorID,
				IOMMUGroup:       iommuGroup,
				ProductName:      pfProductName,
				NumaNode:         numaNode,
			}
			vfMap[iommuGroup] = append(vfMap[iommuGroup], vfInfo)
			glog.Infof("VFIO VF: PF %s -> VF %s IOMMU group: %s", pfAddr, vfAddr, iommuGroup)
		}
	}
	return vfMap, nil
}

// GetIOMMUGroup returns the IOMMU group number for a PCI device.
func GetIOMMUGroup(pciAddr string) (string, error) {
	iommuLink := filepath.Join(PCIDevicePath, pciAddr, "iommu_group")
	target, err := os.Readlink(iommuLink)
	if err != nil {
		return "", fmt.Errorf("failed to read iommu_group link for %s: %w", pciAddr, err)
	}
	return filepath.Base(target), nil
}

// GetPCIDriver returns the kernel driver currently bound to a PCI device,
// or "" if no driver is bound.
func GetPCIDriver(pciAddr string) (string, error) {
	driverLink := filepath.Join(PCIDevicePath, pciAddr, "driver")
	target, err := os.Readlink(driverLink)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return filepath.Base(target), nil
}

// CheckVFIOModuleLoaded checks whether the vfio_pci kernel module is loaded.
func CheckVFIOModuleLoaded() bool {
	info, err := os.Stat(VFIOModulePath)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// CheckIOMMUEnabled checks whether IOMMU is enabled in the kernel by looking
// for entries in /sys/kernel/iommu_groups.
func CheckIOMMUEnabled() bool {
	f, err := os.Open(KernelIOMMUGroupPath)
	if err != nil {
		return false
	}
	defer f.Close()
	names, err := f.Readdirnames(1)
	return err == nil && len(names) > 0
}

// CheckGIMDriverLoaded checks if the AMD GIM driver is loaded.
func CheckGIMDriverLoaded() bool {
	_, err := os.Stat(GIMDriverPath)
	return err == nil
}

// CheckVFIODriverLoaded checks if the vfio-pci driver is available.
func CheckVFIODriverLoaded() bool {
	_, err := os.Stat(VFIODriverPath)
	return err == nil
}

// readSysfsFile reads a sysfs file and returns the trimmed content.
func readSysfsFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// readProductName reads the product_name from sysfs for a PCI device.
func readProductName(pciAddr string) string {
	// Try the DRM card path first.
	matches, _ := filepath.Glob(fmt.Sprintf("/sys/bus/pci/devices/%s/drm/card*/device/product_name", pciAddr))
	if len(matches) > 0 {
		if data, err := os.ReadFile(matches[0]); err == nil {
			replacer := strings.NewReplacer(" ", "_", "(", "", ")", "")
			return replacer.Replace(strings.TrimSpace(string(data)))
		}
	}
	// Fallback: try device/product_name directly.
	path := filepath.Join(PCIDevicePath, pciAddr, "product_name")
	if data, err := os.ReadFile(path); err == nil {
		replacer := strings.NewReplacer(" ", "_", "(", "", ")", "")
		return replacer.Replace(strings.TrimSpace(string(data)))
	}
	return ""
}

// readNumaNode reads the NUMA node from sysfs for a PCI device path.
func readNumaNode(pciPath string) int {
	data, err := os.ReadFile(filepath.Join(pciPath, "numa_node"))
	if err != nil {
		return -1
	}
	val := strings.TrimSpace(string(data))
	var node int
	fmt.Sscanf(val, "%d", &node)
	return node
}
