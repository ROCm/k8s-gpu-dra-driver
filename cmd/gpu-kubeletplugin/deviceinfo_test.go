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
	"fmt"
	"testing"

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/consts"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
)

func TestPartitionMode(t *testing.T) {
	tests := []struct {
		numVFs   int
		expected string
	}{
		{1, "spx"},
		{2, "dpx"},
		{4, "qpx"},
		{8, "cpx"},
		{0, ""},
		{3, "tpx"},
		{16, ""},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("NumVFs=%d", tc.numVFs), func(t *testing.T) {
			d := &AmdGpuVFIOInfo{NumVFs: tc.numVFs}
			assert.Equal(t, tc.expected, d.partitionMode())
		})
	}
}

func TestGetSharedCounterSetName(t *testing.T) {
	tests := map[string]struct {
		totalVFs        int
		parentPFAddress string
		expected        string
	}{
		"PF with VFs": {
			totalVFs:        4,
			parentPFAddress: "0000:0a:00.0",
			expected:        "pf-0000-0a-00-0-counter-set",
		},
		"TotalVFs=0": {
			totalVFs:        0,
			parentPFAddress: "0000:0a:00.0",
			expected:        "",
		},
		"empty ParentPFAddress": {
			totalVFs:        4,
			parentPFAddress: "",
			expected:        "",
		},
		"both zero/empty": {
			totalVFs:        0,
			parentPFAddress: "",
			expected:        "",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			d := &AmdGpuVFIOInfo{TotalVFs: tc.totalVFs, ParentPFAddress: tc.parentPFAddress}
			assert.Equal(t, tc.expected, d.GetSharedCounterSetName())
		})
	}
}

func TestGetSharedCounterSet(t *testing.T) {
	t.Run("returns CounterSet when TotalVFs > 0", func(t *testing.T) {
		d := &AmdGpuVFIOInfo{TotalVFs: 8, ParentPFAddress: "0000:0a:00.0"}
		cs := d.GetSharedCounterSet()
		require.NotNil(t, cs)
		assert.Equal(t, "pf-0000-0a-00-0-counter-set", cs.Name)
		counter, ok := cs.Counters[VFSlotCounterName]
		require.True(t, ok)
		assert.Equal(t, *resource.NewQuantity(8, resource.BinarySI), counter.Value)
	})

	t.Run("returns nil when TotalVFs = 0", func(t *testing.T) {
		d := &AmdGpuVFIOInfo{TotalVFs: 0, ParentPFAddress: "0000:0a:00.0"}
		assert.Nil(t, d.GetSharedCounterSet())
	})
}

func TestGetConsumesCounters(t *testing.T) {
	t.Run("VF consumes 1 slot", func(t *testing.T) {
		d := &AmdGpuVFIOInfo{IsVF: true, TotalVFs: 8, ParentPFAddress: "0000:0a:00.0"}
		cc := d.GetConsumesCounters()
		require.Len(t, cc, 1)
		counter := cc[0].Counters[VFSlotCounterName]
		assert.Equal(t, *resource.NewQuantity(1, resource.BinarySI), counter.Value)
	})

	t.Run("PF consumes all slots", func(t *testing.T) {
		d := &AmdGpuVFIOInfo{IsVF: false, TotalVFs: 4, ParentPFAddress: "0000:0a:00.0"}
		cc := d.GetConsumesCounters()
		require.Len(t, cc, 1)
		counter := cc[0].Counters[VFSlotCounterName]
		assert.Equal(t, *resource.NewQuantity(4, resource.BinarySI), counter.Value)
	})

	t.Run("returns nil when TotalVFs = 0", func(t *testing.T) {
		d := &AmdGpuVFIOInfo{IsVF: false, TotalVFs: 0}
		assert.Nil(t, d.GetConsumesCounters())
	})
}

func TestVFIOGetDevice(t *testing.T) {
	t.Run("Attributes", func(t *testing.T) {
		d := &AmdGpuVFIOInfo{
			PCIAddress:  "0000:0b:00.0",
			DeviceID:    "0x740f",
			VendorID:    consts.AMDVendorID,
			IOMMUGroup:  "42",
			ProductName: "MI300X",
			NumaNode:    1,
			IsVF:        true,
			NumVFs:      4,
		}
		dev := d.GetDevice()
		assert.Equal(t, ptr.To(consts.VfioDeviceType), dev.Attributes["type"].StringValue)
		assert.Equal(t, ptr.To("42"), dev.Attributes["iommuGroup"].StringValue)
		assert.Equal(t, ptr.To("0000:0b:00.0"), dev.Attributes["pciAddr"].StringValue)
		assert.Equal(t, ptr.To(true), dev.Attributes["isVF"].BoolValue)
		assert.Equal(t, ptr.To("MI300X"), dev.Attributes["productName"].StringValue)
		assert.Equal(t, ptr.To(int64(1)), dev.Attributes["numaNode"].IntValue)
		assert.Equal(t, ptr.To("qpx"), dev.Attributes["partitionProfile"].StringValue)
	})

	t.Run("CapacityPresent", func(t *testing.T) {
		d := &AmdGpuVFIOInfo{
			MemoryBytes:  51539607552,
			ComputeUnits: 76,
			SimdUnits:    304,
		}
		dev := d.GetDevice()
		require.NotNil(t, dev.Capacity)
		assert.Equal(t, *resource.NewQuantity(51539607552, resource.BinarySI), dev.Capacity["memory"].Value)
		assert.Equal(t, *resource.NewQuantity(76, resource.BinarySI), dev.Capacity["computeUnits"].Value)
		assert.Equal(t, *resource.NewQuantity(304, resource.BinarySI), dev.Capacity["simdUnits"].Value)
	})

	t.Run("CapacityAbsent", func(t *testing.T) {
		d := &AmdGpuVFIOInfo{
			PCIAddress: "0000:0b:00.0",
			IOMMUGroup: "42",
		}
		dev := d.GetDevice()
		assert.Nil(t, dev.Capacity)
	})

	t.Run("ConsumesCounters", func(t *testing.T) {
		d := &AmdGpuVFIOInfo{
			IsVF:            true,
			TotalVFs:        8,
			ParentPFAddress: "0000:0a:00.0",
		}
		dev := d.GetDevice()
		require.Len(t, dev.ConsumesCounters, 1)
		assert.Equal(t, "pf-0000-0a-00-0-counter-set", dev.ConsumesCounters[0].CounterSet)
	})
}

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
