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
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/utils/ptr"

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/amdgpu"
)

// AmdGpuInfo represents a full AMD GPU device
type AmdGpuInfo struct {
	UUID             string
	ProductName      string
	KFDID            string // KFD-derived PCI address for internal parent-child tracking
	DeviceID         string // sysfs PCI device ID (e.g., "0x740f")
	DriverVersion    string
	PCIAddress       string
	PartitionProfile string
	MemoryBytes      uint64
	ComputeUnits     int
	SimdUnits        int
	NumaNode         int
	cardIndex        int // unexported: for CanonicalName and CDI path derivation
	renderIndex      int // unexported: for CanonicalName and CDI path derivation
	pcieRootAttr     deviceattribute.DeviceAttribute
	pciBusIDAttr     deviceattribute.DeviceAttribute
}

// AmdPartitionInfo represents a partition of an AMD GPU
type AmdPartitionInfo struct {
	Parent           *AmdGpuInfo
	UUID             string
	PartitionProfile string
	MemoryBytes      uint64
	ComputeUnits     int
	SimdUnits        int
	NumaNode         int
	cardIndex        int // unexported: for CanonicalName and CDI path derivation
	renderIndex      int // unexported: for CanonicalName and CDI path derivation
}

// CanonicalName returns the canonical name for this GPU
func (d *AmdGpuInfo) CanonicalName() string {
	return fmt.Sprintf("gpu-%v-%v", d.cardIndex, d.renderIndex)
}

// GetDevice returns the DRA Device representation for a full AMD GPU
func (d *AmdGpuInfo) GetDevice() resourceapi.Device {
	attributes := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
		"type":        {StringValue: ptr.To(AmdGpuDeviceType)},
		"productName": {StringValue: ptr.To(d.ProductName)},
		"numaNode":    {IntValue: ptr.To(int64(d.NumaNode))},
	}
	if d.DriverVersion != "" {
		attributes["driverVersion"] = resourceapi.DeviceAttribute{VersionValue: ptr.To(amdgpu.SemverDriverVersion(d.DriverVersion))}
		attributes["driverVersionFull"] = resourceapi.DeviceAttribute{StringValue: ptr.To(d.DriverVersion)}
	}
	if d.DeviceID != "" {
		attributes["deviceID"] = resourceapi.DeviceAttribute{StringValue: ptr.To(d.DeviceID)}
	}
	if d.PartitionProfile != "" {
		attributes["partitionProfile"] = resourceapi.DeviceAttribute{StringValue: ptr.To(d.PartitionProfile)}
	}
	if d.pciBusIDAttr.Name != "" {
		attributes[d.pciBusIDAttr.Name] = d.pciBusIDAttr.Value
	}
	if d.pcieRootAttr.Name != "" {
		attributes[d.pcieRootAttr.Name] = d.pcieRootAttr.Value
	}
	return resourceapi.Device{
		Name:       d.CanonicalName(),
		Attributes: attributes,
		Capacity: map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
			"memory":       {Value: *resource.NewQuantity(int64(d.MemoryBytes), resource.BinarySI)},
			"computeUnits": {Value: *resource.NewQuantity(int64(d.ComputeUnits), resource.BinarySI)},
			"simdUnits":    {Value: *resource.NewQuantity(int64(d.SimdUnits), resource.BinarySI)},
		},
	}
}

// CanonicalName returns the canonical name for this partition
func (d *AmdPartitionInfo) CanonicalName() string {
	return fmt.Sprintf("gpu-%v-%v", d.cardIndex, d.renderIndex)
}

// GetDevice returns the DRA Device representation for an AMD GPU partition
func (d *AmdPartitionInfo) GetDevice() resourceapi.Device {
	attributes := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
		"type":             {StringValue: ptr.To(AmdPartitionDeviceType)},
		"productName":      {StringValue: ptr.To(d.Parent.ProductName)},
		"partitionProfile": {StringValue: ptr.To(d.PartitionProfile)},
		"numaNode":         {IntValue: ptr.To(int64(d.NumaNode))},
	}
	if d.Parent.DriverVersion != "" {
		attributes["driverVersion"] = resourceapi.DeviceAttribute{VersionValue: ptr.To(amdgpu.SemverDriverVersion(d.Parent.DriverVersion))}
		attributes["driverVersionFull"] = resourceapi.DeviceAttribute{StringValue: ptr.To(d.Parent.DriverVersion)}
	}
	if d.Parent.DeviceID != "" {
		attributes["deviceID"] = resourceapi.DeviceAttribute{StringValue: ptr.To(d.Parent.DeviceID)}
	}
	if d.Parent.pciBusIDAttr.Name != "" {
		attributes[d.Parent.pciBusIDAttr.Name] = d.Parent.pciBusIDAttr.Value
	}
	if d.Parent.pcieRootAttr.Name != "" {
		attributes[d.Parent.pcieRootAttr.Name] = d.Parent.pcieRootAttr.Value
	}
	return resourceapi.Device{
		Name:       d.CanonicalName(),
		Attributes: attributes,
		Capacity: map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
			"memory":       {Value: *resource.NewQuantity(int64(d.MemoryBytes), resource.BinarySI)},
			"computeUnits": {Value: *resource.NewQuantity(int64(d.ComputeUnits), resource.BinarySI)},
			"simdUnits":    {Value: *resource.NewQuantity(int64(d.SimdUnits), resource.BinarySI)},
		},
	}
}
