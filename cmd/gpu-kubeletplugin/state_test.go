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
	"testing"

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/consts"

	"github.com/stretchr/testify/assert"

	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"
)

func TestRestoreFromVfio(t *testing.T) {
	original := &AmdGpuInfo{PCIAddress: "0000:0d:00.0", cardIndex: 0, renderIndex: 128}
	state := &DeviceState{
		allocatable: AllocatableDevices{
			"gpu-0-128": {Vfio: &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0"}},
		},
		claimVfioConversions: map[string]*AmdGpuInfo{
			"gpu-0-128": original,
		},
	}

	state.restoreFromVfio("gpu-0-128")

	allocDev := state.allocatable["gpu-0-128"]
	assert.NotNil(t, allocDev.AmdGpu, "AmdGpu should be restored")
	assert.Nil(t, allocDev.Vfio, "Vfio should be cleared")
	assert.Equal(t, "0000:0d:00.0", allocDev.AmdGpu.PCIAddress)
	assert.Equal(t, 0, allocDev.AmdGpu.cardIndex)
	assert.Equal(t, 128, allocDev.AmdGpu.renderIndex)
	assert.Equal(t, consts.AmdGpuDeviceType, allocDev.Type())
	_, inMap := state.claimVfioConversions["gpu-0-128"]
	assert.False(t, inMap, "device should be removed from claimVfioConversions")
}

func TestRestoreFromVfio_NoConversion(t *testing.T) {
	state := &DeviceState{
		allocatable: AllocatableDevices{
			"gpu-vfio-0": {Vfio: &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0"}},
		},
		claimVfioConversions: map[string]*AmdGpuInfo{},
	}

	state.restoreFromVfio("gpu-vfio-0")

	allocDev := state.allocatable["gpu-vfio-0"]
	assert.NotNil(t, allocDev.Vfio, "pre-discovered VFIO device should stay as VFIO")
	assert.Nil(t, allocDev.AmdGpu, "should not gain an AmdGpu entry")
}

func TestRestoreFromVfio_NilMap(t *testing.T) {
	state := &DeviceState{
		allocatable: AllocatableDevices{
			"gpu-vfio-0": {Vfio: &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0"}},
		},
	}

	assert.NotPanics(t, func() {
		state.restoreFromVfio("gpu-vfio-0")
	})
}

func TestUnprepareDevices_RestoresConvertedDevice(t *testing.T) {
	original := &AmdGpuInfo{PCIAddress: "0000:0d:00.0", cardIndex: 0, renderIndex: 128}
	state := &DeviceState{
		allocatable: AllocatableDevices{
			"gpu-0-128": {Vfio: &AmdGpuVFIOInfo{
				PCIAddress:         "0000:0d:00.0",
				preConfigureDriver: "vfio-pci",
			}},
		},
		claimVfioConversions: map[string]*AmdGpuInfo{
			"gpu-0-128": original,
		},
		vfioManager: &VfioPciManager{},
	}
	devices := PreparedDevices{
		{Device: drapbv1.Device{DeviceName: "gpu-0-128"}},
	}

	err := state.unprepareDevices("test-claim", devices)
	assert.NoError(t, err)

	allocDev := state.allocatable["gpu-0-128"]
	assert.NotNil(t, allocDev.AmdGpu, "AmdGpu should be restored after unprepare")
	assert.Nil(t, allocDev.Vfio, "Vfio should be cleared after unprepare")
	assert.Equal(t, consts.AmdGpuDeviceType, allocDev.Type())
}

func TestUnprepareDevices_PreDiscoveredVfioNotRestored(t *testing.T) {
	state := &DeviceState{
		allocatable: AllocatableDevices{
			"gpu-vfio-0": {Vfio: &AmdGpuVFIOInfo{
				PCIAddress:         "0000:0d:00.0",
				preConfigureDriver: "vfio-pci",
			}},
		},
		claimVfioConversions: map[string]*AmdGpuInfo{},
		vfioManager:          &VfioPciManager{},
	}
	devices := PreparedDevices{
		{Device: drapbv1.Device{DeviceName: "gpu-vfio-0"}},
	}

	err := state.unprepareDevices("test-claim", devices)
	assert.NoError(t, err)

	allocDev := state.allocatable["gpu-vfio-0"]
	assert.NotNil(t, allocDev.Vfio, "pre-discovered VFIO should stay as VFIO")
	assert.Nil(t, allocDev.AmdGpu, "should not gain an AmdGpu entry")
}

func TestRemoveAndRestoreSiblingDevices(t *testing.T) {
	pciAddr := "0000:0a:00.0"
	computeDev := &AllocatableDevice{AmdGpu: &AmdGpuInfo{PCIAddress: pciAddr, cardIndex: 0, renderIndex: 128}}
	vfioDev := &AllocatableDevice{Vfio: &AmdGpuVFIOInfo{
		PCIAddress:  pciAddr,
		IsVF:        false,
		Index:       0,
		MemoryBytes: 206158430208,
	}}

	state := &DeviceState{
		allocatable: AllocatableDevices{
			"gpu-0-128":  computeDev,
			"gpu-vfio-0": vfioDev,
		},
		siblingCache: make(AllocatableDevices),
	}
	state.buildPCIIndex()

	state.RemoveSiblingDevices("gpu-0-128", computeDev)
	assert.Contains(t, state.allocatable, "gpu-0-128")
	assert.NotContains(t, state.allocatable, "gpu-vfio-0")
	assert.Contains(t, state.siblingCache, "gpu-vfio-0")

	state.RestoreSiblingDevices(computeDev)
	assert.Contains(t, state.allocatable, "gpu-0-128")
	assert.Contains(t, state.allocatable, "gpu-vfio-0")
	assert.NotContains(t, state.siblingCache, "gpu-vfio-0")
}

func TestSiblingCachePreservesCapacity(t *testing.T) {
	pciAddr := "0000:0a:00.0"
	computeDev := &AllocatableDevice{AmdGpu: &AmdGpuInfo{PCIAddress: pciAddr, cardIndex: 0, renderIndex: 128}}
	vfioDev := &AllocatableDevice{Vfio: &AmdGpuVFIOInfo{
		PCIAddress:   pciAddr,
		IsVF:         false,
		Index:        0,
		MemoryBytes:  206158430208,
		ComputeUnits: 304,
		SimdUnits:    1216,
	}}

	state := &DeviceState{
		allocatable: AllocatableDevices{
			"gpu-0-128":  computeDev,
			"gpu-vfio-0": vfioDev,
		},
		siblingCache: make(AllocatableDevices),
	}
	state.buildPCIIndex()

	state.RemoveSiblingDevices("gpu-0-128", computeDev)
	state.RestoreSiblingDevices(computeDev)

	restored := state.allocatable["gpu-vfio-0"]
	assert.Equal(t, uint64(206158430208), restored.Vfio.MemoryBytes)
	assert.Equal(t, 304, restored.Vfio.ComputeUnits)
	assert.Equal(t, 1216, restored.Vfio.SimdUnits)
}

func TestRemoveSiblingDevices_NoSiblings(t *testing.T) {
	dev := &AllocatableDevice{AmdGpu: &AmdGpuInfo{PCIAddress: "0000:0a:00.0", cardIndex: 0, renderIndex: 128}}
	state := &DeviceState{
		allocatable: AllocatableDevices{
			"gpu-0-128": dev,
		},
		siblingCache: make(AllocatableDevices),
	}
	state.buildPCIIndex()

	assert.NotPanics(t, func() {
		state.RemoveSiblingDevices("gpu-0-128", dev)
	})
	assert.Contains(t, state.allocatable, "gpu-0-128")
	assert.Empty(t, state.siblingCache)
}

func TestRestoreSiblingDevices_EmptyCache(t *testing.T) {
	dev := &AllocatableDevice{AmdGpu: &AmdGpuInfo{PCIAddress: "0000:0a:00.0", cardIndex: 0, renderIndex: 128}}
	state := &DeviceState{
		allocatable: AllocatableDevices{
			"gpu-0-128": dev,
		},
		siblingCache: make(AllocatableDevices),
	}
	state.buildPCIIndex()

	assert.NotPanics(t, func() {
		state.RestoreSiblingDevices(dev)
	})
	assert.Contains(t, state.allocatable, "gpu-0-128")
}

func TestBuildPCIIndex(t *testing.T) {
	state := &DeviceState{
		allocatable: AllocatableDevices{
			"gpu-0-128":  {AmdGpu: &AmdGpuInfo{PCIAddress: "0000:0a:00.0", cardIndex: 0, renderIndex: 128}},
			"gpu-vfio-0": {Vfio: &AmdGpuVFIOInfo{PCIAddress: "0000:0a:00.0", IsVF: false, Index: 0}},
			"gpu-vfio-1": {Vfio: &AmdGpuVFIOInfo{PCIAddress: "0000:0b:00.0", IsVF: true, Index: 1}},
			"gpu-1-129":  {AmdPartition: &AmdPartitionInfo{Parent: &AmdGpuInfo{PCIAddress: "0000:0c:00.0"}, cardIndex: 1, renderIndex: 129}},
		},
		siblingCache: make(AllocatableDevices),
	}
	state.buildPCIIndex()

	assert.Len(t, state.byPCIAddress["0000:0a:00.0"], 2, "compute GPU and VFIO PF share PCI address")
	assert.Contains(t, state.byPCIAddress["0000:0a:00.0"], "gpu-0-128")
	assert.Contains(t, state.byPCIAddress["0000:0a:00.0"], "gpu-vfio-0")

	_, hasVF := state.byPCIAddress["0000:0b:00.0"]
	assert.False(t, hasVF, "VFIO VF should not be indexed (returns empty PCI address)")

	_, hasPartition := state.byPCIAddress["0000:0c:00.0"]
	assert.False(t, hasPartition, "AmdPartition should not be indexed")
}

func TestPreparedDevicesGetDevices(t *testing.T) {
	tests := map[string]struct {
		preparedDevices PreparedDevices
		expected        []*drapbv1.Device
	}{
		"nil PreparedDevices": {
			preparedDevices: nil,
			expected:        nil,
		},
		"several PreparedDevices": {
			preparedDevices: PreparedDevices{
				{Device: drapbv1.Device{DeviceName: "dev1"}},
				{Device: drapbv1.Device{DeviceName: "dev2"}},
				{Device: drapbv1.Device{DeviceName: "dev3"}},
			},
			expected: []*drapbv1.Device{
				{DeviceName: "dev1"},
				{DeviceName: "dev2"},
				{DeviceName: "dev3"},
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			devices := test.preparedDevices.GetDevices()
			assert.Equal(t, test.expected, devices)
		})
	}
}
