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

	resourceapi "k8s.io/api/resource/v1"
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
