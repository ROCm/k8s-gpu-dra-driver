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

	resourceapi "k8s.io/api/resource/v1"
)

func deviceNames(devices []resourceapi.Device) []string {
	names := make([]string, len(devices))
	for i, d := range devices {
		names[i] = d.Name
	}
	return names
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
