/*
 * Copyright 2023 The Kubernetes Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

/*
Copyright (c) Advanced Micro Devices, Inc. All rights reserved.

Licensed under the Apache License, Version 2.0 (the \"License\");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an \"AS IS\" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"fmt"
	"sort"

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/amdgpu"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/consts"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/featuregates"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	klog "k8s.io/klog/v2"
)

func parseDeviceName(name string) (int, int, error) {
	var card, renderD int
	_, err := fmt.Sscanf(name, "gpu-%d-%d", &card, &renderD)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to parse device name %s: %v", name, err)
	}
	return card, renderD, nil
}

// Helper function to extract topology information from GPU info map
func extractTopologyInfo(gpuInfoMap map[string]interface{}) (simdUnits, computeUnits int) {
	if simdCount, ok := gpuInfoMap["simdCount"].(int); ok {
		simdUnits = simdCount
	}
	if cuCount, ok := gpuInfoMap["cuCount"].(int); ok {
		computeUnits = cuCount
	}
	return
}

// getMemoryBytes returns the device VRAM in bytes. 0 is a sentinel for unreadable
// VRAM, not a measured capacity; discovery re-runs on restart.
func getMemoryBytes(gpuInfoMap map[string]interface{}, deviceType, pciAddr string) uint64 {
	if vramBytes, ok := gpuInfoMap["vramBytes"].(uint64); ok && vramBytes > 0 {
		return vramBytes
	}
	klog.Warningf("VRAM info not available for %s %s, reporting 0", deviceType, pciAddr)
	return 0
}

func getPcieInfo(gpuInfoMap map[string]interface{}) (deviceattribute.DeviceAttribute, deviceattribute.DeviceAttribute, string, error) {
	pciAddr := gpuInfoMap["pciAddr"].(string)
	// The PCIe root and bus-id attributes are optional NUMA/topology hints; the caller
	// keeps going without them. The device's own BDF is known regardless, so return it
	// even on error rather than an empty string that would leave a partition's parent
	// PCIAddress blank.
	pcieRootAttr, err := deviceattribute.GetPCIeRootAttributeByPCIBusID(pciAddr)
	if err != nil {
		return pcieRootAttr, deviceattribute.DeviceAttribute{}, pciAddr, fmt.Errorf("Failed to get PCIe root attribute for device %s: %v", pciAddr, err)
	}
	pciBusIDAttr, err := deviceattribute.GetPCIBusIDAttribute(pciAddr)
	if err != nil {
		return pcieRootAttr, pciBusIDAttr, pciAddr, fmt.Errorf("Failed to get PCI Bus ID attribute for device %s: %v", pciAddr, err)
	}
	return pcieRootAttr, pciBusIDAttr, pciAddr, nil
}

// addAllocatableDevice inserts a device under its canonical name, returning an
// error on a name collision instead of silently overwriting a device already
// discovered under the same name.
func addAllocatableDevice(devices AllocatableDevices, device *AllocatableDevice) error {
	name := device.CanonicalName()
	if _, exists := devices[name]; exists {
		return fmt.Errorf("duplicate device identity %q", name)
	}
	devices[name] = device
	return nil
}

// isKnownComputePartition reports whether t is a compute-partition mode this driver
// models. spx is a full GPU and is handled separately. A type outside this set means the
// driver cannot model the device's partitioning, so discovery skips it rather than
// publishing a device with an unhandled profile; a new AMD mode has to be added here.
func isKnownComputePartition(t string) bool {
	switch t {
	case consts.ComputePartitionDPX, consts.ComputePartitionQPX, consts.ComputePartitionCPX:
		return true
	default:
		return false
	}
}

func enumerateAllPossibleDevices() (AllocatableDevices, error) {
	alldevices := make(AllocatableDevices)
	allAMDGPUs := amdgpu.GetAMDGPUs()

	for pciAddr, gpuInfoMap := range allAMDGPUs {
		// Get PCIe root attribute for this device using the PCI address from the device info
		pcieRootAttr, pciBusIDAttr, pciAddrFromMap, err := getPcieInfo(gpuInfoMap)
		if err != nil {
			// Continue without PCIe root attribute rather than failing completely
			klog.Warning(err.Error())
		}

		// Check compute partition type to determine device type
		computePartitionType := gpuInfoMap["computePartitionType"].(string)
		memoryPartitionType := gpuInfoMap["memoryPartitionType"].(string)

		// Extract common topology information
		simdUnits, computeUnits := extractTopologyInfo(gpuInfoMap)

		if computePartitionType == consts.ComputePartitionSPX || computePartitionType == "" {
			// This is a full AMD GPU (either explicitly "spx" or no partition support)
			partitionProfile := ""
			if computePartitionType != "" && memoryPartitionType != "" {
				partitionProfile = fmt.Sprintf("%s_%s", computePartitionType, memoryPartitionType)
			}

			amdGpuInfo := &AmdGpuInfo{
				PCIAddress:       pciAddr,
				cardIndex:        gpuInfoMap["card"].(int),
				renderIndex:      gpuInfoMap["renderD"].(int),
				KFDID:            gpuInfoMap["kfdID"].(string),
				DeviceID:         gpuInfoMap["deviceID"].(string),
				DriverVersion:    gpuInfoMap["driverVersion"].(string),
				PartitionProfile: partitionProfile,
				ProductName:      gpuInfoMap["productName"].(string),
				pcieRootAttr:     pcieRootAttr,
				pciBusIDAttr:     pciBusIDAttr,
				SimdUnits:        simdUnits,
				ComputeUnits:     computeUnits,
				NumaNode:         gpuInfoMap["numaNode"].(int),
				MemoryBytes:      getMemoryBytes(gpuInfoMap, "device", pciAddr),
			}

			// Create allocatable device for the full GPU
			device := &AllocatableDevice{
				AmdGpu: amdGpuInfo,
			}
			if err := addAllocatableDevice(alldevices, device); err != nil {
				return nil, err
			}

			klog.Infof("Found full AMD GPU: %s, compute type: %s, memory type: %s",
				device.CanonicalName(), computePartitionType, memoryPartitionType)
		} else if isKnownComputePartition(computePartitionType) {
			// This is a partition - create both parent GPU info and partition info.
			// A well-formed profile needs both halves; an empty memory type would produce
			// a truncated "dpx_" profile, so skip an incomplete one rather than publish it.
			if memoryPartitionType == "" {
				klog.Warningf("Skipping partition for device %s: compute type %q has no memory partition type", pciAddr, computePartitionType)
				continue
			}

			// Create parent GPU info
			parentGpuInfo := &AmdGpuInfo{
				PCIAddress:    pciAddrFromMap,
				KFDID:         gpuInfoMap["kfdID"].(string),
				DeviceID:      gpuInfoMap["deviceID"].(string),
				DriverVersion: gpuInfoMap["driverVersion"].(string),
				ProductName:   gpuInfoMap["productName"].(string),
				pcieRootAttr:  pcieRootAttr,
				pciBusIDAttr:  pciBusIDAttr,
			}

			// Create partition info
			partitionInfo := &AmdPartitionInfo{
				cardIndex:        gpuInfoMap["card"].(int),
				renderIndex:      gpuInfoMap["renderD"].(int),
				Parent:           parentGpuInfo,
				PartitionProfile: fmt.Sprintf("%s_%s", computePartitionType, memoryPartitionType),
				SimdUnits:        simdUnits,
				ComputeUnits:     computeUnits,
				NumaNode:         gpuInfoMap["numaNode"].(int),
				MemoryBytes:      getMemoryBytes(gpuInfoMap, "partition", pciAddr),
			}

			// Create allocatable device for the partition
			device := &AllocatableDevice{
				AmdPartition: partitionInfo,
			}
			if err := addAllocatableDevice(alldevices, device); err != nil {
				return nil, err
			}

			klog.Infof("Found AMD GPU partition: %s, compute type: %s, memory type: %s",
				device.CanonicalName(), computePartitionType, memoryPartitionType)
		} else {
			klog.Warningf("Skipping device %s: unsupported compute partition type %q", pciAddr, computePartitionType)
		}
	}

	klog.Infof("Discovered %d AMD GPU devices", len(alldevices))

	// Discover VFIO passthrough devices:
	// - PFs already bound to vfio-pci by the GPU Operator (pf-passthrough mode)
	// - GIM SR-IOV VFs (vf-passthrough mode)
	vfioIndex := 0

	if featuregates.Enabled(featuregates.VFIOPassthrough) {
		pfMap, err := amdgpu.GetPFMapping()
		if err != nil {
			klog.V(2).Infof("No VFIO PF devices found: %v", err)
		} else {
			pfKeys := make([]string, 0, len(pfMap))
			for k := range pfMap {
				pfKeys = append(pfKeys, k)
			}
			sort.Strings(pfKeys)
			for _, k := range pfKeys {
				// Sort PFs within an IOMMU group by PCI address for stability.
				pfs := pfMap[k]
				sort.Slice(pfs, func(i, j int) bool { return pfs[i].PCIAddress < pfs[j].PCIAddress })
				for _, pf := range pfs {
					pcieRootAttr, err := deviceattribute.GetPCIeRootAttributeByPCIBusID(pf.PCIAddress)
					if err != nil {
						klog.Warningf("Failed to get PCIe root for VFIO PF %s: %v", pf.PCIAddress, err)
					}
					pciBusIDAttr, _ := deviceattribute.GetPCIBusIDAttribute(pf.PCIAddress)
					device := &AmdGpuVFIOInfo{
						PCIAddress:         pf.PCIAddress,
						DeviceID:           pf.DeviceID,
						VendorID:           pf.VendorID,
						IOMMUGroup:         pf.IOMMUGroup,
						Index:              vfioIndex,
						ProductName:        pf.ProductName,
						NumaNode:           pf.NumaNode,
						IsVF:               false,
						pciBusIDAttr:       pciBusIDAttr,
						pcieRootAttr:       pcieRootAttr,
						preConfigureDriver: consts.VFIODriverName,
					}
					alldevices[device.CanonicalName()] = &AllocatableDevice{Vfio: device}
					klog.Infof("Found VFIO PF device (pre-bound): %s (PCI: %s, IOMMU: %s)", device.CanonicalName(), pf.PCIAddress, pf.IOMMUGroup)
					vfioIndex++
				}
			}
		}
	}

	if featuregates.Enabled(featuregates.VFIOPassthrough) {
		vfMap, err := amdgpu.GetVFMapping()
		if err != nil {
			klog.V(2).Infof("No VFIO VF devices found: %v", err)
		} else {
			vfKeys := make([]string, 0, len(vfMap))
			for k := range vfMap {
				vfKeys = append(vfKeys, k)
			}
			sort.Strings(vfKeys)
			for _, k := range vfKeys {
				vfs := vfMap[k]
				sort.Slice(vfs, func(i, j int) bool { return vfs[i].PCIAddress < vfs[j].PCIAddress })
				for _, vf := range vfs {
					pcieRootAttr, err := deviceattribute.GetPCIeRootAttributeByPCIBusID(vf.PCIAddress)
					if err != nil {
						klog.Warningf("Failed to get PCIe root for VFIO VF %s: %v", vf.PCIAddress, err)
					}
					pciBusIDAttr, _ := deviceattribute.GetPCIBusIDAttribute(vf.PCIAddress)
					currentDriver, _ := amdgpu.GetPCIDriver(vf.PCIAddress)
					device := &AmdGpuVFIOInfo{
						PCIAddress:         vf.PCIAddress,
						DeviceID:           vf.DeviceID,
						VendorID:           vf.VendorID,
						IOMMUGroup:         vf.IOMMUGroup,
						Index:              vfioIndex,
						ProductName:        vf.ProductName,
						NumaNode:           vf.NumaNode,
						IsVF:               true,
						pciBusIDAttr:       pciBusIDAttr,
						pcieRootAttr:       pcieRootAttr,
						preConfigureDriver: currentDriver,
					}
					alldevices[device.CanonicalName()] = &AllocatableDevice{Vfio: device}
					klog.Infof("Found VFIO VF device: %s (PCI: %s, PF: %s, IOMMU: %s)", device.CanonicalName(), vf.PCIAddress, vf.ParentPCIAddress, vf.IOMMUGroup)
					vfioIndex++
				}
			}
		}
	}

	if vfioIndex > 0 {
		klog.Infof("Discovered %d VFIO passthrough devices", vfioIndex)
	}

	return alldevices, nil
}
