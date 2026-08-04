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

import "testing"

// Two devices that resolve to the same canonical name must not silently overwrite
// each other; discovery reports the collision instead of dropping a GPU.
func TestAddAllocatableDeviceRejectsCollision(t *testing.T) {
	devices := make(AllocatableDevices)
	first := &AllocatableDevice{AmdGpu: &AmdGpuInfo{cardIndex: 0, renderIndex: 128}}
	if err := addAllocatableDevice(devices, first); err != nil {
		t.Fatalf("first insert: unexpected error %v", err)
	}

	dup := &AllocatableDevice{AmdGpu: &AmdGpuInfo{cardIndex: 0, renderIndex: 128}}
	if err := addAllocatableDevice(devices, dup); err == nil {
		t.Fatalf("colliding insert: want an error, got nil")
	}
	if devices[first.CanonicalName()] != first {
		t.Fatalf("colliding insert overwrote the original device")
	}
}
