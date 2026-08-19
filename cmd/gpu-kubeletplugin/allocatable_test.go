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
)

func TestGetSiblingLookupPCIAddress(t *testing.T) {
	tests := map[string]struct {
		device   *AllocatableDevice
		expected string
	}{
		"AmdGpu returns PCIAddress": {
			device:   &AllocatableDevice{AmdGpu: &AmdGpuInfo{PCIAddress: "0000:0a:00.0"}},
			expected: "0000:0a:00.0",
		},
		"VFIO PF returns PCIAddress": {
			device:   &AllocatableDevice{Vfio: &AmdGpuVFIOInfo{PCIAddress: "0000:0a:00.0", IsVF: false}},
			expected: "0000:0a:00.0",
		},
		"VFIO VF returns empty": {
			device:   &AllocatableDevice{Vfio: &AmdGpuVFIOInfo{PCIAddress: "0000:0b:00.0", IsVF: true}},
			expected: "",
		},
		"AmdPartition returns empty": {
			device:   &AllocatableDevice{AmdPartition: &AmdPartitionInfo{Parent: &AmdGpuInfo{PCIAddress: "0000:0a:00.0"}}},
			expected: "",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.expected, tc.device.GetSiblingLookupPCIAddress())
		})
	}
}
