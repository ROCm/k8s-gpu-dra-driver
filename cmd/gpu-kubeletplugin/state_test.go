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

func newCacheAndCheckpointer(t *testing.T) (string, *cdiapi.Cache, checkpointmanager.CheckpointManager) {
	t.Helper()
	cdiRoot := t.TempDir()
	cache, err := cdiapi.NewCache(cdiapi.WithSpecDirs(cdiRoot))
	require.NoError(t, err)
	cm, err := checkpointmanager.NewCheckpointManager(t.TempDir())
	require.NoError(t, err)
	return cdiRoot, cache, cm
}

// testDeviceState wires a DeviceState with a temp plugin dir, a matching boot epoch,
// and the named allocatable devices, so reconcile replays instead of discarding.
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
		pluginPath:        t.TempDir(),
		bootID:            "test-boot",
	}
	require.NoError(t, s.writeEpoch("test-boot"))
	return s
}

// kfdDevices is a well-formed single-device claim naming gpu-0-128. Its node points at
// /dev/null (a real char device, major 1 minor 3 on Linux) so the reconcile device-node
// verification sees numbers that match the host in tests that expect a replay.
func kfdDevices() PreparedDevices {
	return PreparedDevices{
		{
			Device: drapbv1.Device{DeviceName: "gpu-0-128"},
			ContainerEdits: &cdiapi.ContainerEdits{ContainerEdits: &cdispec.ContainerEdits{
				DeviceNodes: []*cdispec.DeviceNode{
					{Path: "/dev/null", HostPath: "/dev/null", Type: "c", Major: 1, Minor: 3, Permissions: "rw"},
				},
			}},
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
	edits := &cdiapi.ContainerEdits{ContainerEdits: &cdispec.ContainerEdits{}}
	valid := PreparedDevices{{Device: drapbv1.Device{DeviceName: "gpu-0-128"}, ContainerEdits: edits}}
	require.NoError(t, validatePreparedDevices("uid", valid))

	nilNode := &cdiapi.ContainerEdits{ContainerEdits: &cdispec.ContainerEdits{DeviceNodes: []*cdispec.DeviceNode{nil}}}
	nilHook := &cdiapi.ContainerEdits{ContainerEdits: &cdispec.ContainerEdits{Hooks: []*cdispec.Hook{nil}}}
	nilMount := &cdiapi.ContainerEdits{ContainerEdits: &cdispec.ContainerEdits{Mounts: []*cdispec.Mount{nil}}}
	nilNet := &cdiapi.ContainerEdits{ContainerEdits: &cdispec.ContainerEdits{NetDevices: []*cdispec.LinuxNetDevice{nil}}}

	bad := map[string]PreparedDevices{
		"empty":               {},
		"nil device":          {nil},
		"no device name":      {{Device: drapbv1.Device{}, ContainerEdits: edits}},
		"nil container edits": {{Device: drapbv1.Device{DeviceName: "gpu-0-128"}}},
		"nil deviceNode":      {{Device: drapbv1.Device{DeviceName: "gpu-0-128"}, ContainerEdits: nilNode}},
		"nil hook":            {{Device: drapbv1.Device{DeviceName: "gpu-0-128"}, ContainerEdits: nilHook}},
		"nil mount":           {{Device: drapbv1.Device{DeviceName: "gpu-0-128"}, ContainerEdits: nilMount}},
		"nil netDevice":       {{Device: drapbv1.Device{DeviceName: "gpu-0-128"}, ContainerEdits: nilNet}},
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
	require.Error(t, err, "checkpoint-hit Prepare with a malformed entry must fail closed, not report success")
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

// A checkpoint written under a different boot must be discarded, not replayed: its
// device nodes may now point at other hardware.
func TestReconcileDiscardsCheckpointFromDifferentBoot(t *testing.T) {
	cdiRoot, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm, "gpu-0-128")
	require.NoError(t, s.writeEpoch("boot-previous")) // stored epoch now differs from s.bootID

	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims["claim-uid-1"] = kfdDevices()
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	require.NoError(t, s.reconcileCDISpecs())

	after, err := os.ReadDir(cdiRoot)
	require.NoError(t, err)
	require.Empty(t, after, "a checkpoint from a previous boot must not be replayed into a CDI spec")

	reloaded := newCheckpoint()
	require.NoError(t, cm.GetCheckpoint(DriverPluginCheckpointFile, reloaded))
	require.Empty(t, reloaded.V1.PreparedClaims, "the stale checkpoint must be discarded")
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

// The first restart after the boot-epoch field is introduced has no recorded epoch, and
// a plugin-only upgrade keeps the same kubelet running. Because kubelet still holds the
// claims, reconcile must NOT discard on a missing or unreadable epoch: deleting the specs
// would strand claims kubelet will not re-Prepare. It rebuilds instead when the device
// nodes still match, so the migration is seamless.
func TestReconcileRebuildsWithoutConfirmedRebootWhenNodesMatch(t *testing.T) {
	t.Run("epoch file missing", func(t *testing.T) {
		cdiRoot, cache, cm := newCacheAndCheckpointer(t)
		s := &DeviceState{
			cdi:               &CDIHandler{cache: cache},
			allocatable:       AllocatableDevices{"gpu-0-128": &AllocatableDevice{}},
			checkpointManager: cm,
			pluginPath:        t.TempDir(), // no boot-epoch file written -> stored == ""
			bootID:            "test-boot",
		}
		checkpoint := newCheckpoint()
		checkpoint.V1.PreparedClaims["claim-uid-1"] = kfdDevices()
		require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

		require.NoError(t, s.reconcileCDISpecs())

		after, err := os.ReadDir(cdiRoot)
		require.NoError(t, err)
		require.NotEmpty(t, after, "a missing epoch must rebuild, not discard, while kubelet still holds the claim")

		reloaded := newCheckpoint()
		require.NoError(t, cm.GetCheckpoint(DriverPluginCheckpointFile, reloaded))
		require.NotEmpty(t, reloaded.V1.PreparedClaims, "the checkpoint must be preserved, not discarded")
	})

	t.Run("boot id unreadable", func(t *testing.T) {
		cdiRoot, cache, cm := newCacheAndCheckpointer(t)
		s := &DeviceState{
			cdi:               &CDIHandler{cache: cache},
			allocatable:       AllocatableDevices{"gpu-0-128": &AllocatableDevice{}},
			checkpointManager: cm,
			pluginPath:        t.TempDir(),
			bootID:            "", // procfs unreadable
		}
		require.NoError(t, s.writeEpoch("")) // stored epoch also empty
		checkpoint := newCheckpoint()
		checkpoint.V1.PreparedClaims["claim-uid-1"] = kfdDevices()
		require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

		require.NoError(t, s.reconcileCDISpecs())

		after, err := os.ReadDir(cdiRoot)
		require.NoError(t, err)
		require.NotEmpty(t, after, "an unreadable boot id must rebuild, not discard, while kubelet still holds the claim")
	})
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

// A cross-boot discard must also delete any stale CDI spec files that survived on a
// persistent spec directory, so the container runtime cannot resolve a claim against a
// spec that now points at different hardware.
func TestReconcileDiscardDeletesStaleSpecs(t *testing.T) {
	cdiRoot, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm, "gpu-0-128")

	const claimUID = "claim-uid-1"
	// A spec left on disk by a previous boot.
	require.NoError(t, s.cdi.CreateClaimSpecFile(claimUID, kfdDevices()))
	before, err := os.ReadDir(cdiRoot)
	require.NoError(t, err)
	require.NotEmpty(t, before)

	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims[claimUID] = kfdDevices()
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	require.NoError(t, s.writeEpoch("boot-previous")) // force a cross-boot discard

	require.NoError(t, s.reconcileCDISpecs())

	after, err := os.ReadDir(cdiRoot)
	require.NoError(t, err)
	require.Empty(t, after, "a cross-boot discard must delete the stale CDI spec, not leave it on disk")
}

// A same-boot driver reload or repartition can renumber a device node without changing
// the boot id. A checkpointed node whose major/minor no longer match the host is stale,
// so reconcile discards it (and its specs) rather than replaying the wrong numbers.
// Same boot (epoch matches), so a renumbered node is not a reboot. kubelet still holds the
// claim, so reconcile must fail startup rather than discard the spec it relies on.
func TestReconcileFailsStartupOnDeviceNodeRenumber(t *testing.T) {
	cdiRoot, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm, "gpu-0-128") // same boot: epoch matches, inventory has the device

	// A node at /dev/null (major 1, minor 3) but recorded with the wrong major, as if the
	// device was renumbered after the checkpoint was written.
	stale := PreparedDevices{{
		Device: drapbv1.Device{DeviceName: "gpu-0-128"},
		ContainerEdits: &cdiapi.ContainerEdits{ContainerEdits: &cdispec.ContainerEdits{
			DeviceNodes: []*cdispec.DeviceNode{
				{Path: "/dev/null", HostPath: "/dev/null", Type: "c", Major: 99, Minor: 3, Permissions: "rw"},
			},
		}},
	}}
	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims["claim-uid-1"] = stale
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	require.Error(t, s.reconcileCDISpecs(), "a same-boot renumber must fail startup, not discard")

	after, err := os.ReadDir(cdiRoot)
	require.NoError(t, err)
	require.Empty(t, after, "a checkpointed node whose device numbers moved must not be replayed")

	reloaded := newCheckpoint()
	require.NoError(t, cm.GetCheckpoint(DriverPluginCheckpointFile, reloaded))
	require.NotEmpty(t, reloaded.V1.PreparedClaims, "startup must fail without discarding the checkpoint kubelet still relies on")
}

// A checkpointed node whose HostPath no longer exists (device removed) is stale too, and
// without a confirmed reboot reconcile must fail startup rather than discard it.
func TestReconcileFailsStartupOnMissingDeviceNode(t *testing.T) {
	cdiRoot, cache, cm := newCacheAndCheckpointer(t)
	s := testDeviceState(t, cache, cm, "gpu-0-128")

	gone := PreparedDevices{{
		Device: drapbv1.Device{DeviceName: "gpu-0-128"},
		ContainerEdits: &cdiapi.ContainerEdits{ContainerEdits: &cdispec.ContainerEdits{
			DeviceNodes: []*cdispec.DeviceNode{
				{Path: "/dev/does-not-exist-kfd", HostPath: "/dev/does-not-exist-kfd", Type: "c", Major: 1, Minor: 3, Permissions: "rw"},
			},
		}},
	}}
	checkpoint := newCheckpoint()
	checkpoint.V1.PreparedClaims["claim-uid-1"] = gone
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint))

	require.Error(t, s.reconcileCDISpecs(), "a missing node without a confirmed reboot must fail startup")

	after, err := os.ReadDir(cdiRoot)
	require.NoError(t, err)
	require.Empty(t, after, "a checkpointed node whose device path is gone must not be replayed")

	reloaded := newCheckpoint()
	require.NoError(t, cm.GetCheckpoint(DriverPluginCheckpointFile, reloaded))
	require.NotEmpty(t, reloaded.V1.PreparedClaims, "startup must fail without discarding the checkpoint")
}
