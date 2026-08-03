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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	cdispec "tags.cncf.io/container-device-interface/specs-go"
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

func TestReconcileCDISpecs(t *testing.T) {
	cdiRoot := t.TempDir()
	cache, err := cdiapi.NewCache(cdiapi.WithSpecDirs(cdiRoot))
	assert.NoError(t, err)
	cm, err := checkpointmanager.NewCheckpointManager(t.TempDir())
	assert.NoError(t, err)

	s := &DeviceState{cdi: &CDIHandler{cache: cache}, checkpointManager: cm}

	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims["claim-uid-1"] = PreparedDevices{
		{
			Device: drapbv1.Device{DeviceName: "gpu-0-128"},
			ContainerEdits: &cdiapi.ContainerEdits{ContainerEdits: &cdispec.ContainerEdits{
				DeviceNodes: []*cdispec.DeviceNode{
					{Path: "/dev/kfd", HostPath: "/dev/kfd", Type: "c", Major: 1, Minor: 1, Permissions: "rw"},
				},
			}},
		},
	}
	assert.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	// The spec dir starts empty, as a reboot leaves it.
	before, err := os.ReadDir(cdiRoot)
	assert.NoError(t, err)
	assert.Empty(t, before)

	assert.NoError(t, s.reconcileCDISpecs())

	after, err := os.ReadDir(cdiRoot)
	assert.NoError(t, err)
	require.NotEmpty(t, after, "reconcile should rebuild the CDI spec from the checkpoint")

	data, err := os.ReadFile(filepath.Join(cdiRoot, after[0].Name()))
	assert.NoError(t, err)
	assert.Contains(t, string(data), "/dev/kfd", "the rebuilt spec must carry the device node from the checkpoint")
}

func TestValidatePreparedDevices(t *testing.T) {
	edits := &cdiapi.ContainerEdits{ContainerEdits: &cdispec.ContainerEdits{}}
	valid := PreparedDevices{{Device: drapbv1.Device{DeviceName: "gpu-0-128"}, ContainerEdits: edits}}
	require.NoError(t, validatePreparedDevices("uid", valid))

	bad := map[string]PreparedDevices{
		"empty":               {},
		"nil device":          {nil},
		"no device name":      {{Device: drapbv1.Device{}, ContainerEdits: edits}},
		"nil container edits": {{Device: drapbv1.Device{DeviceName: "gpu-0-128"}}},
	}
	for name, pds := range bad {
		t.Run(name, func(t *testing.T) {
			require.Error(t, validatePreparedDevices("uid", pds))
		})
	}
}

// A checkpoint-hit Prepare must recreate a spec that a tmpfs-clearing restart
// removed, rather than reporting the claim as prepared with no spec on disk.
func TestPrepareRepairsMissingCheckpointedSpec(t *testing.T) {
	cdiRoot := t.TempDir()
	cache, err := cdiapi.NewCache(cdiapi.WithSpecDirs(cdiRoot))
	require.NoError(t, err)
	cm, err := checkpointmanager.NewCheckpointManager(t.TempDir())
	require.NoError(t, err)

	s := &DeviceState{cdi: &CDIHandler{cache: cache}, checkpointManager: cm}

	const claimUID = "claim-uid-1"
	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims[claimUID] = PreparedDevices{
		{
			Device: drapbv1.Device{DeviceName: "gpu-0-128"},
			ContainerEdits: &cdiapi.ContainerEdits{ContainerEdits: &cdispec.ContainerEdits{
				DeviceNodes: []*cdispec.DeviceNode{
					{Path: "/dev/kfd", HostPath: "/dev/kfd", Type: "c", Major: 1, Minor: 1, Permissions: "rw"},
				},
			}},
		},
	}
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	before, err := os.ReadDir(cdiRoot)
	require.NoError(t, err)
	require.Empty(t, before)

	claim := &resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{UID: types.UID(claimUID)}}
	devices, err := s.Prepare(claim)
	require.NoError(t, err)
	require.Len(t, devices, 1)

	after, err := os.ReadDir(cdiRoot)
	require.NoError(t, err)
	require.NotEmpty(t, after, "a checkpoint-hit Prepare must recreate the missing CDI spec")
}

// A malformed checkpoint entry is logged and skipped at startup, not a panic.
func TestReconcileSkipsMalformedCheckpointEntry(t *testing.T) {
	cdiRoot := t.TempDir()
	cache, err := cdiapi.NewCache(cdiapi.WithSpecDirs(cdiRoot))
	require.NoError(t, err)
	cm, err := checkpointmanager.NewCheckpointManager(t.TempDir())
	require.NoError(t, err)

	s := &DeviceState{cdi: &CDIHandler{cache: cache}, checkpointManager: cm}

	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims["bad"] = PreparedDevices{nil}
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	require.NoError(t, s.reconcileCDISpecs())
}

// A checkpoint-hit Prepare must fail closed on a malformed entry rather than
// report the claim as prepared. A device with no name cannot become a CDI spec,
// so Prepare has to return an error instead of the devices.
func TestPrepareRejectsMalformedCheckpointEntry(t *testing.T) {
	cdiRoot := t.TempDir()
	cache, err := cdiapi.NewCache(cdiapi.WithSpecDirs(cdiRoot))
	require.NoError(t, err)
	cm, err := checkpointmanager.NewCheckpointManager(t.TempDir())
	require.NoError(t, err)

	s := &DeviceState{cdi: &CDIHandler{cache: cache}, checkpointManager: cm}

	const claimUID = "claim-uid-1"
	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims[claimUID] = PreparedDevices{
		{Device: drapbv1.Device{DeviceName: ""}, ContainerEdits: &cdiapi.ContainerEdits{ContainerEdits: &cdispec.ContainerEdits{}}},
	}
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	claim := &resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{UID: types.UID(claimUID)}}
	_, err = s.Prepare(claim)
	require.Error(t, err, "checkpoint-hit Prepare with a malformed entry must fail closed, not report success")
}

// A checkpoint entry whose container edits wrapper is present but hollow (nil
// inner edits) must be rejected during reconciliation, not dereferenced into a
// panic while rebuilding the CDI spec.
func TestReconcileRejectsHollowContainerEdits(t *testing.T) {
	cdiRoot := t.TempDir()
	cache, err := cdiapi.NewCache(cdiapi.WithSpecDirs(cdiRoot))
	require.NoError(t, err)
	cm, err := checkpointmanager.NewCheckpointManager(t.TempDir())
	require.NoError(t, err)

	s := &DeviceState{cdi: &CDIHandler{cache: cache}, checkpointManager: cm}

	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims["hollow"] = PreparedDevices{
		{Device: drapbv1.Device{DeviceName: "gpu-0-128"}, ContainerEdits: &cdiapi.ContainerEdits{}},
	}
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	// Must not panic; the malformed entry is logged and skipped.
	require.NoError(t, s.reconcileCDISpecs())
}
