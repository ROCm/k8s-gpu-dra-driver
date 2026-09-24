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
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/dynamic-resource-allocation/resourceslice"
)

func deviceNames(devices []resourceapi.Device) []string {
	names := make([]string, len(devices))
	for i, d := range devices {
		names[i] = d.Name
	}
	return names
}

func TestCollectCounterSets(t *testing.T) {
	t.Run("deduplicates VFs from same PF", func(t *testing.T) {
		d := &driver{state: &DeviceState{
			allocatable: AllocatableDevices{
				"gpu-vfio-0": {Vfio: &AmdGpuVFIOInfo{Index: 0, IsVF: true, TotalVFs: 4, ParentPFAddress: "0000:0a:00.0"}},
				"gpu-vfio-1": {Vfio: &AmdGpuVFIOInfo{Index: 1, IsVF: true, TotalVFs: 4, ParentPFAddress: "0000:0a:00.0"}},
				"gpu-0-128":  {AmdGpu: &AmdGpuInfo{cardIndex: 0, renderIndex: 128}},
			},
		}}
		sets := d.collectCounterSets()
		assert.Len(t, sets, 1)
		assert.Equal(t, "pf-0000-0a-00-0-counter-set", sets[0].Name)
	})

	t.Run("multiple PFs produce multiple sets sorted by address", func(t *testing.T) {
		d := &driver{state: &DeviceState{
			allocatable: AllocatableDevices{
				"gpu-vfio-0": {Vfio: &AmdGpuVFIOInfo{Index: 0, IsVF: true, TotalVFs: 4, ParentPFAddress: "0000:0b:00.0"}},
				"gpu-vfio-1": {Vfio: &AmdGpuVFIOInfo{Index: 1, IsVF: false, TotalVFs: 8, ParentPFAddress: "0000:0a:00.0"}},
			},
		}}
		sets := d.collectCounterSets()
		assert.Len(t, sets, 2)
		assert.Equal(t, "pf-0000-0a-00-0-counter-set", sets[0].Name)
		assert.Equal(t, "pf-0000-0b-00-0-counter-set", sets[1].Name)
	})

	t.Run("no VFIO devices returns empty", func(t *testing.T) {
		d := &driver{state: &DeviceState{
			allocatable: AllocatableDevices{
				"gpu-0-128": {AmdGpu: &AmdGpuInfo{cardIndex: 0, renderIndex: 128}},
			},
		}}
		sets := d.collectCounterSets()
		assert.Empty(t, sets)
	})

	t.Run("compute VFs publish the counter set when VFIO siblings are unavailable", func(t *testing.T) {
		d := &driver{state: &DeviceState{
			allocatable: AllocatableDevices{
				"gpu-0-128": {AmdGpu: &AmdGpuInfo{
					cardIndex:       0,
					renderIndex:     128,
					ParentPFAddress: "0000:0a:00.0",
					TotalVFs:        8,
					IsVF:            true,
				}},
			},
		}}
		sets := d.collectCounterSets()
		require.Len(t, sets, 1)
		assert.Equal(t, "pf-0000-0a-00-0-counter-set", sets[0].Name)
	})
}

func TestBuildDriverResourcesWithCounters(t *testing.T) {
	t.Run("compute VF publishes matching counter consumption", func(t *testing.T) {
		d := &driver{state: &DeviceState{
			allocatable: AllocatableDevices{
				"gpu-0-128": {AmdGpu: &AmdGpuInfo{
					cardIndex:       0,
					renderIndex:     128,
					ParentPFAddress: "0000:0a:00.0",
					TotalVFs:        8,
					IsVF:            true,
				}},
			},
		}}

		res := d.buildDriverResources("test-node")
		pool := res.Pools["test-node"]
		require.Len(t, pool.Slices, 2)
		require.Len(t, pool.Slices[0].SharedCounters, 1)
		require.Len(t, pool.Slices[1].Devices, 1)

		counterSet := pool.Slices[0].SharedCounters[0]
		assert.Equal(t, "pf-0000-0a-00-0-counter-set", counterSet.Name)
		assert.Equal(t, *resource.NewQuantity(8, resource.BinarySI), counterSet.Counters[VFSlotCounterName].Value)

		device := pool.Slices[1].Devices[0]
		require.Len(t, device.ConsumesCounters, 1)
		consumption := device.ConsumesCounters[0]
		assert.Equal(t, counterSet.Name, consumption.CounterSet)
		assert.Equal(t, *resource.NewQuantity(1, resource.BinarySI), consumption.Counters[VFSlotCounterName].Value)
	})

	t.Run("VFIO PF publishes matching full counter consumption", func(t *testing.T) {
		d := &driver{state: &DeviceState{
			allocatable: AllocatableDevices{
				"gpu-vfio-0": {Vfio: &AmdGpuVFIOInfo{
					Index:           0,
					IsVF:            false,
					TotalVFs:        4,
					ParentPFAddress: "0000:0a:00.0",
					IOMMUGroup:      "42",
					PCIAddress:      "0000:0a:00.0",
				}},
			},
		}}

		res := d.buildDriverResources("test-node")
		pool := res.Pools["test-node"]
		require.Len(t, pool.Slices, 2)
		require.Len(t, pool.Slices[0].SharedCounters, 1)
		require.Len(t, pool.Slices[1].Devices, 1)

		counterSet := pool.Slices[0].SharedCounters[0]
		device := pool.Slices[1].Devices[0]
		require.Len(t, device.ConsumesCounters, 1)
		assert.Equal(t, counterSet.Name, device.ConsumesCounters[0].CounterSet)
		assert.Equal(t, *resource.NewQuantity(4, resource.BinarySI), device.ConsumesCounters[0].Counters[VFSlotCounterName].Value)
	})

	t.Run("with counters has 2 slices", func(t *testing.T) {
		d := &driver{state: &DeviceState{
			allocatable: AllocatableDevices{
				"gpu-vfio-0": {Vfio: &AmdGpuVFIOInfo{Index: 0, IsVF: false, TotalVFs: 4, ParentPFAddress: "0000:0a:00.0", IOMMUGroup: "42", PCIAddress: "0000:0a:00.0"}},
			},
		}}
		res := d.buildDriverResources("test-node")
		pool := res.Pools["test-node"]
		assert.Len(t, pool.Slices, 2, "should have SharedCounters slice + Devices slice")
		assert.NotEmpty(t, pool.Slices[0].SharedCounters)
		assert.NotEmpty(t, pool.Slices[1].Devices)
	})

	t.Run("without counters has 1 slice", func(t *testing.T) {
		d := &driver{state: &DeviceState{
			allocatable: AllocatableDevices{
				"gpu-0-128": {AmdGpu: &AmdGpuInfo{cardIndex: 0, renderIndex: 128}},
			},
		}}
		res := d.buildDriverResources("test-node")
		pool := res.Pools["test-node"]
		assert.Len(t, pool.Slices, 1, "should have only Devices slice")
		assert.NotEmpty(t, pool.Slices[0].Devices)
	})
}

func TestResourceSliceDevicesAreSortedByName(t *testing.T) {
	allocatable := AllocatableDevices{
		"gpu-9-136":  {AmdGpu: &AmdGpuInfo{cardIndex: 9, renderIndex: 136}},
		"gpu-1-128":  {AmdGpu: &AmdGpuInfo{cardIndex: 1, renderIndex: 128}},
		"gpu-17-144": {AmdGpu: &AmdGpuInfo{cardIndex: 17, renderIndex: 144}},
		"gpu-3-130":  {AmdGpu: &AmdGpuInfo{cardIndex: 3, renderIndex: 130}},
		"gpu-11-138": {AmdGpu: &AmdGpuInfo{cardIndex: 11, renderIndex: 138}},
	}

	// The published Device.Name values in lexical order. gpu-11 sorts before
	// gpu-3 because the names are compared as strings, not as numbers. Pinning
	// the exact sequence checks that every device is kept (no drop, duplicate,
	// or empty result) and documents the order, which the scheduler uses for
	// first-fit allocation.
	want := []string{
		"gpu-1-128",
		"gpu-11-138",
		"gpu-17-144",
		"gpu-3-130",
		"gpu-9-136",
	}

	// Map iteration order is not defined, so repeating the call also catches an
	// accidental removal of the sort.
	for i := 0; i < 50; i++ {
		got := deviceNames(resourceSliceDevices(allocatable))
		if !slices.Equal(got, want) {
			t.Fatalf("device order mismatch on call %d: got %v, want %v", i, got, want)
		}
	}
}

// TestChunkDevices guards the ResourceSlice chunking fix: a node with more
// partitionable GPUs than resourceapi.ResourceSliceMaxDevicesWithAdvancedFeatures
// (64) synthetic devices must produce multiple Devices slices instead of one
// oversized (API-invalid) slice.
func TestChunkDevices(t *testing.T) {
	makeDevices := func(n int) []resourceapi.Device {
		devices := make([]resourceapi.Device, n)
		for i := range devices {
			devices[i] = resourceapi.Device{Name: string(rune('a' + i%26))}
		}
		return devices
	}

	tests := map[string]struct {
		count      int
		size       int
		wantChunks []int // length of each expected chunk, in order
	}{
		"empty":               {count: 0, size: 64, wantChunks: nil},
		"under limit":         {count: 5, size: 64, wantChunks: []int{5}},
		"exactly at limit":    {count: 64, size: 64, wantChunks: []int{64}},
		"one over limit":      {count: 65, size: 64, wantChunks: []int{64, 1}},
		"several chunks":      {count: 150, size: 64, wantChunks: []int{64, 64, 22}},
		"9 GPUs x 6 devices":  {count: 54, size: 64, wantChunks: []int{54}},
		"11 GPUs x 6 devices": {count: 66, size: 64, wantChunks: []int{64, 2}},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			chunks := chunkDevices(makeDevices(test.count), test.size)
			if len(chunks) != len(test.wantChunks) {
				t.Fatalf("got %d chunks, want %d (%v)", len(chunks), len(test.wantChunks), test.wantChunks)
			}
			total := 0
			for i, chunk := range chunks {
				if len(chunk) != test.wantChunks[i] {
					t.Errorf("chunk %d: got len %d, want %d", i, len(chunk), test.wantChunks[i])
				}
				if len(chunk) > test.size {
					t.Errorf("chunk %d exceeds size limit %d: len %d", i, test.size, len(chunk))
				}
				total += len(chunk)
			}
			if total != test.count {
				t.Errorf("total devices across chunks: got %d, want %d", total, test.count)
			}
		})
	}
}

// TestChunkCounterSets mirrors TestChunkDevices for the shared-counter-set
// side: a node with more than resourceapi.ResourceSliceMaxCounterSets (8)
// partitionable GPUs must produce multiple SharedCounters slices.
func TestChunkCounterSets(t *testing.T) {
	makeCounterSets := func(n int) []resourceapi.CounterSet {
		sets := make([]resourceapi.CounterSet, n)
		for i := range sets {
			sets[i] = resourceapi.CounterSet{Name: string(rune('a' + i%26))}
		}
		return sets
	}

	tests := map[string]struct {
		count      int
		size       int
		wantChunks []int
	}{
		"empty":            {count: 0, size: 8, wantChunks: nil},
		"under limit":      {count: 3, size: 8, wantChunks: []int{3}},
		"exactly at limit": {count: 8, size: 8, wantChunks: []int{8}},
		"one over limit":   {count: 9, size: 8, wantChunks: []int{8, 1}},
		"several chunks":   {count: 20, size: 8, wantChunks: []int{8, 8, 4}},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			chunks := chunkCounterSets(makeCounterSets(test.count), test.size)
			if len(chunks) != len(test.wantChunks) {
				t.Fatalf("got %d chunks, want %d (%v)", len(chunks), len(test.wantChunks), test.wantChunks)
			}
			total := 0
			for i, chunk := range chunks {
				if len(chunk) != test.wantChunks[i] {
					t.Errorf("chunk %d: got len %d, want %d", i, len(chunk), test.wantChunks[i])
				}
				if len(chunk) > test.size {
					t.Errorf("chunk %d exceeds size limit %d: len %d", i, test.size, len(chunk))
				}
				total += len(chunk)
			}
			if total != test.count {
				t.Errorf("total counter sets across chunks: got %d, want %d", total, test.count)
			}
		})
	}
}

// vfioVFs returns allocatable VFIO VFs spread over numPFs SR-IOV PFs,
// vfsPerPF each. Every VF consumes its PF's vf-slots counter.
func vfioVFs(numPFs, vfsPerPF int) AllocatableDevices {
	devs := make(AllocatableDevices)
	idx := 0
	for pf := 0; pf < numPFs; pf++ {
		pfAddr := fmt.Sprintf("0000:%02x:00.0", 0x10+pf)
		for vf := 0; vf < vfsPerPF; vf++ {
			devs[fmt.Sprintf("gpu-vfio-%d", idx)] = &AllocatableDevice{Vfio: &AmdGpuVFIOInfo{
				Index:           idx,
				PCIAddress:      fmt.Sprintf("0000:%02x:00.%d", 0x10+pf, vf+1),
				IsVF:            true,
				ParentPFAddress: pfAddr,
				TotalVFs:        vfsPerPF,
				NumVFs:          vfsPerPF,
			}}
			idx++
		}
	}
	return devs
}

// checkSliceLimits asserts that every slice respects the API limits, that
// slices hold either counters or devices, that every device is published
// exactly once, and that every consumed counter set is published.
func checkSliceLimits(t *testing.T, pool resourceslice.Pool, wantDevices int) (counterSlices, deviceSlices int) {
	t.Helper()
	counterSets := map[string]bool{}
	seen := map[string]bool{}
	var devices []resourceapi.Device
	for _, sl := range pool.Slices {
		require.False(t, len(sl.SharedCounters) > 0 && len(sl.Devices) > 0, "slice mixes counters and devices")
		if len(sl.SharedCounters) > 0 {
			counterSlices++
			assert.LessOrEqual(t, len(sl.SharedCounters), resourceapi.ResourceSliceMaxCounterSets)
			for _, cs := range sl.SharedCounters {
				counterSets[cs.Name] = true
			}
			continue
		}
		deviceSlices++
		limit := resourceapi.ResourceSliceMaxDevices
		for _, d := range sl.Devices {
			if len(d.ConsumesCounters) > 0 {
				limit = resourceapi.ResourceSliceMaxDevicesWithAdvancedFeatures
			}
		}
		assert.LessOrEqual(t, len(sl.Devices), limit)
		devices = append(devices, sl.Devices...)
	}
	for _, d := range devices {
		assert.False(t, seen[d.Name], "device %s published twice", d.Name)
		seen[d.Name] = true
		for _, c := range d.ConsumesCounters {
			assert.True(t, counterSets[c.CounterSet], "device %s consumes unpublished counter set %s", d.Name, c.CounterSet)
		}
	}
	assert.Len(t, devices, wantDevices)
	return counterSlices, deviceSlices
}

func TestBuildDriverResources_SliceLimits(t *testing.T) {
	build := func(devs AllocatableDevices) resourceslice.Pool {
		d := &driver{state: &DeviceState{allocatable: devs}}
		return d.buildDriverResources("test-node").Pools["test-node"]
	}

	t.Run("9 SR-IOV PFs split counter sets across slices", func(t *testing.T) {
		counterSlices, deviceSlices := checkSliceLimits(t, build(vfioVFs(9, 1)), 9)
		assert.Equal(t, 2, counterSlices, "8 + 1 counter sets")
		assert.Equal(t, 1, deviceSlices)
	})

	t.Run("65 counter-consuming devices split at 64", func(t *testing.T) {
		counterSlices, deviceSlices := checkSliceLimits(t, build(vfioVFs(5, 13)), 65)
		assert.Equal(t, 1, counterSlices)
		assert.Equal(t, 2, deviceSlices, "64 + 1 devices")
	})

	t.Run("64 counter-consuming devices fit one slice", func(t *testing.T) {
		_, deviceSlices := checkSliceLimits(t, build(vfioVFs(8, 8)), 64)
		assert.Equal(t, 1, deviceSlices)
	})

	t.Run("devices without counters keep the 128 limit", func(t *testing.T) {
		devs := make(AllocatableDevices)
		for i := 0; i < 129; i++ {
			devs[fmt.Sprintf("gpu-%d-%d", i, 128+i)] = &AllocatableDevice{AmdGpu: &AmdGpuInfo{cardIndex: i, renderIndex: 128 + i}}
		}
		counterSlices, deviceSlices := checkSliceLimits(t, build(devs), 129)
		assert.Equal(t, 0, counterSlices)
		assert.Equal(t, 2, deviceSlices, "128 + 1 devices")
	})

	t.Run("no devices still publishes one empty device slice", func(t *testing.T) {
		pool := build(AllocatableDevices{})
		require.Len(t, pool.Slices, 1)
		assert.Empty(t, pool.Slices[0].Devices)
	})
}
