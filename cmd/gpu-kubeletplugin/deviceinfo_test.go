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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/consts"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// A memory capacity of 0 is the unreadable-VRAM sentinel. Both device types must
// publish the key with a zero value, so positive memory selectors fail closed
// rather than erroring on an absent field. This locks the ResourceSlice the
// scheduler actually sees, not just the getMemoryBytes helper.
func TestZeroMemoryCapacityIsPublished(t *testing.T) {
	fullGPU := (&AmdGpuInfo{
		MemoryBytes: 0,
	}).GetDevice()

	partition := (&AmdPartitionInfo{
		Parent:      &AmdGpuInfo{},
		MemoryBytes: 0,
	}).GetDevice()

	for name, device := range map[string]resourceapi.Device{
		"full GPU":  fullGPU,
		"partition": partition,
	} {
		t.Run(name, func(t *testing.T) {
			memory, ok := device.Capacity["memory"]
			require.True(t, ok)
			require.True(t, memory.Value.IsZero())
		})
	}
}

// ---- Tests for parseSyntheticPartitionDeviceName ----

func TestParseSyntheticPartitionDeviceName(t *testing.T) {
	tests := []struct {
		name        string
		deviceName  string
		wantIndex   int
		wantCompute string
		wantMemory  string
		wantErr     bool
	}{
		{
			name:        "spx-nps1",
			deviceName:  "gpu-0-spx-nps1",
			wantIndex:   0,
			wantCompute: consts.ComputePartitionSPX,
			wantMemory:  consts.MemoryPartitionNPS1,
		},
		{
			name:        "dpx-nps2",
			deviceName:  "gpu-1-dpx-nps2",
			wantIndex:   1,
			wantCompute: consts.ComputePartitionDPX,
			wantMemory:  consts.MemoryPartitionNPS2,
		},
		{
			name:        "cpx-nps1",
			deviceName:  "gpu-2-cpx-nps1",
			wantIndex:   2,
			wantCompute: consts.ComputePartitionCPX,
			wantMemory:  consts.MemoryPartitionNPS1,
		},
		{
			name:        "cpx-nps4",
			deviceName:  "gpu-0-cpx-nps4",
			wantIndex:   0,
			wantCompute: consts.ComputePartitionCPX,
			wantMemory:  consts.MemoryPartitionNPS4,
		},
		{
			name:        "high gpu index",
			deviceName:  "gpu-7-cpx-nps4",
			wantIndex:   7,
			wantCompute: consts.ComputePartitionCPX,
			wantMemory:  consts.MemoryPartitionNPS4,
		},
		{
			name:       "invalid prefix",
			deviceName: "not-a-gpu-0-cpx-nps4",
			wantErr:    true,
		},
		{
			name:        "dpx-nps1",
			deviceName:  "gpu-0-dpx-nps1",
			wantIndex:   0,
			wantCompute: consts.ComputePartitionDPX,
			wantMemory:  consts.MemoryPartitionNPS1,
		},
		{
			name:        "qpx-nps1",
			deviceName:  "gpu-0-qpx-nps1",
			wantIndex:   0,
			wantCompute: consts.ComputePartitionQPX,
			wantMemory:  consts.MemoryPartitionNPS1,
		},
		{
			name:       "unrecognized partition combination",
			deviceName: "gpu-0-dpx-nps4",
			wantErr:    true,
		},
		{
			name:       "missing memory mode",
			deviceName: "gpu-0-cpx",
			wantErr:    true,
		},
		{
			name:       "empty string",
			deviceName: "",
			wantErr:    true,
		},
		{
			name:       "real GPU name (not synthetic)",
			deviceName: "gpu-0-128",
			wantErr:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gpuIndex, compute, memory, err := parseSyntheticPartitionDeviceName(tc.deviceName)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error for %q, got nil", tc.deviceName)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.deviceName, err)
			}
			if gpuIndex != tc.wantIndex {
				t.Errorf("gpuIndex: expected %d, got %d", tc.wantIndex, gpuIndex)
			}
			if compute != tc.wantCompute {
				t.Errorf("compute: expected %q, got %q", tc.wantCompute, compute)
			}
			if memory != tc.wantMemory {
				t.Errorf("memory: expected %q, got %q", tc.wantMemory, memory)
			}
		})
	}
}

// ---- Tests for SyntheticPartitionDevice.CanonicalName ----

func TestSyntheticPartitionDevice_CanonicalName(t *testing.T) {
	tests := []struct {
		name     string
		device   SyntheticPartitionDevice
		wantName string
	}{
		{
			name: "gpu-0-spx-nps1",
			device: SyntheticPartitionDevice{
				GPUIndex:         0,
				ComputePartition: consts.ComputePartitionSPX,
				MemoryPartition:  consts.MemoryPartitionNPS1,
			},
			wantName: "gpu-0-spx-nps1",
		},
		{
			name: "gpu-1-cpx-nps4",
			device: SyntheticPartitionDevice{
				GPUIndex:         1,
				ComputePartition: consts.ComputePartitionCPX,
				MemoryPartition:  consts.MemoryPartitionNPS4,
			},
			wantName: "gpu-1-cpx-nps4",
		},
		{
			name: "gpu-3-dpx-nps2",
			device: SyntheticPartitionDevice{
				GPUIndex:         3,
				ComputePartition: consts.ComputePartitionDPX,
				MemoryPartition:  consts.MemoryPartitionNPS2,
			},
			wantName: "gpu-3-dpx-nps2",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.device.CanonicalName()
			if got != tc.wantName {
				t.Errorf("expected %q, got %q", tc.wantName, got)
			}
		})
	}
}

// ---- Tests for SyntheticPartitionDevice.GetDevice ----

func TestSyntheticPartitionDevice_GetDevice_SPX_TypeIsAmdGpu(t *testing.T) {
	device := &SyntheticPartitionDevice{
		GPUIndex:         0,
		ComputePartition: consts.ComputePartitionSPX,
		MemoryPartition:  consts.MemoryPartitionNPS1,
		PartitionCount:   1,
		PCIAddress:       "0000:19:00.0",
		ProductName:      "AMD_Instinct_MI300X",
		DeviceID:         "0x740f",
		DriverVersion:    "6.8.5",
		MemoryBytes:      80 * 1024 * 1024 * 1024, // 80 GiB
		ComputeUnits:     304,
		SimdUnits:        1216,
		NumaNode:         0,
	}

	d := device.GetDevice()

	// SPX should get type "amdgpu"
	typeAttr, ok := d.Attributes["type"]
	if !ok {
		t.Fatal("expected 'type' attribute, not found")
	}
	if typeAttr.StringValue == nil || *typeAttr.StringValue != consts.AmdGpuDeviceType {
		t.Errorf("SPX device should have type=%q, got %v", consts.AmdGpuDeviceType, typeAttr.StringValue)
	}

	// Name should be canonical
	if d.Name != "gpu-0-spx-nps1" {
		t.Errorf("expected name %q, got %q", "gpu-0-spx-nps1", d.Name)
	}
}

func TestSyntheticPartitionDevice_GetDevice_CPX_TypeIsAmdGpuPartition(t *testing.T) {
	device := &SyntheticPartitionDevice{
		GPUIndex:         0,
		ComputePartition: consts.ComputePartitionCPX,
		MemoryPartition:  consts.MemoryPartitionNPS4,
		PartitionCount:   8,
		PCIAddress:       "0000:19:00.0",
		ProductName:      "AMD_Instinct_MI300X",
		DeviceID:         "0x740f",
		DriverVersion:    "6.8.5",
		MemoryBytes:      10 * 1024 * 1024 * 1024, // 10 GiB per partition
		ComputeUnits:     38,
		SimdUnits:        152,
		NumaNode:         0,
	}

	d := device.GetDevice()

	// CPX should get type "amdgpu-partition"
	typeAttr, ok := d.Attributes["type"]
	if !ok {
		t.Fatal("expected 'type' attribute, not found")
	}
	if typeAttr.StringValue == nil || *typeAttr.StringValue != consts.AmdPartitionDeviceType {
		t.Errorf("CPX device should have type=%q, got %v", consts.AmdPartitionDeviceType, typeAttr.StringValue)
	}
}

func TestSyntheticPartitionDevice_GetDevice_DPX_TypeIsAmdGpuPartition(t *testing.T) {
	device := &SyntheticPartitionDevice{
		GPUIndex:         0,
		ComputePartition: consts.ComputePartitionDPX,
		MemoryPartition:  consts.MemoryPartitionNPS2,
		PartitionCount:   2,
		PCIAddress:       "0000:19:00.0",
	}

	d := device.GetDevice()

	typeAttr, ok := d.Attributes["type"]
	if !ok {
		t.Fatal("expected 'type' attribute, not found")
	}
	if typeAttr.StringValue == nil || *typeAttr.StringValue != consts.AmdPartitionDeviceType {
		t.Errorf("DPX device should have type=%q, got %v", consts.AmdPartitionDeviceType, typeAttr.StringValue)
	}
}

func TestSyntheticPartitionDevice_GetDevice_CorrectAttributes(t *testing.T) {
	const (
		gpuIndex         = 1
		computePartition = consts.ComputePartitionCPX
		memoryPartition  = consts.MemoryPartitionNPS4
		partitionCount   = 8
		pciAddr          = "0000:20:00.0"
		productName      = "AMD_Instinct_MI300X"
		deviceID         = "0x740f"
		driverVersion    = "6.8.5"
		memoryBytes      = uint64(10 * 1024 * 1024 * 1024)
		computeUnits     = 38
		simdUnits        = 152
		numaNode         = 1
	)

	device := &SyntheticPartitionDevice{
		GPUIndex:         gpuIndex,
		ComputePartition: computePartition,
		MemoryPartition:  memoryPartition,
		PartitionCount:   partitionCount,
		PCIAddress:       pciAddr,
		ProductName:      productName,
		DeviceID:         deviceID,
		DriverVersion:    driverVersion,
		MemoryBytes:      memoryBytes,
		ComputeUnits:     computeUnits,
		SimdUnits:        simdUnits,
		NumaNode:         numaNode,
	}

	d := device.GetDevice()

	checkStringAttr := func(attrName string, expected string) {
		t.Helper()
		attr, ok := d.Attributes[resourceapi.QualifiedName(attrName)]
		if !ok {
			t.Errorf("expected attribute %q not found", attrName)
			return
		}
		if attr.StringValue == nil || *attr.StringValue != expected {
			t.Errorf("attribute %q: expected %q, got %v", attrName, expected, attr.StringValue)
		}
	}
	checkIntAttr := func(attrName string, expected int64) {
		t.Helper()
		attr, ok := d.Attributes[resourceapi.QualifiedName(attrName)]
		if !ok {
			t.Errorf("expected attribute %q not found", attrName)
			return
		}
		if attr.IntValue == nil || *attr.IntValue != expected {
			t.Errorf("attribute %q: expected %d, got %v", attrName, expected, attr.IntValue)
		}
	}

	checkStringAttr("computePartition", computePartition)
	checkStringAttr("memoryPartition", memoryPartition)
	checkStringAttr("pciAddr", pciAddr)
	checkStringAttr("productName", productName)
	checkStringAttr("deviceID", deviceID)
	checkStringAttr("driverVersionFull", driverVersion)
	checkIntAttr("gpuIndex", int64(gpuIndex))
	checkIntAttr("numaNode", int64(numaNode))

	// Check version attribute separately
	versionAttr, ok := d.Attributes["driverVersion"]
	if !ok {
		t.Error("expected 'driverVersion' attribute")
	} else if versionAttr.VersionValue == nil || *versionAttr.VersionValue != driverVersion {
		t.Errorf("driverVersion: expected %q, got %v", driverVersion, versionAttr.VersionValue)
	}
}

func TestSyntheticPartitionDevice_GetDevice_CapacityAndPartitions(t *testing.T) {
	const (
		partitionCount = 8
		memoryBytes    = uint64(10 * 1024 * 1024 * 1024)
		computeUnits   = 38
		simdUnits      = 152
	)

	device := &SyntheticPartitionDevice{
		GPUIndex:         0,
		ComputePartition: consts.ComputePartitionCPX,
		MemoryPartition:  consts.MemoryPartitionNPS4,
		PartitionCount:   partitionCount,
		MemoryBytes:      memoryBytes,
		ComputeUnits:     computeUnits,
		SimdUnits:        simdUnits,
	}

	d := device.GetDevice()

	// Verify partitions capacity with RequestPolicy
	partCap, ok := d.Capacity["partitions"]
	if !ok {
		t.Fatal("expected 'partitions' capacity")
	}
	expectedPartitions := resource.NewQuantity(int64(partitionCount), resource.DecimalSI)
	if partCap.Value.Cmp(*expectedPartitions) != 0 {
		t.Errorf("partitions capacity: expected %v, got %v", expectedPartitions, partCap.Value)
	}
	if partCap.RequestPolicy == nil {
		t.Error("expected RequestPolicy on 'partitions' capacity")
	} else {
		defaultQ := resource.NewQuantity(1, resource.DecimalSI)
		if partCap.RequestPolicy.Default == nil || partCap.RequestPolicy.Default.Cmp(*defaultQ) != 0 {
			t.Errorf("RequestPolicy.Default: expected 1, got %v", partCap.RequestPolicy.Default)
		}
	}

	// Verify memory capacity: Value = total (per-partition * count), Default = per-partition
	memCap, ok := d.Capacity["memory"]
	if !ok {
		t.Fatal("expected 'memory' capacity")
	}
	expectedMemTotal := resource.NewQuantity(int64(memoryBytes)*int64(partitionCount), resource.BinarySI)
	if memCap.Value.Cmp(*expectedMemTotal) != 0 {
		t.Errorf("memory capacity Value: expected total %v, got %v", expectedMemTotal, memCap.Value)
	}
	if memCap.RequestPolicy == nil {
		t.Error("expected RequestPolicy on 'memory' capacity for partitionCount > 1")
	} else {
		expectedMemDefault := resource.NewQuantity(int64(memoryBytes), resource.BinarySI)
		if memCap.RequestPolicy.Default == nil || memCap.RequestPolicy.Default.Cmp(*expectedMemDefault) != 0 {
			t.Errorf("memory RequestPolicy.Default: expected per-partition %v, got %v", expectedMemDefault, memCap.RequestPolicy.Default)
		}
	}

	// Verify computeUnits capacity: Value = total (per-partition * count), Default = per-partition
	cuCap, ok := d.Capacity["computeUnits"]
	if !ok {
		t.Fatal("expected 'computeUnits' capacity")
	}
	expectedCUTotal := resource.NewQuantity(int64(computeUnits)*int64(partitionCount), resource.DecimalSI)
	if cuCap.Value.Cmp(*expectedCUTotal) != 0 {
		t.Errorf("computeUnits capacity Value: expected total %v, got %v", expectedCUTotal, cuCap.Value)
	}
	if cuCap.RequestPolicy == nil {
		t.Error("expected RequestPolicy on 'computeUnits' capacity for partitionCount > 1")
	} else {
		expectedCUDefault := resource.NewQuantity(int64(computeUnits), resource.DecimalSI)
		if cuCap.RequestPolicy.Default == nil || cuCap.RequestPolicy.Default.Cmp(*expectedCUDefault) != 0 {
			t.Errorf("computeUnits RequestPolicy.Default: expected per-partition %v, got %v", expectedCUDefault, cuCap.RequestPolicy.Default)
		}
	}

	// Verify simdUnits capacity: Value = total (per-partition * count), Default = per-partition
	simdCap, ok := d.Capacity["simdUnits"]
	if !ok {
		t.Fatal("expected 'simdUnits' capacity")
	}
	expectedSIMDTotal := resource.NewQuantity(int64(simdUnits)*int64(partitionCount), resource.DecimalSI)
	if simdCap.Value.Cmp(*expectedSIMDTotal) != 0 {
		t.Errorf("simdUnits capacity Value: expected total %v, got %v", expectedSIMDTotal, simdCap.Value)
	}
	if simdCap.RequestPolicy == nil {
		t.Error("expected RequestPolicy on 'simdUnits' capacity for partitionCount > 1")
	} else {
		expectedSIMDDefault := resource.NewQuantity(int64(simdUnits), resource.DecimalSI)
		if simdCap.RequestPolicy.Default == nil || simdCap.RequestPolicy.Default.Cmp(*expectedSIMDDefault) != 0 {
			t.Errorf("simdUnits RequestPolicy.Default: expected per-partition %v, got %v", expectedSIMDDefault, simdCap.RequestPolicy.Default)
		}
	}
}

func TestSyntheticPartitionDevice_GetDevice_AllowMultipleAllocations(t *testing.T) {
	tests := []struct {
		name           string
		partitionCount int
		wantMulti      bool
	}{
		{
			name:           "spx partition count 1 does not set AllowMultipleAllocations",
			partitionCount: 1,
			wantMulti:      false,
		},
		{
			name:           "dpx partition count 2 sets AllowMultipleAllocations",
			partitionCount: 2,
			wantMulti:      true,
		},
		{
			name:           "qpx partition count 4 sets AllowMultipleAllocations",
			partitionCount: 4,
			wantMulti:      true,
		},
		{
			name:           "cpx partition count 8 sets AllowMultipleAllocations",
			partitionCount: 8,
			wantMulti:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			device := &SyntheticPartitionDevice{
				GPUIndex:         0,
				ComputePartition: consts.ComputePartitionCPX,
				MemoryPartition:  consts.MemoryPartitionNPS4,
				PartitionCount:   tc.partitionCount,
			}
			d := device.GetDevice()
			if tc.wantMulti {
				if d.AllowMultipleAllocations == nil || !*d.AllowMultipleAllocations {
					t.Errorf("expected AllowMultipleAllocations=true for partitionCount=%d", tc.partitionCount)
				}
			} else {
				if d.AllowMultipleAllocations != nil && *d.AllowMultipleAllocations {
					t.Errorf("expected AllowMultipleAllocations=nil/false for partitionCount=%d", tc.partitionCount)
				}
			}
		})
	}
}

func TestSyntheticPartitionDevice_GetDevice_SPX_NoRequestPolicyOnCapacity(t *testing.T) {
	// For SPX (partitionCount=1), memory/computeUnits/simdUnits must NOT have RequestPolicy set.
	device := &SyntheticPartitionDevice{
		GPUIndex:         0,
		ComputePartition: consts.ComputePartitionSPX,
		MemoryPartition:  consts.MemoryPartitionNPS1,
		PartitionCount:   1,
		MemoryBytes:      80 * 1024 * 1024 * 1024,
		ComputeUnits:     304,
		SimdUnits:        1216,
	}

	d := device.GetDevice()

	// partitions capacity should have no RequestPolicy for SPX (count=1)
	partCap, ok := d.Capacity["partitions"]
	if !ok {
		t.Fatal("expected 'partitions' capacity")
	}
	if partCap.RequestPolicy != nil {
		t.Errorf("SPX: expected no RequestPolicy on 'partitions' capacity, got %v", partCap.RequestPolicy)
	}

	// memory should have no RequestPolicy for SPX
	memCap, ok := d.Capacity["memory"]
	if !ok {
		t.Fatal("expected 'memory' capacity")
	}
	if memCap.RequestPolicy != nil {
		t.Errorf("SPX: expected no RequestPolicy on 'memory' capacity, got %v", memCap.RequestPolicy)
	}

	// computeUnits should have no RequestPolicy for SPX
	cuCap, ok := d.Capacity["computeUnits"]
	if !ok {
		t.Fatal("expected 'computeUnits' capacity")
	}
	if cuCap.RequestPolicy != nil {
		t.Errorf("SPX: expected no RequestPolicy on 'computeUnits' capacity, got %v", cuCap.RequestPolicy)
	}

	// simdUnits should have no RequestPolicy for SPX
	simdCap, ok := d.Capacity["simdUnits"]
	if !ok {
		t.Fatal("expected 'simdUnits' capacity")
	}
	if simdCap.RequestPolicy != nil {
		t.Errorf("SPX: expected no RequestPolicy on 'simdUnits' capacity, got %v", simdCap.RequestPolicy)
	}
}

func TestSyntheticPartitionDevice_GetDevice_QPX_TypeIsPartition(t *testing.T) {
	device := &SyntheticPartitionDevice{
		GPUIndex:         0,
		ComputePartition: consts.ComputePartitionQPX,
		MemoryPartition:  consts.MemoryPartitionNPS1,
		PartitionCount:   4,
		PCIAddress:       "0000:19:00.0",
	}

	d := device.GetDevice()

	typeAttr, ok := d.Attributes["type"]
	if !ok {
		t.Fatal("expected 'type' attribute, not found")
	}
	if typeAttr.StringValue == nil || *typeAttr.StringValue != consts.AmdPartitionDeviceType {
		t.Errorf("QPX device should have type=%q, got %v", consts.AmdPartitionDeviceType, typeAttr.StringValue)
	}
}

func TestSyntheticPartitionDevice_GetDevice_ConsumesCounters(t *testing.T) {
	const gpuIndex = 3
	device := &SyntheticPartitionDevice{
		GPUIndex:         gpuIndex,
		ComputePartition: consts.ComputePartitionCPX,
		MemoryPartition:  consts.MemoryPartitionNPS4,
		PartitionCount:   8,
	}

	d := device.GetDevice()

	if len(d.ConsumesCounters) == 0 {
		t.Fatal("expected ConsumesCounters to be set")
	}

	cc := d.ConsumesCounters[0]
	expectedCounterSet := "gpu-3-mutex"
	if cc.CounterSet != expectedCounterSet {
		t.Errorf("expected CounterSet=%q, got %q", expectedCounterSet, cc.CounterSet)
	}

	counterVal, ok := cc.Counters["partition-mode"]
	if !ok {
		t.Fatal("expected 'partition-mode' counter")
	}
	expectedVal := resource.NewQuantity(1, resource.DecimalSI)
	if counterVal.Value.Cmp(*expectedVal) != 0 {
		t.Errorf("partition-mode counter: expected 1, got %v", counterVal.Value)
	}
}

func TestSyntheticPartitionDevice_GetDevice_TaintsApplied(t *testing.T) {
	taint := resourceapi.DeviceTaint{
		Key:    consts.MemoryPartitionTaintKey,
		Value:  consts.MemoryPartitionNPS4,
		Effect: resourceapi.DeviceTaintEffectNoExecute,
	}

	device := &SyntheticPartitionDevice{
		GPUIndex:         0,
		ComputePartition: consts.ComputePartitionCPX,
		MemoryPartition:  consts.MemoryPartitionNPS1,
		PartitionCount:   8,
		Taints:           []resourceapi.DeviceTaint{taint},
	}

	d := device.GetDevice()

	if len(d.Taints) == 0 {
		t.Fatal("expected taints to be propagated to device")
	}
	if d.Taints[0].Key != taint.Key {
		t.Errorf("taint key: expected %q, got %q", taint.Key, d.Taints[0].Key)
	}
	if d.Taints[0].Effect != taint.Effect {
		t.Errorf("taint effect: expected %v, got %v", taint.Effect, d.Taints[0].Effect)
	}
}

func TestSyntheticPartitionDevice_GetDevice_NoTaintsWhenEmpty(t *testing.T) {
	device := &SyntheticPartitionDevice{
		GPUIndex:         0,
		ComputePartition: consts.ComputePartitionSPX,
		MemoryPartition:  consts.MemoryPartitionNPS1,
		PartitionCount:   1,
		Taints:           nil,
	}

	d := device.GetDevice()
	if len(d.Taints) != 0 {
		t.Errorf("expected no taints for device with nil Taints, got %v", d.Taints)
	}
}

// ---- Tests for AmdGpuInfo.GetDevice (type attribute) ----

func TestAmdGpuInfo_GetDevice_TypeAttribute(t *testing.T) {
	gpu := &AmdGpuInfo{
		UUID:             "test-uuid",
		cardIndex:        0,
		renderIndex:      128,
		PCIAddress:       "0000:19:00.0",
		ProductName:      "AMD_Instinct_MI300X",
		DeviceID:         "0x740f",
		DriverVersion:    "6.8.5",
		PartitionProfile: consts.DefaultPartitionProfile,
		MemoryBytes:      80 * 1024 * 1024 * 1024,
		ComputeUnits:     304,
		SimdUnits:        1216,
		NumaNode:         0,
	}

	d := gpu.GetDevice()

	typeAttr, ok := d.Attributes["type"]
	if !ok {
		t.Fatal("expected 'type' attribute, not found")
	}
	if typeAttr.StringValue == nil || *typeAttr.StringValue != consts.AmdGpuDeviceType {
		t.Errorf("full GPU should have type=%q, got %v", consts.AmdGpuDeviceType, typeAttr.StringValue)
	}
}

// ---- Tests for AmdPartitionInfo.GetDevice (type attribute) ----

func TestAmdPartitionInfo_GetDevice_TypeAttribute(t *testing.T) {
	parent := &AmdGpuInfo{
		PCIAddress:    "0000:19:00.0",
		ProductName:   "AMD_Instinct_MI300X",
		DeviceID:      "0x740f",
		DriverVersion: "6.8.5",
	}
	partition := &AmdPartitionInfo{
		Parent:           parent,
		cardIndex:        1,
		renderIndex:      129,
		PartitionProfile: "cpx_nps4",
		MemoryBytes:      10 * 1024 * 1024 * 1024,
		ComputeUnits:     38,
		SimdUnits:        152,
		NumaNode:         0,
	}

	d := partition.GetDevice()

	typeAttr, ok := d.Attributes["type"]
	if !ok {
		t.Fatal("expected 'type' attribute, not found")
	}
	if typeAttr.StringValue == nil || *typeAttr.StringValue != consts.AmdPartitionDeviceType {
		t.Errorf("partition should have type=%q, got %v", consts.AmdPartitionDeviceType, typeAttr.StringValue)
	}
}

// ---- Tests for compatibility matrix (ValidPartitionConfigs) ----

func TestValidPartitionConfigs_AllHavePositivePartitionCount(t *testing.T) {
	for _, cfg := range consts.ValidPartitionConfigs {
		if cfg.PartitionCount <= 0 {
			t.Errorf("config {%s, %s} has non-positive PartitionCount=%d",
				cfg.Compute, cfg.Memory, cfg.PartitionCount)
		}
	}
}

func TestValidPartitionConfigs_PartitionCounts(t *testing.T) {
	expectedCounts := map[string]int{
		consts.ComputePartitionSPX: 1,
		consts.ComputePartitionDPX: 2,
		consts.ComputePartitionQPX: 4,
		consts.ComputePartitionCPX: 8,
	}

	for _, cfg := range consts.ValidPartitionConfigs {
		expectedCount, ok := expectedCounts[cfg.Compute]
		if !ok {
			t.Errorf("unexpected compute partition mode %q", cfg.Compute)
			continue
		}
		if cfg.PartitionCount != expectedCount {
			t.Errorf("config {%s, %s}: expected PartitionCount=%d, got %d",
				cfg.Compute, cfg.Memory, expectedCount, cfg.PartitionCount)
		}
	}
}

func TestValidPartitionConfigs_NoDuplicates(t *testing.T) {
	seen := make(map[string]bool)
	for _, cfg := range consts.ValidPartitionConfigs {
		key := cfg.Compute + "+" + cfg.Memory
		if seen[key] {
			t.Errorf("duplicate partition config: {%s, %s}", cfg.Compute, cfg.Memory)
		}
		seen[key] = true
	}
}

func TestValidPartitionConfigs_KnownCombinations(t *testing.T) {
	// Verify that only the documented valid combinations are present.
	// The compatibility matrix should include: spx+nps1, dpx+nps1, dpx+nps2, qpx+nps1, cpx+nps1, cpx+nps4
	type combo struct {
		compute string
		memory  string
	}
	expected := map[combo]bool{
		{consts.ComputePartitionSPX, consts.MemoryPartitionNPS1}: true,
		{consts.ComputePartitionDPX, consts.MemoryPartitionNPS1}: true,
		{consts.ComputePartitionDPX, consts.MemoryPartitionNPS2}: true,
		{consts.ComputePartitionQPX, consts.MemoryPartitionNPS1}: true,
		{consts.ComputePartitionCPX, consts.MemoryPartitionNPS1}: true,
		{consts.ComputePartitionCPX, consts.MemoryPartitionNPS4}: true,
	}

	for _, cfg := range consts.ValidPartitionConfigs {
		k := combo{cfg.Compute, cfg.Memory}
		if !expected[k] {
			t.Errorf("unexpected partition config in ValidPartitionConfigs: {%s, %s}", cfg.Compute, cfg.Memory)
		}
	}

	if len(consts.ValidPartitionConfigs) != len(expected) {
		t.Errorf("expected %d valid partition configs, got %d",
			len(expected), len(consts.ValidPartitionConfigs))
	}
}

// TestValidPartitionConfigs_CanonicalNamesAreUnique verifies that all SyntheticPartitionDevices
// generated from ValidPartitionConfigs for a given GPU index have unique canonical names.
func TestValidPartitionConfigs_CanonicalNamesAreUnique(t *testing.T) {
	seen := make(map[string]bool)
	for _, cfg := range consts.ValidPartitionConfigs {
		sp := &SyntheticPartitionDevice{
			GPUIndex:         0,
			ComputePartition: cfg.Compute,
			MemoryPartition:  cfg.Memory,
		}
		name := sp.CanonicalName()
		if seen[name] {
			t.Errorf("duplicate canonical name %q for config {%s, %s}", name, cfg.Compute, cfg.Memory)
		}
		seen[name] = true
	}
}

// TestValidPartitionConfigs_ParseRoundTrip verifies that canonical names generated from
// ValidPartitionConfigs can be successfully parsed back by parseSyntheticPartitionDeviceName.
func TestValidPartitionConfigs_ParseRoundTrip(t *testing.T) {
	const gpuIndex = 2
	for _, cfg := range consts.ValidPartitionConfigs {
		sp := &SyntheticPartitionDevice{
			GPUIndex:         gpuIndex,
			ComputePartition: cfg.Compute,
			MemoryPartition:  cfg.Memory,
		}
		name := sp.CanonicalName()

		parsedIndex, parsedCompute, parsedMemory, err := parseSyntheticPartitionDeviceName(name)
		if err != nil {
			t.Errorf("failed to parse canonical name %q: %v", name, err)
			continue
		}
		if parsedIndex != gpuIndex {
			t.Errorf("name %q: expected gpuIndex=%d, got %d", name, gpuIndex, parsedIndex)
		}
		if parsedCompute != cfg.Compute {
			t.Errorf("name %q: expected compute=%q, got %q", name, cfg.Compute, parsedCompute)
		}
		if parsedMemory != cfg.Memory {
			t.Errorf("name %q: expected memory=%q, got %q", name, cfg.Memory, parsedMemory)
		}
	}
}

// ---- Tests for mutexCounterSetName and buildMutexCounterSet ----

func TestMutexCounterSetName(t *testing.T) {
	tests := []struct {
		gpuIndex int
		expected string
	}{
		{0, "gpu-0-mutex"},
		{1, "gpu-1-mutex"},
		{7, "gpu-7-mutex"},
	}

	for _, tc := range tests {
		t.Run(tc.expected, func(t *testing.T) {
			got := mutexCounterSetName(tc.gpuIndex)
			if got != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, got)
			}
		})
	}
}

func TestBuildMutexCounterSet(t *testing.T) {
	cs := buildMutexCounterSet(2)

	if cs.Name != "gpu-2-mutex" {
		t.Errorf("expected name %q, got %q", "gpu-2-mutex", cs.Name)
	}
	counter, ok := cs.Counters["partition-mode"]
	if !ok {
		t.Fatal("expected 'partition-mode' counter in CounterSet")
	}
	expectedVal := resource.NewQuantity(1, resource.DecimalSI)
	if counter.Value.Cmp(*expectedVal) != 0 {
		t.Errorf("partition-mode counter value: expected 1, got %v", counter.Value)
	}
}

// ---- Tests for IsCompatibleMemoryMode ----

func TestIsCompatibleMemoryMode(t *testing.T) {
	tests := []struct {
		name      string
		active    string
		requested string
		want      bool
	}{
		{
			name:      "no active mode is always compatible",
			active:    "",
			requested: consts.MemoryPartitionNPS4,
			want:      true,
		},
		{
			name:      "matching modes are compatible",
			active:    consts.MemoryPartitionNPS4,
			requested: consts.MemoryPartitionNPS4,
			want:      true,
		},
		{
			name:      "different modes are incompatible",
			active:    consts.MemoryPartitionNPS4,
			requested: consts.MemoryPartitionNPS1,
			want:      false,
		},
		{
			name:      "nps2 vs nps1 is incompatible",
			active:    consts.MemoryPartitionNPS2,
			requested: consts.MemoryPartitionNPS1,
			want:      false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsCompatibleMemoryMode(tc.active, tc.requested)
			if got != tc.want {
				t.Errorf("IsCompatibleMemoryMode(%q, %q) = %v, want %v",
					tc.active, tc.requested, got, tc.want)
			}
		})
	}
}

// ---- Tests for enumerateAllPossibleDevices (synthetic partition virtual device count) ----

// TestEnumerateSyntheticPartitions_VirtualDeviceCount verifies that for each partitionable
// GPU, exactly len(consts.ValidPartitionConfigs) virtual devices are generated.
// This test uses the allocatable map directly rather than calling the real function
// (which depends on CGo amdgpu.GetAMDGPUs), by constructing synthetic devices directly.
func TestEnumerateSyntheticPartitions_VirtualDeviceCount(t *testing.T) {
	// Simulate what enumerateAllPossibleDevices does for 2 MI300X GPUs
	const numGPUs = 2
	totalMemoryPerGPU := uint64(192 * 1024 * 1024 * 1024)
	const computeUnitsPerGPU = 304
	const simdUnitsPerGPU = 1216

	allocatable := make(AllocatableDevices)

	for gpuIndex := 0; gpuIndex < numGPUs; gpuIndex++ {
		for _, cfg := range consts.ValidPartitionConfigs {
			sp := &SyntheticPartitionDevice{
				GPUIndex:         gpuIndex,
				ComputePartition: cfg.Compute,
				MemoryPartition:  cfg.Memory,
				PartitionCount:   cfg.PartitionCount,
				PCIAddress:       "0000:19:00.0",
				ProductName:      "AMD_Instinct_MI300X",
				DeviceID:         "0x740f",
				DriverVersion:    "6.8.5",
				MemoryBytes:      totalMemoryPerGPU / uint64(cfg.PartitionCount),
				ComputeUnits:     computeUnitsPerGPU / cfg.PartitionCount,
				SimdUnits:        simdUnitsPerGPU / cfg.PartitionCount,
				NumaNode:         0,
			}
			ad := &AllocatableDevice{SyntheticPartition: sp}
			allocatable[ad.CanonicalName()] = ad
		}
	}

	// Total devices = numGPUs * len(ValidPartitionConfigs)
	expectedTotal := numGPUs * len(consts.ValidPartitionConfigs)
	if len(allocatable) != expectedTotal {
		t.Errorf("expected %d virtual devices for %d GPUs, got %d", expectedTotal, numGPUs, len(allocatable))
	}

	// Verify memory and compute are properly divided per partition count
	for _, ad := range allocatable {
		sp := ad.SyntheticPartition
		if sp == nil {
			t.Errorf("expected SyntheticPartition device, got nil")
			continue
		}
		expectedMem := totalMemoryPerGPU / uint64(sp.PartitionCount)
		if sp.MemoryBytes != expectedMem {
			t.Errorf("device %s: expected MemoryBytes=%d (totalMem/count), got %d",
				sp.CanonicalName(), expectedMem, sp.MemoryBytes)
		}
		expectedCU := computeUnitsPerGPU / sp.PartitionCount
		if sp.ComputeUnits != expectedCU {
			t.Errorf("device %s: expected ComputeUnits=%d, got %d",
				sp.CanonicalName(), expectedCU, sp.ComputeUnits)
		}
	}
}

// TestEnumerateSyntheticPartitions_DeviceNamesMatchCanonical verifies that all
// virtual device canonical names follow the "gpu-<index>-<compute>-<memory>" pattern
// and can be parsed back correctly.
func TestEnumerateSyntheticPartitions_DeviceNamesMatchCanonical(t *testing.T) {
	const gpuIndex = 0
	for _, cfg := range consts.ValidPartitionConfigs {
		sp := &SyntheticPartitionDevice{
			GPUIndex:         gpuIndex,
			ComputePartition: cfg.Compute,
			MemoryPartition:  cfg.Memory,
			PartitionCount:   cfg.PartitionCount,
		}
		name := sp.CanonicalName()

		// Verify the name stored in the allocatable map matches what is returned
		ad := &AllocatableDevice{SyntheticPartition: sp}
		if ad.CanonicalName() != name {
			t.Errorf("AllocatableDevice.CanonicalName() %q != SyntheticPartitionDevice.CanonicalName() %q",
				ad.CanonicalName(), name)
		}
	}
}
