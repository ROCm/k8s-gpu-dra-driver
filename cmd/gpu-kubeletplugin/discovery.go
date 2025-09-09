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
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/klog/v2"
)

func getDeviceName(card, renderD int) string {
	return fmt.Sprintf("gpu-%v-%v", card, renderD)
}

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

func getPcieInfo(gpuInfoMap map[string]interface{}) (deviceattribute.DeviceAttribute, string, error) {
	pciAddr := gpuInfoMap["pciAddr"].(string)

	// Use the PCI address from the device info (which is the parent's PCI address for partitions)
	pcieRootAttr, err := deviceattribute.GetPCIeRootAttributeByPCIBusID(pciAddr)
	if err != nil {
		return pcieRootAttr, "", fmt.Errorf("Failed to get PCIe root attribute for device %s (using PCI addr %s): %v", pciAddr, pciAddr, err)
	}

	return pcieRootAttr, pciAddr, nil
}

func enumerateAllPossibleDevices() (AllocatableDevices, error) {
	alldevices := make(AllocatableDevices)
	allAMDGPUs := amdgpu.GetAMDGPUs()

	for pciAddr, gpuInfoMap := range allAMDGPUs {
		// Get PCIe root attribute for this device using the PCI address from the device info
		pcieRootAttr, pciAddrFromMap, err := getPcieInfo(gpuInfoMap)
		if err != nil {
			// Continue without PCIe root attribute rather than failing completely
			klog.Warningf(err.Error())
		}

		// Check compute partition type to determine device type
		computePartitionType := gpuInfoMap["computePartitionType"].(string)
		memoryPartitionType := gpuInfoMap["memoryPartitionType"].(string)

		// Extract common topology information
		simdUnits, computeUnits := extractTopologyInfo(gpuInfoMap)

		if computePartitionType == "spx" {
			// This is a full AMD GPU
			amdGpuInfo := &AmdGpuInfo{
				PCIAddress:       pciAddr,
				CardIndex:        gpuInfoMap["card"].(int),
				RenderIndex:      gpuInfoMap["renderD"].(int),
				DeviceID:         gpuInfoMap["devID"].(string),
				DriverVersion:    gpuInfoMap["driverVersion"].(string),
				DriverSrcVersion: gpuInfoMap["driverSrcVersion"].(string),
				PartitionProfile: fmt.Sprintf("%s_%s", computePartitionType, memoryPartitionType),
				Family:           gpuInfoMap["family"].(string),
				ProductName:      gpuInfoMap["productName"].(string),
				pcieRootAttr:     pcieRootAttr,
				SimdUnits:        simdUnits,
				ComputeUnits:     computeUnits,
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
				PCIAddress:       pciAddrFromMap,
				DeviceID:         gpuInfoMap["devID"].(string),
				DriverVersion:    gpuInfoMap["driverVersion"].(string),
				DriverSrcVersion: gpuInfoMap["driverSrcVersion"].(string),
				Family:           gpuInfoMap["family"].(string),
				ProductName:      gpuInfoMap["productName"].(string),
				pcieRootAttr:     pcieRootAttr,
			}

			// Create partition info
			partitionInfo := &AmdPartitionInfo{
				CardIndex:        gpuInfoMap["card"].(int),
				RenderIndex:      gpuInfoMap["renderD"].(int),
				Parent:           parentGpuInfo,
				PartitionProfile: fmt.Sprintf("%s_%s", computePartitionType, memoryPartitionType),
				SimdUnits:        simdUnits,
				ComputeUnits:     computeUnits,
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
	return alldevices, nil
}
