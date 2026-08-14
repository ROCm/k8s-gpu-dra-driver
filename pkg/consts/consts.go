/*
 * Copyright 2025 The Kubernetes Authors.
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

package consts

const DriverName = "gpu.amd.com"

// PCI and kernel driver constants
const (
	AMDVendorID    = "0x1002"
	VFIODriverName = "vfio-pci"
	GIMDriverName  = "gim"
	VFIOPCIModule  = "vfio_pci"
)

// Device type constants for ResourceSlice attributes
const (
	AmdGpuDeviceType             = "amdgpu"
	AmdPartitionDeviceType       = "amdgpu-partition"
	SyntheticPartitionDeviceType = "amdgpu-synthetic-partition"
	VfioDeviceType               = "vfio"
	UnknownDeviceType            = "unknown"
)

// Compute partition modes
const (
	ComputePartitionSPX = "spx"
	ComputePartitionDPX = "dpx"
	ComputePartitionQPX = "qpx"
	ComputePartitionCPX = "cpx"
)

// Memory partition modes
const (
	MemoryPartitionNPS1 = "nps1"
	MemoryPartitionNPS2 = "nps2"
	MemoryPartitionNPS4 = "nps4"
)

// Default partition profile for non-partitioned GPUs
const DefaultPartitionProfile = "spx_nps1"

// PartitionConfig defines a valid compute+memory combination
type PartitionConfig struct {
	Compute        string
	Memory         string
	PartitionCount int
}

// ValidPartitionConfigs is the compatibility matrix of compute+memory combinations
var ValidPartitionConfigs = []PartitionConfig{
	{ComputePartitionSPX, MemoryPartitionNPS1, 1},
	{ComputePartitionDPX, MemoryPartitionNPS1, 2},
	{ComputePartitionDPX, MemoryPartitionNPS2, 2},
	{ComputePartitionQPX, MemoryPartitionNPS1, 4},
	{ComputePartitionCPX, MemoryPartitionNPS1, 8},
	{ComputePartitionCPX, MemoryPartitionNPS4, 8},
}

// PartitionCountMap maps compute partition mode to the number of partitions it creates
var PartitionCountMap = map[string]int{
	ComputePartitionSPX: 1,
	ComputePartitionDPX: 2,
	ComputePartitionQPX: 4,
	ComputePartitionCPX: 8,
}

// MemoryPartitionTaintKey is the taint key used for memory partition conflicts
const MemoryPartitionTaintKey = "gpu.amd.com/memory-partition-conflict"
