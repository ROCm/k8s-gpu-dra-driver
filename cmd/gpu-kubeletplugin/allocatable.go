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

	resourceapi "k8s.io/api/resource/v1"
)

// AllocatableDevices represents a collection of allocatable devices mapped by their canonical names
type AllocatableDevices map[string]*AllocatableDevice

// AllocatableDevice wraps either a full AMD GPU or a partition
type AllocatableDevice struct {
	AmdGpu       *AmdGpuInfo
	AmdPartition *AmdPartitionInfo
}

// Type returns the device type (amdgpu or amdgpu-partition)
func (d *AllocatableDevice) Type() string {
	if d.AmdGpu != nil {
		return AmdGpuDeviceType
	}
	if d.AmdPartition != nil {
		return AmdPartitionDeviceType
	}
	return UnknownDeviceType
}

// CanonicalName returns the canonical device name
func (d *AllocatableDevice) CanonicalName() string {
	switch d.Type() {
	case AmdGpuDeviceType:
		return d.AmdGpu.CanonicalName()
	case AmdPartitionDeviceType:
		return d.AmdPartition.CanonicalName()
	}
	panic(fmt.Sprintf("unexpected device type: %s", d.Type()))
}

// GetDevice returns the DRA Device representation for Kubernetes
func (d *AllocatableDevice) GetDevice() resourceapi.Device {
	switch d.Type() {
	case AmdGpuDeviceType:
		return d.AmdGpu.GetDevice()
	case AmdPartitionDeviceType:
		return d.AmdPartition.GetDevice()
	}
	panic(fmt.Sprintf("unexpected device type: %s", d.Type()))
}
