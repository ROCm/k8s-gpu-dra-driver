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

	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"
)

func TestRestoreFromVfio(t *testing.T) {
	original := &AmdGpuInfo{PCIAddress: "0000:0d:00.0", cardIndex: 0, renderIndex: 128}
	state := &DeviceState{
		allocatable: AllocatableDevices{
			"gpu-0-128": {Vfio: &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0"}},
		},
		claimVfioConversions: map[string]map[string]*AmdGpuInfo{
			"claim-a": {"gpu-0-128": original},
		},
	}

	state.restoreFromVfio("claim-a", "gpu-0-128")

	allocDev := state.allocatable["gpu-0-128"]
	assert.NotNil(t, allocDev.AmdGpu, "AmdGpu should be restored")
	assert.Nil(t, allocDev.Vfio, "Vfio should be cleared")
	assert.Equal(t, "0000:0d:00.0", allocDev.AmdGpu.PCIAddress)
	assert.Equal(t, 0, allocDev.AmdGpu.cardIndex)
	assert.Equal(t, 128, allocDev.AmdGpu.renderIndex)
	assert.Equal(t, consts.AmdGpuDeviceType, allocDev.Type())
	assert.Empty(t, state.claimVfioConversions, "record should be removed once restored")
}

func TestRestoreFromVfio_NoConversion(t *testing.T) {
	state := &DeviceState{
		allocatable: AllocatableDevices{
			"gpu-vfio-0": {Vfio: &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0"}},
		},
		claimVfioConversions: map[string]map[string]*AmdGpuInfo{},
	}

	state.restoreFromVfio("claim-a", "gpu-vfio-0")

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
		state.restoreFromVfio("claim-a", "gpu-vfio-0")
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
		claimVfioConversions: map[string]map[string]*AmdGpuInfo{
			"test-claim": {"gpu-0-128": original},
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
		vfioManager: &VfioPciManager{},
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

// TestPartitionSharesForClaim_DriverAndPoolMismatch guards the identity check
// added to partitionSharesForClaim: a DRA device is identified by
// (driver, pool, device), not device name alone. A same-named result from
// another driver or another pool must not be treated as a local partition
// device, even though the name collides with a real local synthetic device.
func TestPartitionSharesForClaim_DriverAndPoolMismatch(t *testing.T) {
	const (
		nodeName   = "node-a"
		deviceName = "gpu-0-cpx-nps4"
	)
	allocatable := AllocatableDevices{
		deviceName: {SyntheticPartition: &SyntheticPartitionDevice{GPUIndex: 0}},
	}

	newClaim := func(driver, pool string) *resourceapi.ResourceClaim {
		return &resourceapi.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{UID: types.UID("claim-1")},
			Status: resourceapi.ResourceClaimStatus{
				Allocation: &resourceapi.AllocationResult{
					Devices: resourceapi.DeviceAllocationResult{
						Results: []resourceapi.DeviceRequestAllocationResult{
							{Request: "gpu", Driver: driver, Pool: pool, Device: deviceName},
						},
					},
				},
			},
		}
	}

	tests := map[string]struct {
		driver      string
		pool        string
		expectEmpty bool
	}{
		"matching driver and pool": {
			driver: consts.DriverName, pool: nodeName, expectEmpty: false,
		},
		"other driver, same pool": {
			driver: "other-driver.example.com", pool: nodeName, expectEmpty: true,
		},
		"local driver, other pool": {
			driver: consts.DriverName, pool: "node-b", expectEmpty: true,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			shares := partitionSharesForClaim(newClaim(test.driver, test.pool), allocatable, nodeName)
			if test.expectEmpty {
				assert.Empty(t, shares, "result from a mismatched driver/pool must not be treated as a local partition device")
			} else {
				assert.Len(t, shares, 1)
			}
		})
	}
}
