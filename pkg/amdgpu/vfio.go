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

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/consts"
	"github.com/golang/glog"
)

var (
	PCIDevicePath        = "/sys/bus/pci/devices/"
	PCIDriversPath       = "/sys/bus/pci/drivers"
	VFIODriverPath       = "/sys/bus/pci/drivers/vfio-pci"
	GIMDriverPath        = "/sys/bus/pci/drivers/gim"
	GIMModulePath        = "/sys/module/gim"
	KernelIOMMUGroupPath = "/sys/kernel/iommu_groups"
	VFIOModulePath       = "/sys/module/vfio_pci"
	VFIODevicesRoot      = "/dev/vfio"
)

// SetSysfsRoot rebases all sysfs/devfs path variables under the given root.
// Used by tests to redirect I/O to a tmpdir-backed fake sysfs.
func SetSysfsRoot(root string) {
	PCIDevicePath = filepath.Join(root, "sys/bus/pci/devices") + "/"
	PCIDriversPath = filepath.Join(root, "sys/bus/pci/drivers")
	VFIODriverPath = filepath.Join(root, "sys/bus/pci/drivers/vfio-pci")
	GIMDriverPath = filepath.Join(root, "sys/bus/pci/drivers/gim")
	GIMModulePath = filepath.Join(root, "sys/module/gim")
	KernelIOMMUGroupPath = filepath.Join(root, "sys/kernel/iommu_groups")
	VFIOModulePath = filepath.Join(root, "sys/module/vfio_pci")
	VFIODevicesRoot = filepath.Join(root, "dev/vfio")
}

// ResetSysfsRoot restores all path variables to their real system defaults.
func ResetSysfsRoot() {
	PCIDevicePath = "/sys/bus/pci/devices/"
	PCIDriversPath = "/sys/bus/pci/drivers"
	VFIODriverPath = "/sys/bus/pci/drivers/vfio-pci"
	GIMDriverPath = "/sys/bus/pci/drivers/gim"
	GIMModulePath = "/sys/module/gim"
	KernelIOMMUGroupPath = "/sys/kernel/iommu_groups"
	VFIOModulePath = "/sys/module/vfio_pci"
	VFIODevicesRoot = "/dev/vfio"
}

// PFInfo holds metadata for a Physical Function already bound to vfio-pci
// by the GPU Operator (pf-passthrough mode). The DRA driver discovers these
// but does not manage their driver binding.
type PFInfo struct {
	PCIAddress  string
	DeviceID    string
	VendorID    string
	IOMMUGroup  string
	ProductName string
	NumaNode    int
}

// GetPFMapping discovers AMD GPU PFs that are already bound to vfio-pci
// (managed by the GPU Operator in pf-passthrough mode). The DRA driver does
// not bind or unbind PFs — it only discovers them for allocation.
func GetPFMapping() (map[string][]PFInfo, error) {
	pfMap := make(map[string][]PFInfo)

	entries, err := os.ReadDir(PCIDevicePath)
	if err != nil {
		return nil, fmt.Errorf("error reading %s: %v", PCIDevicePath, err)
	}

	for _, entry := range entries {
		pciAddr := entry.Name()
		pciPath := filepath.Join(PCIDevicePath, pciAddr)

		vendor, err := readSysfsFile(filepath.Join(pciPath, "vendor"))
		if err != nil || vendor != consts.AMDVendorID {
			continue
		}

		driver, err := GetPCIDriver(pciAddr)
		if err != nil || driver != consts.VFIODriverName {
			continue
		}

		// Skip VFs — they have a physfn symlink pointing to their parent PF.
		if _, err := os.Readlink(filepath.Join(pciPath, "physfn")); err == nil {
			continue
		}

		iommuGroup, err := GetIOMMUGroup(pciAddr)
		if err != nil {
			glog.Warningf("Failed to get IOMMU group for %s: %v", pciAddr, err)
			continue
		}

		deviceID, _ := readSysfsFile(filepath.Join(pciPath, "device"))
		productName := readProductName(pciAddr)
		numaNode := readNumaNode(pciPath)

		pfMap[iommuGroup] = append(pfMap[iommuGroup], PFInfo{
			PCIAddress:  pciAddr,
			DeviceID:    deviceID,
			VendorID:    vendor,
			IOMMUGroup:  iommuGroup,
			ProductName: productName,
			NumaNode:    numaNode,
		})
		glog.Infof("VFIO PF (pre-bound): %s IOMMU group: %s device: %s", pciAddr, iommuGroup, deviceID)
	}
	return pfMap, nil
}

// VFInfo holds metadata for a Virtual Function discovered via GIM SR-IOV.
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
	TotalVFs int
	NumVFs   int
}

func readSysfsInt(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var val int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &val); err != nil {
		return 0
	}
	return val
}

// ReadSRIOVTotalVFs reads the maximum number of SR-IOV VFs supported by a PF.
func ReadSRIOVTotalVFs(pfPCIAddress string) int {
	return readSysfsInt(filepath.Join(PCIDevicePath, pfPCIAddress, "sriov_totalvfs"))
}

// ReadSRIOVNumVFs reads the number of SR-IOV VFs currently active on a PF.
func ReadSRIOVNumVFs(pfPCIAddress string) int {
	return readSysfsInt(filepath.Join(PCIDevicePath, pfPCIAddress, "sriov_numvfs"))
}

// GetVFMapping scans for AMD GPU Virtual Functions created by the GIM SR-IOV
// driver. It finds PFs managed by GIM and discovers their VFs via virtfn*
// symlinks. Returns a map keyed by IOMMU group ID. Only VFs that are unbound
// or already on vfio-pci are included — VFs bound to other drivers (e.g.
// amdgpu) are skipped to avoid yanking them from active workloads.
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
		if vendor != consts.AMDVendorID {
			continue
		}

		// Check if this PF is managed by the GIM driver.
		driverLink := filepath.Join(pciPath, "driver")
		driver, err := os.Readlink(driverLink)
		if err != nil {
			continue
		}
		if filepath.Base(driver) != consts.GIMDriverName {
			continue
		}

		// Look for SR-IOV VFs (symlinks named "virtfn*" under the PF).
		vfPattern := filepath.Join(pciPath, "virtfn*")
		vfPaths, err := filepath.Glob(vfPattern)
		if err != nil || len(vfPaths) == 0 {
			continue
		}

		pfProductName := readProductName(pfAddr)
		pfTotalVFs := ReadSRIOVTotalVFs(pfAddr)
		pfNumVFs := ReadSRIOVNumVFs(pfAddr)

		for _, vfPath := range vfPaths {
			vfTarget, err := os.Readlink(vfPath)
			if err != nil {
				continue
			}
			vfAddr := filepath.Base(vfTarget)
			vfFullPath := filepath.Join(PCIDevicePath, vfAddr)

			// Skip VFs already bound to a driver other than vfio-pci.
			// Unbound VFs (driver == "") are eligible for VFIO binding.
			vfDriver, err := GetPCIDriver(vfAddr)
			if err != nil {
				continue
			}
			if vfDriver != "" && vfDriver != consts.VFIODriverName {
				glog.V(2).Infof("Skipping VF %s: bound to %s", vfAddr, vfDriver)
				continue
			}

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
				TotalVFs:         pfTotalVFs,
				NumVFs:           pfNumVFs,
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

// GetPFAddress returns the PCI address of the parent PF for a VF.
// Returns "" if the device is not a VF (no physfn link).
func GetPFAddress(vfAddr string) (string, error) {
	physfnLink := filepath.Join(PCIDevicePath, vfAddr, "physfn")
	target, err := os.Readlink(physfnLink)
	if err != nil {
		return "", err
	}
	return filepath.Base(target), nil
}

// GetPCIDriver returns the kernel driver currently bound to a PCI device,
// or "" if no driver is bound.
func GetPCIDriver(pciAddr string) (string, error) {
	devicePath := filepath.Join(PCIDevicePath, pciAddr)
	if _, err := os.Stat(devicePath); err != nil {
		return "", fmt.Errorf("PCI device %s not found in sysfs: %w", pciAddr, err)
	}
	driverLink := filepath.Join(devicePath, "driver")
	target, err := os.Readlink(driverLink)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // no driver bound
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

// ReadPFCapacity reads the total memory for a GIM-managed PF from sysfs.
// Returns [memoryBytes, cuCount, simdCount]. CU and SIMD counts are inferred
// from the PF device ID since GIM does not expose topology files.
func ReadPFCapacity(pfAddr string) [3]uint64 {
	var mem uint64
	memStr, _ := readSysfsFile(filepath.Join(PCIDevicePath, pfAddr, "mem_info_vram_total"))
	if memStr != "" {
		fmt.Sscanf(memStr, "%d", &mem)
	}
	deviceID, _ := readSysfsFile(filepath.Join(PCIDevicePath, pfAddr, "device"))
	cu, simd := gpuCapacityByDeviceID(deviceID)
	return [3]uint64{mem, uint64(cu), uint64(simd)}
}

// gpuCapacityByDeviceID returns (cuCount, simdCount) for known AMD GPU PF device IDs.
func gpuCapacityByDeviceID(deviceID string) (int, int) {
	switch deviceID {
	case "0x75a3": // MI355X PF — 8 XCCs, 32 CU/XCC, 4 SIMD/CU
		return 256, 1024
	case "0x740f": // MI300X PF — 8 XCCs, 32 CU/XCC, 4 SIMD/CU
		return 304, 1216
	case "0x74a0": // MI325X PF — 8 XCCs, 38 CU/XCC, 4 SIMD/CU
		return 304, 1216
	default:
		return 0, 0
	}
}

// readProductName reads the product_name from sysfs for a PCI device.
func readProductName(pciAddr string) string {
	// Try the DRM card path first.
	matches, _ := filepath.Glob(filepath.Join(PCIDevicePath, pciAddr, "drm/card*/device/product_name"))
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
	if _, err := fmt.Sscanf(val, "%d", &node); err != nil {
		return -1
	}
	return node
}
