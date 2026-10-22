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
	"os"
	"path/filepath"
	"testing"

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/consts"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/featuregates"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
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

// TestPrepareDevices_ConversionPreservesNumaNodeAttr covers the on-demand
// AMD GPU to VFIO conversion path in prepareDevices(): converting a compute
// GPU carrying the standard numaNode attribute must copy numaNodeAttr onto
// the resulting AmdGpuVFIOInfo, the same way pciBusIDAttr and pcieRootAttr
// are already copied. Before this fix the attribute was dropped, so a
// converted device's GetDevice() omitted resource.kubernetes.io/numaNode
// even though DeviceMetadata had previously advertised it for scheduling
// (review comment 4332719438).
func TestPrepareDevices_ConversionPreservesNumaNodeAttr(t *testing.T) {
	fg := featuregates.FeatureGates()
	require.NoError(t, fg.Set(string(featuregates.VFIOPassthrough)+"=true"))
	t.Cleanup(func() { _ = fg.Set(string(featuregates.VFIOPassthrough) + "=false") })

	root := setupFakeVfioSysfs(t)
	const pci = "0000:0d:00.0"
	createPCIDevice(t, root, pci, "amdgpu")
	createDriverDir(t, root, "amdgpu")
	createDriverDir(t, root, consts.VFIODriverName)
	require.NoError(t, os.Symlink("../../../kernel/iommu_groups/42",
		filepath.Join(root, "sys/bus/pci/devices", pci, "iommu_group")))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "dev/vfio"), 0755))
	require.NoError(t, os.Symlink("/dev/null", filepath.Join(root, "dev/vfio/vfio")))
	require.NoError(t, os.Symlink("/dev/null", filepath.Join(root, "dev/vfio/42")))

	numaAttr := deviceattribute.DeviceAttribute{
		Name:  "resource.kubernetes.io/numaNode",
		Value: resourceapi.DeviceAttribute{IntValue: func() *int64 { v := int64(7); return &v }()},
	}
	gpu := &AmdGpuInfo{PCIAddress: pci, cardIndex: 0, renderIndex: 128, numaNodeAttr: numaAttr}
	state := &DeviceState{
		allocatable: AllocatableDevices{
			"gpu-0-128": {AmdGpu: gpu},
		},
		vfioManager: &VfioPciManager{},
	}

	claim := &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID("claim-uid")},
		Status: resourceapi.ResourceClaimStatus{
			Allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Results: []resourceapi.DeviceRequestAllocationResult{
						{Request: "gpu", Driver: consts.DriverName, Pool: "node", Device: "gpu-0-128"},
					},
					Config: []resourceapi.DeviceAllocationConfiguration{{
						Source: resourceapi.AllocationConfigSourceClaim,
						DeviceConfiguration: resourceapi.DeviceConfiguration{
							Opaque: &resourceapi.OpaqueDeviceConfiguration{
								Driver:     consts.DriverName,
								Parameters: runtime.RawExtension{Raw: []byte(`{"apiVersion":"gpu.resource.amd.com/v1alpha1","kind":"VfioDeviceConfig"}`)},
							},
						},
					}},
				},
			},
		},
	}

	_, err := state.prepareDevices(claim)
	require.NoError(t, err)

	converted := state.allocatable["gpu-0-128"]
	require.NotNil(t, converted.Vfio, "the device must have been converted to VFIO")
	assert.Equal(t, numaAttr, converted.Vfio.numaNodeAttr, "numaNodeAttr must survive the amdgpu to VFIO conversion")
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
