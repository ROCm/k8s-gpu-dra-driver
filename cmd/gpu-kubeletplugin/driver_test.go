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
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"

	resourceapi "k8s.io/api/resource/v1"
)

func deviceNames(devices []resourceapi.Device) []string {
	names := make([]string, len(devices))
	for i, d := range devices {
		names[i] = d.Name
	}
	return names
}

func TestCollectVFIOCounterSets(t *testing.T) {
	t.Run("deduplicates VFs from same PF", func(t *testing.T) {
		d := &driver{state: &DeviceState{
			allocatable: AllocatableDevices{
				"gpu-vfio-0": {Vfio: &AmdGpuVFIOInfo{Index: 0, IsVF: true, TotalVFs: 4, ParentPFAddress: "0000:0a:00.0"}},
				"gpu-vfio-1": {Vfio: &AmdGpuVFIOInfo{Index: 1, IsVF: true, TotalVFs: 4, ParentPFAddress: "0000:0a:00.0"}},
				"gpu-0-128":  {AmdGpu: &AmdGpuInfo{cardIndex: 0, renderIndex: 128}},
			},
		}}
		sets := d.collectVFIOCounterSets()
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
		sets := d.collectVFIOCounterSets()
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
		sets := d.collectVFIOCounterSets()
		assert.Empty(t, sets)
	})
}

func TestBuildDriverResourcesWithCounters(t *testing.T) {
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
