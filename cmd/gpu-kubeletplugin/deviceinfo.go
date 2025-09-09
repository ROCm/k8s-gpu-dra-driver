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

	"github.com/google/uuid"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/utils/ptr"
)

// AMD GPU namespace for deterministic UUID generation
var amdGpuNamespace = uuid.MustParse("12345678-1234-5678-9abc-123456789012")

// AmdGpuInfo represents a full AMD GPU device
type AmdGpuInfo struct {
	UUID             string
	RenderIndex      int
	CardIndex        int
	ProductName      string
	Family           string
	DeviceID         string
	DriverVersion    string
	DriverSrcVersion string
	PCIAddress       string
	PartitionProfile string
	MemoryBytes      uint64
	ComputeUnits     int
	SimdUnits        int
	// PCIe root attribute for topology awareness
	pcieRootAttr deviceattribute.DeviceAttribute
}

// AmdPartitionInfo represents a partition of an AMD GPU
type AmdPartitionInfo struct {
	Parent           *AmdGpuInfo // Reference to parent GPU
	UUID             string
	RenderIndex      int
	CardIndex        int
	PartitionProfile string
	MemoryBytes      uint64
	ComputeUnits     int
	SimdUnits        int
}

// CanonicalName returns the canonical name for this GPU
func (d *AmdGpuInfo) CanonicalName() string {
	return fmt.Sprintf("gpu-%v-%v", d.CardIndex, d.RenderIndex)
}

// GenerateUUID generates a deterministic UUID for this AMD GPU
func (d *AmdGpuInfo) GenerateUUID() string {
	uniqueID := fmt.Sprintf("%s-card%d-render%d", d.DeviceID, d.CardIndex, d.RenderIndex)
	return uuid.NewSHA1(amdGpuNamespace, []byte(uniqueID)).String()
}

// GetDevice returns the DRA Device representation for a full AMD GPU
func (d *AmdGpuInfo) GetDevice() resourceapi.Device {
	attributes := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
		"type": {
			StringValue: ptr.To(AmdGpuDeviceType),
		},
		"uuid": {
			StringValue: ptr.To(d.GenerateUUID()),
		},
		"pciAddr": {
			StringValue: ptr.To(d.PCIAddress),
		},
		"cardIndex": {
			IntValue: ptr.To(int64(d.CardIndex)),
		},
		"renderIndex": {
			IntValue: ptr.To(int64(d.RenderIndex)),
		},
		"deviceID": {
			StringValue: ptr.To(d.DeviceID),
		},
		"family": {
			StringValue: ptr.To(d.Family),
		},
		"productName": {
			StringValue: ptr.To(d.ProductName),
		},
		"driverVersion": {
			VersionValue: ptr.To(d.DriverVersion),
		},
		"driverSrcVersion": {
			StringValue: ptr.To(d.DriverSrcVersion),
		},
		"partitionProfile": {
			StringValue: ptr.To(d.PartitionProfile),
		},
	}

	// Add PCIe root attribute if available
	if d.pcieRootAttr.Name != "" {
		attributes[d.pcieRootAttr.Name] = d.pcieRootAttr.Value
	}

	return resourceapi.Device{
		Name:       d.CanonicalName(),
		Attributes: attributes,
		Capacity: map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
			"memory": {
				Value: *resource.NewQuantity(int64(d.MemoryBytes), resource.BinarySI),
			},
			"computeUnits": {
				Value: *resource.NewQuantity(int64(d.ComputeUnits), resource.BinarySI),
			},
			"simdUnits": {
				Value: *resource.NewQuantity(int64(d.SimdUnits), resource.BinarySI),
			},
		},
	}
}

// CanonicalName returns the canonical name for this partition
func (d *AmdPartitionInfo) CanonicalName() string {
	return fmt.Sprintf("gpu-%v-%v", d.CardIndex, d.RenderIndex)
}

// GenerateUUID generates a deterministic UUID for this AMD GPU partition
func (d *AmdPartitionInfo) GenerateUUID() string {
	uniqueID := fmt.Sprintf("%s-card%d-render%d", d.Parent.DeviceID, d.CardIndex, d.RenderIndex)
	return uuid.NewSHA1(amdGpuNamespace, []byte(uniqueID)).String()
}

// GetDevice returns the DRA Device representation for an AMD GPU partition
func (d *AmdPartitionInfo) GetDevice() resourceapi.Device {
	attributes := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
		"type": {
			StringValue: ptr.To(AmdPartitionDeviceType),
		},
		"uuid": {
			StringValue: ptr.To(d.GenerateUUID()),
		},
		"parentPciAddr": {
			StringValue: ptr.To(d.Parent.PCIAddress),
		},
		"cardIndex": {
			IntValue: ptr.To(int64(d.CardIndex)),
		},
		"renderIndex": {
			IntValue: ptr.To(int64(d.RenderIndex)),
		},
		"parentDeviceID": {
			StringValue: ptr.To(d.Parent.DeviceID),
		},
		"family": {
			StringValue: ptr.To(d.Parent.Family),
		},
		"productName": {
			StringValue: ptr.To(d.Parent.ProductName),
		},
		"driverVersion": {
			VersionValue: ptr.To(d.Parent.DriverVersion),
		},
		"driverSrcVersion": {
			StringValue: ptr.To(d.Parent.DriverSrcVersion),
		},
		"partitionProfile": {
			StringValue: ptr.To(d.PartitionProfile),
		},
	}

	// Add PCIe root attribute if available (inherited from parent)
	if d.Parent.pcieRootAttr.Name != "" {
		attributes[d.Parent.pcieRootAttr.Name] = d.Parent.pcieRootAttr.Value
	}

	return resourceapi.Device{
		Name:       d.CanonicalName(),
		Attributes: attributes,
		Capacity: map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
			"memory": {
				Value: *resource.NewQuantity(int64(d.MemoryBytes), resource.BinarySI),
			},
			"computeUnits": {
				Value: *resource.NewQuantity(int64(d.ComputeUnits), resource.BinarySI),
			},
			"simdUnits": {
				Value: *resource.NewQuantity(int64(d.SimdUnits), resource.BinarySI),
			},
		},
	}
}
