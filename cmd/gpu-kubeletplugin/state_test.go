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
	"encoding/json"
	"fmt"
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
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/checksum"

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

func newCacheAndCheckpointer(t *testing.T) (string, *cdiapi.Cache, checkpointmanager.CheckpointManager) {
	t.Helper()
	cdiRoot := t.TempDir()
	cache, err := cdiapi.NewCache(cdiapi.WithSpecDirs(cdiRoot))
	require.NoError(t, err)
	cm, err := checkpointmanager.NewCheckpointManager(t.TempDir())
	require.NoError(t, err)
	return cdiRoot, cache, cm
}

// testDeviceState wires a DeviceState over a temp CDI cache and checkpoint dir with the
// named devices allocatable.
func testDeviceState(t *testing.T, cache *cdiapi.Cache, cm checkpointmanager.CheckpointManager, devices ...string) *DeviceState {
	t.Helper()
	alloc := AllocatableDevices{}
	for _, d := range devices {
		alloc[d] = &AllocatableDevice{}
	}
	s := &DeviceState{
		cdi:               &CDIHandler{cache: cache},
		allocatable:       alloc,
		checkpointManager: cm,
	}
	return s
}

// devNull is /dev/null as the host sees it, so a checkpointed node built from it passes the
// device-node check without hardcoding the numbers.
func devNull() cdispec.DeviceNode {
	major, minor, devType, _, err := getDeviceAttrs("/dev/null")
	if err != nil {
		panic(err)
	}
	return cdispec.DeviceNode{Path: "/dev/null", HostPath: "/dev/null", Type: devType, Major: major, Minor: minor, Permissions: "rw"}
}

// kfdDevices is a well-formed single-device claim naming gpu-0-128 whose node is /dev/null.
func kfdDevices() PreparedDevices {
	node := devNull()
	return PreparedDevices{
		{
			Device:         drapbv1.Device{DeviceName: "gpu-0-128"},
			ContainerEdits: &cdiapi.ContainerEdits{ContainerEdits: &cdispec.ContainerEdits{DeviceNodes: []*cdispec.DeviceNode{&node}}},
		},
	}
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

// Within the same boot, with the device still allocatable, reconcile rebuilds the
// per-claim CDI spec from the checkpoint.
func TestReconcileCDISpecs(t *testing.T) {
	cdiRoot, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm, "gpu-0-128")

	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims["claim-uid-1"] = kfdDevices()
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	before, err := os.ReadDir(cdiRoot)
	require.NoError(t, err)
	require.Empty(t, before)

	require.NoError(t, s.reconcileCDISpecs())

	after, err := os.ReadDir(cdiRoot)
	require.NoError(t, err)
	require.NotEmpty(t, after, "reconcile should rebuild the CDI spec from the checkpoint")

	data, err := os.ReadFile(filepath.Join(cdiRoot, after[0].Name()))
	require.NoError(t, err)
	require.Contains(t, string(data), "/dev/null", "the rebuilt spec must carry the device node from the checkpoint")
}

func TestValidatePreparedDevices(t *testing.T) {
	node := func() []*cdispec.DeviceNode {
		return []*cdispec.DeviceNode{{Path: "/dev/kfd", HostPath: "/dev/kfd", Type: "c", Major: 1, Minor: 3}}
	}
	editsWith := func(mutate func(*cdispec.ContainerEdits)) *cdiapi.ContainerEdits {
		e := &cdispec.ContainerEdits{DeviceNodes: node()}
		if mutate != nil {
			mutate(e)
		}
		return &cdiapi.ContainerEdits{ContainerEdits: e}
	}

	valid := PreparedDevices{{Device: drapbv1.Device{DeviceName: "gpu-0-128"}, ContainerEdits: editsWith(nil)}}
	require.NoError(t, validatePreparedDevices("uid", valid))

	// Each bad case keeps an otherwise usable edit set, so it fails on its own defect
	// rather than on a shared missing field.
	bad := map[string]struct {
		pds  PreparedDevices
		want string
	}{
		"empty":               {PreparedDevices{}, "no prepared devices"},
		"nil device":          {PreparedDevices{nil}, "nil prepared device"},
		"no device name":      {PreparedDevices{{Device: drapbv1.Device{}, ContainerEdits: editsWith(nil)}}, "no name"},
		"nil container edits": {PreparedDevices{{Device: drapbv1.Device{DeviceName: "gpu-0-128"}}}, "no container edits"},
		"hollow edits":        {PreparedDevices{{Device: drapbv1.Device{DeviceName: "gpu-0-128"}, ContainerEdits: &cdiapi.ContainerEdits{}}}, "no container edits"},
		"no device nodes": {PreparedDevices{{Device: drapbv1.Device{DeviceName: "gpu-0-128"},
			ContainerEdits: &cdiapi.ContainerEdits{ContainerEdits: &cdispec.ContainerEdits{}}}}, "grants no device nodes"},
		"nil deviceNode": {PreparedDevices{{Device: drapbv1.Device{DeviceName: "gpu-0-128"},
			ContainerEdits: editsWith(func(e *cdispec.ContainerEdits) { e.DeviceNodes = append(e.DeviceNodes, nil) })}}, "nil deviceNodes[1]"},
		"nil hook": {PreparedDevices{{Device: drapbv1.Device{DeviceName: "gpu-0-128"},
			ContainerEdits: editsWith(func(e *cdispec.ContainerEdits) { e.Hooks = []*cdispec.Hook{nil} })}}, "nil hooks[0]"},
		"nil mount": {PreparedDevices{{Device: drapbv1.Device{DeviceName: "gpu-0-128"},
			ContainerEdits: editsWith(func(e *cdispec.ContainerEdits) { e.Mounts = []*cdispec.Mount{nil} })}}, "nil mounts[0]"},
		"nil netDevice": {PreparedDevices{{Device: drapbv1.Device{DeviceName: "gpu-0-128"},
			ContainerEdits: editsWith(func(e *cdispec.ContainerEdits) { e.NetDevices = []*cdispec.LinuxNetDevice{nil} })}}, "nil netDevices[0]"},
	}
	for name, tc := range bad {
		t.Run(name, func(t *testing.T) {
			require.ErrorContains(t, validatePreparedDevices("uid", tc.pds), tc.want)
		})
	}
}

// A checkpoint-hit Prepare must recreate a spec that a tmpfs-clearing restart
// removed, rather than reporting the claim as prepared with no spec on disk.
func TestPrepareRepairsMissingCheckpointedSpec(t *testing.T) {
	cdiRoot, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm, "gpu-0-128")

	const claimUID = "claim-uid-1"
	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims[claimUID] = kfdDevices()
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

// A malformed checkpoint entry is logged and skipped at startup, not a panic, and
// leaves no spec behind.
func TestReconcileSkipsMalformedCheckpointEntry(t *testing.T) {
	cdiRoot, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm)

	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims["bad"] = PreparedDevices{nil}
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	require.NoError(t, s.reconcileCDISpecs())

	after, err := os.ReadDir(cdiRoot)
	require.NoError(t, err)
	require.Empty(t, after, "a malformed entry must be skipped, not written as a spec")
}

// A checkpoint-hit Prepare must fail closed on a malformed entry rather than
// report the claim as prepared. A device with no name cannot become a CDI spec.
func TestPrepareRejectsMalformedCheckpointEntry(t *testing.T) {
	_, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm)

	const claimUID = "claim-uid-1"
	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims[claimUID] = PreparedDevices{
		{Device: drapbv1.Device{DeviceName: ""}, ContainerEdits: &cdiapi.ContainerEdits{ContainerEdits: &cdispec.ContainerEdits{}}},
	}
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	claim := &resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{UID: types.UID(claimUID)}}
	_, err := s.Prepare(claim)
	require.ErrorContains(t, err, "cannot be prepared from its checkpoint entry")
}

// A non-nil but hollow ContainerEdits (nil inner edits) must be rejected during
// reconciliation, not dereferenced into a panic, and must leave no spec behind.
func TestReconcileRejectsHollowContainerEdits(t *testing.T) {
	cdiRoot, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm, "gpu-0-128")

	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims["hollow"] = PreparedDevices{
		{Device: drapbv1.Device{DeviceName: "gpu-0-128"}, ContainerEdits: &cdiapi.ContainerEdits{}},
	}
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	require.NoError(t, s.reconcileCDISpecs())

	after, err := os.ReadDir(cdiRoot)
	require.NoError(t, err)
	require.Empty(t, after, "a hollow container-edits entry must not be written as a spec")
}

// A checkpointed device that is no longer in the allocatable inventory (removed or
// skipped by discovery this boot) must be skipped, not rebuilt from the checkpoint.
func TestReconcileSkipsDeviceNotAllocatable(t *testing.T) {
	cdiRoot, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm) // gpu-0-128 is deliberately absent from allocatable

	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims["claim-uid-1"] = kfdDevices()
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	require.NoError(t, s.reconcileCDISpecs())

	after, err := os.ReadDir(cdiRoot)
	require.NoError(t, err)
	require.Empty(t, after, "a device absent from the current inventory must not be rebuilt")
}

// Absence from the inventory is not proof the claim is dead: discovery can still be
// incomplete, and an on-demand VFIO conversion returns the device under another name.
// A spec that is already on disk keeps working, so reconcile must leave it alone.
func TestReconcileKeepsExistingSpecWhenDeviceAbsentFromInventory(t *testing.T) {
	cdiRoot, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm) // gpu-0-128 is deliberately absent from allocatable

	const claimUID = "claim-uid-1"
	require.NoError(t, s.cdi.CreateClaimSpecFile(claimUID, kfdDevices()))
	before, err := os.ReadDir(cdiRoot)
	require.NoError(t, err)
	require.NotEmpty(t, before, "the spec under test has to exist before reconcile runs")

	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims[claimUID] = kfdDevices()
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	require.NoError(t, s.reconcileCDISpecs())

	after, err := os.ReadDir(cdiRoot)
	require.NoError(t, err)
	require.Len(t, after, len(before), "an absent device must not cost the claim a spec that still resolves")
}

// A checkpoint with a nested nil device node passes the wrapper checks; reconcile
// must reject it rather than dereference it into a startup panic.
func TestReconcileRejectsNestedNullDeviceNode(t *testing.T) {
	cdiRoot, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm, "gpu-0-128")

	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims["nested"] = PreparedDevices{
		{Device: drapbv1.Device{DeviceName: "gpu-0-128"}, ContainerEdits: &cdiapi.ContainerEdits{ContainerEdits: &cdispec.ContainerEdits{DeviceNodes: []*cdispec.DeviceNode{nil}}}},
	}
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	require.NoError(t, s.reconcileCDISpecs()) // must not panic

	after, err := os.ReadDir(cdiRoot)
	require.NoError(t, err)
	require.Empty(t, after, "a nested nil device node must be rejected, not written as a spec")
}

// Reconcile never removes checkpointed state: a running kubelet does not Prepare a claim
// again, so the entry is all a later Unprepare has to go on.
func TestReconcilePreservesTheCheckpointAndRebuilds(t *testing.T) {
	cdiRoot, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm, "gpu-0-128")

	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims["claim-uid-1"] = kfdDevices()
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	require.NoError(t, s.reconcileCDISpecs())

	after, err := os.ReadDir(cdiRoot)
	require.NoError(t, err)
	require.NotEmpty(t, after, "the spec must be rebuilt")

	reloaded := newCheckpoint()
	require.NoError(t, cm.GetCheckpoint(DriverPluginCheckpointFile, reloaded))
	require.Contains(t, reloaded.V1.PreparedClaims, "claim-uid-1", "the checkpoint must be preserved")
}

// A checkpoint holding one good and one bad claim must rebuild the good claim's spec
// and skip the bad one, proving the reconcile loop is selective rather than
// all-or-nothing (and not vacuously passing because nothing is ever written).
func TestReconcileQuarantinesBadClaimButRebuildsGood(t *testing.T) {
	cdiRoot, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm, "gpu-0-128") // only the good device is allocatable

	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims["good"] = kfdDevices()
	checkpoint.V1.PreparedClaims["bad"] = PreparedDevices{nil} // malformed -> skipped
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	require.NoError(t, s.reconcileCDISpecs())

	after, err := os.ReadDir(cdiRoot)
	require.NoError(t, err)
	require.Len(t, after, 1, "exactly the good claim's spec must be rebuilt")
	data, err := os.ReadFile(filepath.Join(cdiRoot, after[0].Name()))
	require.NoError(t, err)
	require.Contains(t, string(data), "/dev/null")
}

// A CDI write error during replay is not self-healing: the kubelet may never re-Prepare
// an already-prepared claim, so reconcile must fail startup loudly instead of
// registering with a missing spec.
func TestReconcileFailsLoudOnCDIWriteError(t *testing.T) {
	cdiRoot, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm, "gpu-0-128")

	const claimUID = "claim-uid-1"
	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims[claimUID] = kfdDevices()
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	// Force WriteSpec to fail deterministically and independently of file permissions
	// (which root ignores) by planting a directory where the spec file must be written.
	specName := cdiapi.GenerateTransientSpecName(cdiVendor, cdiClass, claimUID)
	require.NoError(t, os.Mkdir(filepath.Join(cdiRoot, specName+".yaml"), 0o755))

	require.Error(t, s.reconcileCDISpecs(), "a CDI write failure during replay must fail startup, not be swallowed")
}

// staleClaim is a well-formed single-device claim whose only device node is the given
// one, for entries that no longer match the host.
func staleClaim(node cdispec.DeviceNode) PreparedDevices {
	return PreparedDevices{{
		Device:         drapbv1.Device{DeviceName: "gpu-0-128"},
		ContainerEdits: &cdiapi.ContainerEdits{ContainerEdits: &cdispec.ContainerEdits{DeviceNodes: []*cdispec.DeviceNode{&node}}},
	}}
}

// requireQuarantined checks that reconcile removed the stale claim's spec, kept its
// checkpoint entry, and that a checkpoint-hit Prepare for it fails instead of replaying.
func requireQuarantined(t *testing.T, s *DeviceState, cm checkpointmanager.CheckpointManager, cdiRoot, claimUID string) {
	t.Helper()
	after, err := os.ReadDir(cdiRoot)
	require.NoError(t, err)
	require.Empty(t, after, "a stale entry must lose its spec, not be replayed")

	reloaded := newCheckpoint()
	require.NoError(t, cm.GetCheckpoint(DriverPluginCheckpointFile, reloaded))
	require.Contains(t, reloaded.V1.PreparedClaims, claimUID, "the entry must stay for a later Unprepare")

	claim := &resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{UID: types.UID(claimUID)}}
	_, err = s.Prepare(claim)
	require.ErrorContains(t, err, "recreate the pod")
}

// A driver reload or repartition can renumber a device node within a boot, and a reboot
// can renumber all of them. A node recorded with numbers that no longer match the host
// is quarantined: its spec goes, its entry stays, and startup carries on for the rest.
func TestReconcileQuarantinesRenumberedDeviceNode(t *testing.T) {
	cdiRoot, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm, "gpu-0-128")

	node := devNull()
	node.Major++ // recorded with the wrong major
	stale := staleClaim(node)
	require.NoError(t, s.cdi.CreateClaimSpecFile("claim-uid-1", stale))
	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims["claim-uid-1"] = stale
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	require.NoError(t, s.reconcileCDISpecs(), "one stale claim must not take the plugin down")
	requireQuarantined(t, s, cm, cdiRoot, "claim-uid-1")
}

// A node whose path is gone (device removed) is quarantined the same way.
func TestReconcileQuarantinesMissingDeviceNode(t *testing.T) {
	cdiRoot, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm, "gpu-0-128")

	node := devNull()
	node.Path, node.HostPath = "/dev/does-not-exist-kfd", "/dev/does-not-exist-kfd"
	gone := staleClaim(node)
	require.NoError(t, s.cdi.CreateClaimSpecFile("claim-uid-1", gone))
	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims["claim-uid-1"] = gone
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	require.NoError(t, s.reconcileCDISpecs())
	requireQuarantined(t, s, cm, cdiRoot, "claim-uid-1")
}

// Unprepare is reached on every teardown, so a malformed entry must be dropped there
// rather than dereferenced or left to wedge the pod's deletion.
func TestUnprepareDropsMalformedEntryWithoutPanic(t *testing.T) {
	_, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm)

	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims["bad"] = PreparedDevices{nil}
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	var err error
	require.NotPanics(t, func() { err = s.Unprepare("bad") })
	require.NoError(t, err, "a malformed entry must not block teardown")

	reloaded := newCheckpoint()
	require.NoError(t, cm.GetCheckpoint(DriverPluginCheckpointFile, reloaded))
	require.NotContains(t, reloaded.V1.PreparedClaims, "bad")
}

// A device that left the inventory (removed, or renamed by an on-demand VFIO conversion
// before a restart) has nothing to restore against; the claim is still released so the
// pod can be deleted, and the host state is left to #94.
func TestUnprepareReleasesDeviceMissingFromInventory(t *testing.T) {
	_, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm) // nothing allocatable

	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims["c"] = kfdDevices()
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	require.NoError(t, s.Unprepare("c"))

	reloaded := newCheckpoint()
	require.NoError(t, cm.GetCheckpoint(DriverPluginCheckpointFile, reloaded))
	require.NotContains(t, reloaded.V1.PreparedClaims, "c", "the claim must be released")
}

// CDI allows a device node to carry only a path, and the VFIO path emits exactly that.
// Such a node is verified by path rather than skipped, so a group that no longer exists
// is quarantined like any other stale node.
func TestReconcileVerifiesPathOnlyDeviceNodes(t *testing.T) {
	cdiRoot, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm, "gpu-0-128")

	stale := staleClaim(cdispec.DeviceNode{Path: "/dev/vfio/no-such-group"})
	require.NoError(t, s.cdi.CreateClaimSpecFile("c", stale))
	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims["c"] = stale
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	require.NoError(t, s.reconcileCDISpecs())
	requireQuarantined(t, s, cm, cdiRoot, "c")
}

func TestDeviceNodesCurrent(t *testing.T) {
	current := devNull()
	renumbered := devNull()
	renumbered.Minor++
	retyped := devNull()
	retyped.Type = "b"
	gone := devNull()
	gone.Path, gone.HostPath = "/dev/does-not-exist", "/dev/does-not-exist"

	tests := map[string]struct {
		node cdispec.DeviceNode
		want string // empty means current
	}{
		"matches the host":            {node: current},
		"path only, resolves":         {node: cdispec.DeviceNode{Path: "/dev/null"}},
		"path only, gone":             {node: cdispec.DeviceNode{Path: "/dev/does-not-exist"}, want: "no longer resolves"},
		"no path at all":              {node: cdispec.DeviceNode{Type: "c", Major: 1, Minor: 3}},
		"numbers moved":               {node: renumbered, want: "recorded"},
		"type changed":                {node: retyped, want: "is type"},
		"host path gone":              {node: gone, want: "no longer resolves"},
		"host path wins over path":    {node: cdispec.DeviceNode{Path: "/dev/does-not-exist", HostPath: "/dev/null"}},
		"numbers unrecorded, resolve": {node: cdispec.DeviceNode{Path: "/dev/null", HostPath: "/dev/null", Type: current.Type}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := deviceNodesCurrent(staleClaim(tc.node))
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// writeRawCheckpoint stores a hand-built checkpoint file whose checksum matches what the
// decoded struct re-marshals to, as VerifyChecksum computes it.
func writeRawCheckpoint(t *testing.T, dir, canonical, stored string) {
	t.Helper()
	sum := checksum.New([]byte(canonical))
	require.NoError(t, os.WriteFile(filepath.Join(dir, DriverPluginCheckpointFile), fmt.Appendf(nil, stored, sum), 0o600))
}

// A checkpoint whose v1 payload or preparedClaims map is an explicit null decodes to nil.
// newCheckpoint pre-populates both, so only a file that spells the null out reaches these.
func TestLoadCheckpointHandlesExplicitNulls(t *testing.T) {
	t.Run("v1 null is rejected", func(t *testing.T) {
		dir := t.TempDir()
		cm, err := checkpointmanager.NewCheckpointManager(dir)
		require.NoError(t, err)
		writeRawCheckpoint(t, dir, `{"checksum":0}`, `{"checksum":%d,"v1":null}`)

		s := &DeviceState{checkpointManager: cm}
		_, err = s.loadCheckpoint()
		require.ErrorContains(t, err, "no v1 payload")

		_, err = s.Prepare(&resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{UID: "c"}})
		require.ErrorContains(t, err, "no v1 payload")
		require.ErrorContains(t, s.Unprepare("c"), "no v1 payload")
		require.ErrorContains(t, s.reconcileCDISpecs(), "no v1 payload")
	})

	t.Run("preparedClaims null becomes an empty map", func(t *testing.T) {
		dir := t.TempDir()
		cm, err := checkpointmanager.NewCheckpointManager(dir)
		require.NoError(t, err)
		writeRawCheckpoint(t, dir, `{"checksum":0,"v1":{}}`, `{"checksum":%d,"v1":{"preparedClaims":null}}`)

		s := &DeviceState{checkpointManager: cm}
		checkpoint, err := s.loadCheckpoint()
		require.NoError(t, err)
		require.NotNil(t, checkpoint.V1.PreparedClaims)
		require.NotPanics(t, func() { checkpoint.V1.PreparedClaims["c"] = kfdDevices() })
		require.NoError(t, s.Unprepare("c"), "an unknown claim on a null map is a no-op")
	})
}

// FuzzCheckpointEntry decodes arbitrary JSON as a checkpoint entry and drives it through the
// validation and, when that passes, the CDI spec writer. Neither may panic.
func FuzzCheckpointEntry(f *testing.F) {
	seed, err := json.Marshal(kfdDevices())
	require.NoError(f, err)
	f.Add(seed)
	f.Add([]byte(`null`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`[null]`))
	f.Add([]byte(`[{"deviceName":"gpu-0-128"}]`))
	f.Add([]byte(`[{"deviceName":"gpu-0-128","ContainerEdits":{}}]`))
	f.Add([]byte(`[{"deviceName":"gpu-0-128","ContainerEdits":{"deviceNodes":[null]}}]`))
	f.Add([]byte(`[{"deviceName":"gpu-0-128","ContainerEdits":{"deviceNodes":[{"path":"/dev/null"}],"hooks":[null]}}]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var pds PreparedDevices
		if err := json.Unmarshal(data, &pds); err != nil {
			return
		}
		if err := validatePreparedDevices("claim-uid-1", pds); err != nil {
			return
		}
		cache, err := cdiapi.NewCache(cdiapi.WithSpecDirs(t.TempDir()))
		require.NoError(t, err)
		h := &CDIHandler{cache: cache}
		// A validated entry may still be rejected by CDI's own validation; it must not panic.
		_ = h.CreateClaimSpecFile("claim-uid-1", pds)
		_ = deviceNodesCurrent(pds)
	})
}

// A checkpoint-hit Prepare that cannot write the spec must fail rather than report the
// claim as prepared with nothing on disk, the same as the startup rebuild.
func TestPrepareFailsWhenTheSpecCannotBeWritten(t *testing.T) {
	cdiRoot, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm, "gpu-0-128")

	const claimUID = "claim-uid-1"
	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims[claimUID] = kfdDevices()
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))
	specName := cdiapi.GenerateTransientSpecName(cdiVendor, cdiClass, claimUID)
	require.NoError(t, os.Mkdir(filepath.Join(cdiRoot, specName+".yaml"), 0o755))

	_, err := s.Prepare(&resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{UID: types.UID(claimUID)}})
	require.ErrorContains(t, err, "unable to ensure the CDI spec")
}

// The common spec is part of the response too, so a checkpoint hit that cannot write it fails.
func TestPrepareFailsWhenTheCommonSpecCannotBeWritten(t *testing.T) {
	cdiRoot, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm, "gpu-0-128")

	const claimUID = "claim-uid-1"
	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims[claimUID] = kfdDevices()
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))
	specName := cdiapi.GenerateTransientSpecName(cdiVendor, cdiClass, cdiCommonDeviceName)
	require.NoError(t, os.Mkdir(filepath.Join(cdiRoot, specName+".yaml"), 0o755))

	_, err := s.Prepare(&resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{UID: types.UID(claimUID)}})
	require.ErrorContains(t, err, "unable to ensure the common CDI spec")
}

// Removing a quarantined claim's spec can fail too; that is not swallowed either.
func TestReconcileFailsWhenAStaleSpecCannotBeRemoved(t *testing.T) {
	cdiRoot, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm)

	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims["bad"] = PreparedDevices{nil}
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))
	// A non-empty directory where the spec file would be cannot be removed as a file.
	specName := cdiapi.GenerateTransientSpecName(cdiVendor, cdiClass, "bad")
	dir := filepath.Join(cdiRoot, specName+".yaml")
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "keep"), nil, 0o600))

	require.ErrorContains(t, s.reconcileCDISpecs(), "unable to remove the CDI spec")
}

func TestLoadCheckpointRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	cm, err := checkpointmanager.NewCheckpointManager(dir)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, DriverPluginCheckpointFile), []byte(`{"checksum":1,"v1":{}}`), 0o600))

	s := &DeviceState{checkpointManager: cm}
	_, err = s.loadCheckpoint()
	require.ErrorContains(t, err, "unable to sync from checkpoint")
}
