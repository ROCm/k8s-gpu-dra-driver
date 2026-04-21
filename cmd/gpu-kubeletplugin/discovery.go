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

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/amdgpu"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/consts"
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

// Helper function to get memory bytes with fallback
func getMemoryBytes(gpuInfoMap map[string]interface{}, defaultBytes uint64, deviceType, pciAddr string) uint64 {
	if vramBytes, ok := gpuInfoMap["vramBytes"].(uint64); ok && vramBytes > 0 {
		return vramBytes
	}
	// Fallback to default if VRAM parsing failed
	klog.Warningf("VRAM info not available for %s %s, using default %dGB", deviceType, pciAddr, defaultBytes/(1024*1024*1024))
	return defaultBytes
}

func getPcieInfo(gpuInfoMap map[string]interface{}) (deviceattribute.DeviceAttribute, deviceattribute.DeviceAttribute, string, error) {
	pciAddr := gpuInfoMap["pciAddr"].(string)
	pcieRootAttr, err := deviceattribute.GetPCIeRootAttributeByPCIBusID(pciAddr)
	if err != nil {
		return pcieRootAttr, deviceattribute.DeviceAttribute{}, "", fmt.Errorf("Failed to get PCIe root attribute for device %s: %v", pciAddr, err)
	}
	pciBusIDAttr, err := deviceattribute.GetPCIBusIDAttribute(pciAddr)
	if err != nil {
		return pcieRootAttr, pciBusIDAttr, "", fmt.Errorf("Failed to get PCI Bus ID attribute for device %s: %v", pciAddr, err)
	}
	return pcieRootAttr, pciBusIDAttr, pciAddr, nil
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
				MemoryBytes:      getMemoryBytes(gpuInfoMap, 80*1024*1024*1024, "device", pciAddr),
			}

			// Create allocatable device for the full GPU
			device := &AllocatableDevice{
				AmdGpu: amdGpuInfo,
			}
			alldevices[device.CanonicalName()] = device

			klog.Infof("Found full AMD GPU: %s, compute type: %s, memory type: %s",
				device.CanonicalName(), computePartitionType, memoryPartitionType)
		} else if computePartitionType != "" {
			// This is a partition - create both parent GPU info and partition info

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
				MemoryBytes:      getMemoryBytes(gpuInfoMap, 20*1024*1024*1024, "partition", pciAddr),
			}

			// Create allocatable device for the partition
			device := &AllocatableDevice{
				AmdPartition: partitionInfo,
			}
			alldevices[device.CanonicalName()] = device

			klog.Infof("Found AMD GPU partition: %s, compute type: %s, memory type: %s",
				device.CanonicalName(), computePartitionType, memoryPartitionType)
		} else {
			klog.Warningf("Unknown compute partition type '%s' for device %s, skipping", computePartitionType, pciAddr)
		}
	}

	klog.Infof("Discovered %d AMD GPU devices", len(alldevices))

	// Discover VFIO passthrough devices (PFs bound to vfio-pci and SR-IOV VFs)
	vfioIndex := 0
	pfMap, err := amdgpu.GetPFMapping()
	if err != nil {
		klog.V(2).Infof("No VFIO PF devices found: %v", err)
	} else {
		for _, pfs := range pfMap {
			for _, pf := range pfs {
				pcieRootAttr, err := deviceattribute.GetPCIeRootAttributeByPCIBusID(pf.PCIAddress)
				if err != nil {
					klog.Warningf("Failed to get PCIe root for VFIO PF %s: %v", pf.PCIAddress, err)
				}
				pciBusIDAttr, _ := deviceattribute.GetPCIBusIDAttribute(pf.PCIAddress)
				device := &AmdGpuVFIOInfo{
					PCIAddress:   pf.PCIAddress,
					IOMMUGroup:   pf.IOMMUGroup,
					Index:        vfioIndex,
					ProductName:  pf.ProductName,
					NumaNode:     pf.NumaNode,
					IsVF:         false,
					pciBusIDAttr: pciBusIDAttr,
					pcieRootAttr: pcieRootAttr,
				}
				alldevices[device.CanonicalName()] = &AllocatableDevice{Vfio: device}
				klog.Infof("Found VFIO PF device: %s (PCI: %s, IOMMU: %s)", device.CanonicalName(), pf.PCIAddress, pf.IOMMUGroup)
				vfioIndex++
			}
		}
	}

	vfMap, err := amdgpu.GetVFMapping()
	if err != nil {
		klog.V(2).Infof("No VFIO VF devices found: %v", err)
	} else {
		for _, vfs := range vfMap {
			for _, vf := range vfs {
				pcieRootAttr, err := deviceattribute.GetPCIeRootAttributeByPCIBusID(vf.PCIAddress)
				if err != nil {
					klog.Warningf("Failed to get PCIe root for VFIO VF %s: %v", vf.PCIAddress, err)
				}
				pciBusIDAttr, _ := deviceattribute.GetPCIBusIDAttribute(vf.PCIAddress)
				device := &AmdGpuVFIOInfo{
					PCIAddress:   vf.PCIAddress,
					IOMMUGroup:   vf.IOMMUGroup,
					Index:        vfioIndex,
					ProductName:  vf.ProductName,
					NumaNode:     vf.NumaNode,
					IsVF:         true,
					pciBusIDAttr: pciBusIDAttr,
					pcieRootAttr: pcieRootAttr,
				}
				alldevices[device.CanonicalName()] = &AllocatableDevice{Vfio: device}
				klog.Infof("Found VFIO VF device: %s (PCI: %s, PF: %s, IOMMU: %s)", device.CanonicalName(), vf.PCIAddress, vf.ParentPCIAddress, vf.IOMMUGroup)
				vfioIndex++
			}
		}
	}

	if vfioIndex > 0 {
		klog.Infof("Discovered %d VFIO passthrough devices", vfioIndex)
	}

	return alldevices, nil
}
