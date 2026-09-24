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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/resourceslice"
)

// canAllocateTogether applies the scheduler's KEP-4815 admission rule to a
// published pool: the named devices can be allocated at the same time only if,
// for every counter, their combined consumption fits the published capacity.
// It fails the test if a device references a counter set or counter that the
// pool does not publish, since the scheduler would reject that slice.
func canAllocateTogether(t *testing.T, pool resourceslice.Pool, names ...string) bool {
	t.Helper()
	capacity := map[string]map[string]int64{}
	devices := map[string]resourceapi.Device{}
	for _, sl := range pool.Slices {
		for _, cs := range sl.SharedCounters {
			capacity[cs.Name] = map[string]int64{}
			for name, c := range cs.Counters {
				capacity[cs.Name][name] = c.Value.Value()
			}
		}
		for _, d := range sl.Devices {
			devices[d.Name] = d
		}
	}
	used := map[string]map[string]int64{}
	for _, name := range names {
		d, ok := devices[name]
		require.True(t, ok, "device %s is not published", name)
		for _, c := range d.ConsumesCounters {
			set, ok := capacity[c.CounterSet]
			require.True(t, ok, "device %s consumes unpublished counter set %s", name, c.CounterSet)
			if used[c.CounterSet] == nil {
				used[c.CounterSet] = map[string]int64{}
			}
			for counter, v := range c.Counters {
				_, ok := set[counter]
				require.True(t, ok, "device %s consumes unpublished counter %s/%s", name, c.CounterSet, counter)
				used[c.CounterSet][counter] += v.Value.Value()
			}
		}
	}
	for setName, counters := range used {
		for counter, v := range counters {
			if v > capacity[setName][counter] {
				return false
			}
		}
	}
	return true
}

// dualEntryPool publishes the given devices after marking sibling pairs, as
// NewDeviceState does before the first publish.
func dualEntryPool(allocatable AllocatableDevices) resourceslice.Pool {
	markSiblingPairs(allocatable)
	d := &driver{state: &DeviceState{allocatable: allocatable}}
	return d.buildDriverResources("node").Pools["node"]
}

func TestDeviceCounters(t *testing.T) {
	const pf, vf = "0000:0a:00.0", "0000:0b:00.1"
	tests := map[string]struct {
		parentPF, pci string
		totalVFs      int
		isVF, sibling bool
		consumed      map[string]int64
		capacity      map[string]int64
	}{
		"standalone GPU without sibling consumes nothing": {pci: pf},
		"standalone GPU with sibling": {
			parentPF: pf, pci: pf, sibling: true,
			consumed: map[string]int64{"fn-0000-0a-00-0": 1},
			capacity: map[string]int64{"fn-0000-0a-00-0": 1},
		},
		"SR-IOV PF without sibling": {
			parentPF: pf, pci: pf, totalVFs: 8,
			consumed: map[string]int64{VFSlotCounterName: 8},
			capacity: map[string]int64{VFSlotCounterName: 8},
		},
		"SR-IOV PF with sibling": {
			parentPF: pf, pci: pf, totalVFs: 8, sibling: true,
			consumed: map[string]int64{VFSlotCounterName: 8, "fn-0000-0a-00-0": 1},
			capacity: map[string]int64{VFSlotCounterName: 8, "fn-0000-0a-00-0": 1},
		},
		"SR-IOV VF": {
			parentPF: pf, pci: vf, totalVFs: 8, isVF: true,
			consumed: map[string]int64{VFSlotCounterName: 1},
			capacity: map[string]int64{VFSlotCounterName: 8},
		},
		"sibling without known parent uses its own family": {
			pci: pf, sibling: true,
			consumed: map[string]int64{"fn-0000-0a-00-0": 1},
			capacity: map[string]int64{"fn-0000-0a-00-0": 1},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			consumed, capacity := deviceCounters(tc.parentPF, tc.pci, tc.totalVFs, tc.isVF, tc.sibling)
			if tc.consumed == nil {
				assert.Empty(t, consumed)
				assert.Empty(t, capacity)
				assert.Nil(t, consumesCounters(tc.parentPF, tc.pci, tc.totalVFs, tc.isVF, tc.sibling))
				return
			}
			assert.Equal(t, tc.consumed, consumed)
			assert.Equal(t, tc.capacity, capacity)
			cc := consumesCounters(tc.parentPF, tc.pci, tc.totalVFs, tc.isVF, tc.sibling)
			require.Len(t, cc, 1)
			assert.Equal(t, "pf-0000-0a-00-0-counter-set", cc[0].CounterSet)
		})
	}
}

func TestMarkSiblingPairs(t *testing.T) {
	pairCompute := &AmdGpuInfo{PCIAddress: "0000:0a:00.0", cardIndex: 0, renderIndex: 128}
	pairVFIO := &AmdGpuVFIOInfo{PCIAddress: "0000:0a:00.0", Index: 0}
	loneCompute := &AmdGpuInfo{PCIAddress: "0000:0c:00.0", cardIndex: 1, renderIndex: 129}
	loneVFIO := &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0", Index: 1}
	partition := &AmdPartitionInfo{Parent: &AmdGpuInfo{PCIAddress: "0000:0a:00.0"}, cardIndex: 2, renderIndex: 130}

	markSiblingPairs(AllocatableDevices{
		"gpu-0-128":  {AmdGpu: pairCompute},
		"gpu-vfio-0": {Vfio: pairVFIO},
		"gpu-1-129":  {AmdGpu: loneCompute},
		"gpu-vfio-1": {Vfio: loneVFIO},
		"gpu-2-130":  {AmdPartition: partition},
	})

	assert.True(t, pairCompute.siblingExclusive)
	assert.True(t, pairVFIO.siblingExclusive)
	assert.False(t, loneCompute.siblingExclusive, "a compute GPU without a VFIO entry has no sibling")
	assert.False(t, loneVFIO.siblingExclusive, "a pre-bound or GIM VFIO device has no sibling")
}

// TestSiblingExclusion_Counters checks, against the published slices, that the
// scheduler cannot allocate both entries of one PCI function, while unrelated
// allocations and the existing PF/VF rules still hold.
func TestSiblingExclusion_Counters(t *testing.T) {
	t.Run("GPU without SR-IOV", func(t *testing.T) {
		pool := dualEntryPool(AllocatableDevices{
			"gpu-0-128":  {AmdGpu: &AmdGpuInfo{PCIAddress: "0000:0a:00.0", ParentPFAddress: "0000:0a:00.0", cardIndex: 0, renderIndex: 128}},
			"gpu-vfio-0": {Vfio: &AmdGpuVFIOInfo{PCIAddress: "0000:0a:00.0", ParentPFAddress: "0000:0a:00.0", Index: 0}},
			"gpu-1-129":  {AmdGpu: &AmdGpuInfo{PCIAddress: "0000:0c:00.0", ParentPFAddress: "0000:0c:00.0", cardIndex: 1, renderIndex: 129}},
			"gpu-vfio-1": {Vfio: &AmdGpuVFIOInfo{PCIAddress: "0000:0c:00.0", ParentPFAddress: "0000:0c:00.0", Index: 1}},
		})
		assert.False(t, canAllocateTogether(t, pool, "gpu-0-128", "gpu-vfio-0"), "siblings must be mutually exclusive")
		assert.False(t, canAllocateTogether(t, pool, "gpu-1-129", "gpu-vfio-1"), "siblings must be mutually exclusive")
		assert.True(t, canAllocateTogether(t, pool, "gpu-0-128"))
		assert.True(t, canAllocateTogether(t, pool, "gpu-vfio-0"))
		assert.True(t, canAllocateTogether(t, pool, "gpu-0-128", "gpu-vfio-1"), "different GPUs are independent")
		assert.True(t, canAllocateTogether(t, pool, "gpu-vfio-0", "gpu-vfio-1"))
	})

	t.Run("SR-IOV PF with GIM VFs", func(t *testing.T) {
		const pf = "0000:0a:00.0"
		pool := dualEntryPool(AllocatableDevices{
			"gpu-0-128":  {AmdGpu: &AmdGpuInfo{PCIAddress: pf, ParentPFAddress: pf, TotalVFs: 4, cardIndex: 0, renderIndex: 128}},
			"gpu-vfio-0": {Vfio: &AmdGpuVFIOInfo{PCIAddress: pf, ParentPFAddress: pf, TotalVFs: 4, Index: 0}},
			"gpu-vfio-1": {Vfio: &AmdGpuVFIOInfo{PCIAddress: "0000:0b:00.1", ParentPFAddress: pf, TotalVFs: 4, IsVF: true, Index: 1}},
			"gpu-vfio-2": {Vfio: &AmdGpuVFIOInfo{PCIAddress: "0000:0b:00.2", ParentPFAddress: pf, TotalVFs: 4, IsVF: true, Index: 2}},
		})
		assert.False(t, canAllocateTogether(t, pool, "gpu-0-128", "gpu-vfio-0"), "PF siblings must be mutually exclusive")
		assert.False(t, canAllocateTogether(t, pool, "gpu-vfio-0", "gpu-vfio-1"), "PF and VF exclude each other")
		assert.False(t, canAllocateTogether(t, pool, "gpu-0-128", "gpu-vfio-1"), "PF and VF exclude each other")
		assert.True(t, canAllocateTogether(t, pool, "gpu-vfio-1", "gpu-vfio-2"), "VFs share the PF's slots")

		var sets []resourceapi.CounterSet
		for _, sl := range pool.Slices {
			sets = append(sets, sl.SharedCounters...)
		}
		require.Len(t, sets, 1, "the PF's slots and its sibling counter share one family set")
		assert.Len(t, sets[0].Counters, 2)
		assert.Contains(t, sets[0].Counters, VFSlotCounterName)
		assert.Contains(t, sets[0].Counters, "fn-0000-0a-00-0")
	})

	t.Run("GPU without a VFIO entry consumes nothing", func(t *testing.T) {
		pool := dualEntryPool(AllocatableDevices{
			"gpu-0-128": {AmdGpu: &AmdGpuInfo{PCIAddress: "0000:0a:00.0", ParentPFAddress: "0000:0a:00.0", cardIndex: 0, renderIndex: 128}},
		})
		require.Len(t, pool.Slices, 1, "no counter set slice")
		assert.Empty(t, pool.Slices[0].Devices[0].ConsumesCounters)
	})

	t.Run("converted GPU keeps the sibling counter", func(t *testing.T) {
		// Conversion (VfioDeviceConfig) copies siblingExclusive, so a republish
		// while a GPU is converted still references a published counter.
		gpu := &AmdGpuInfo{PCIAddress: "0000:0a:00.0", siblingExclusive: true}
		assert.Equal(t,
			consumesCounters(gpu.ParentPFAddress, gpu.PCIAddress, gpu.TotalVFs, gpu.IsVF, true),
			(&AmdGpuVFIOInfo{PCIAddress: gpu.PCIAddress, siblingExclusive: gpu.siblingExclusive}).GetConsumesCounters())
	})
}

// TestSyntheticPartitionResources_PublishFamilyCounterSets covers synthetic
// partition mode, where a non-partitionable GPU is advertised as a dual pair
// next to synthetic partition devices: the family counter sets its entries
// consume must be published alongside the partition mutex sets.
func TestSyntheticPartitionResources_PublishFamilyCounterSets(t *testing.T) {
	allocatable := AllocatableDevices{
		"gpu-0-128":  {AmdGpu: &AmdGpuInfo{PCIAddress: "0000:0a:00.0", ParentPFAddress: "0000:0a:00.0", cardIndex: 0, renderIndex: 128}},
		"gpu-vfio-0": {Vfio: &AmdGpuVFIOInfo{PCIAddress: "0000:0a:00.0", ParentPFAddress: "0000:0a:00.0", Index: 0}},
		"gpu-1-spx-nps1": {SyntheticPartition: &SyntheticPartitionDevice{
			GPUIndex: 1, ComputePartition: "spx", MemoryPartition: "nps1", Taints: []resourceapi.DeviceTaint{},
		}},
	}
	markSiblingPairs(allocatable)
	d := &driver{
		nodeName:          "node",
		partitionableGPUs: []int{1},
		state: &DeviceState{
			allocatable:    allocatable,
			partitionState: newTestPartitionState(map[int]string{1: "0000:0e:00.0"}, []int{1}, allocatable),
		},
	}
	pool := d.buildSyntheticPartitionResources().Pools["node"]

	var names []string
	for _, sl := range pool.Slices {
		for _, cs := range sl.SharedCounters {
			names = append(names, cs.Name)
		}
	}
	assert.ElementsMatch(t, []string{"gpu-1-mutex", "pf-0000-0a-00-0-counter-set"}, names)
	assert.False(t, canAllocateTogether(t, pool, "gpu-0-128", "gpu-vfio-0"))
	assert.True(t, canAllocateTogether(t, pool, "gpu-0-128", "gpu-1-spx-nps1"))
}
