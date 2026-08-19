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
	"strings"
	"testing"
)

// Two devices that resolve to the same canonical name must not silently overwrite each
// other; discovery reports the collision, with the provenance of both sides, instead of
// dropping a GPU.
func TestAddAllocatableDeviceRejectsCollision(t *testing.T) {
	devices := make(AllocatableDevices)
	first := &AllocatableDevice{AmdGpu: &AmdGpuInfo{cardIndex: 0, renderIndex: 128, PCIAddress: "0000:03:00.0"}}
	if err := addAllocatableDevice(devices, first); err != nil {
		t.Fatalf("first insert: unexpected error %v", err)
	}

	dup := &AllocatableDevice{AmdGpu: &AmdGpuInfo{cardIndex: 0, renderIndex: 128, PCIAddress: "0000:04:00.0"}}
	err := addAllocatableDevice(devices, dup)
	if err == nil {
		t.Fatalf("colliding insert: want an error, got nil")
	}
	for _, want := range []string{"0000:03:00.0", "0000:04:00.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("collision error %q should name the colliding device %s", err, want)
		}
	}
	if devices[first.CanonicalName()] != first {
		t.Fatalf("colliding insert overwrote the original device")
	}
}

// Each of the modes AMD defines is classified, and anything else is an error rather than
// a guess: publishing a mode the driver cannot interpret would turn unknown hardware
// state into an allocatable device, and skipping it would lose the GPU silently.
func TestClassifyComputePartition(t *testing.T) {
	for _, tc := range []struct {
		in        string
		partition bool
		wantErr   bool
	}{
		{in: "dpx", partition: true},
		{in: "tpx", partition: true}, // valid on MI300A
		{in: "qpx", partition: true},
		{in: "cpx", partition: true},
		{in: "spx"}, // the single-partition mode is a whole GPU
		{in: ""},    // no partitioning support
		{in: "invalid", wantErr: true},
		{in: "unknown", wantErr: true},
		{in: "zpx", wantErr: true},
	} {
		got, err := classifyComputePartition(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("classifyComputePartition(%q) error = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if got != tc.partition {
			t.Errorf("classifyComputePartition(%q) = %v, want %v", tc.in, got, tc.partition)
		}
	}
}

// An optional PCIe attribute lookup can fail, but the device's own BDF is always known, so
// getPcieInfo must return it rather than an empty string, otherwise a partition's parent
// PCIAddress ends up blank.
func TestGetPcieInfoPreservesBDFOnError(t *testing.T) {
	const bogus = "0000:ff:ff.7" // no such device, so the attribute lookup fails
	_, _, addr, err := getPcieInfo(map[string]interface{}{"pciAddr": bogus})
	if err == nil {
		t.Fatalf("want an error for a nonexistent BDF, got nil")
	}
	if addr != bogus {
		t.Errorf("getPcieInfo returned addr %q on error, want the input BDF %q", addr, bogus)
	}
}
