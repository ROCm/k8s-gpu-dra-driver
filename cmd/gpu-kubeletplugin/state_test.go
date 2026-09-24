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
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
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

// enableVFIOPassthrough turns on the VFIOPassthrough gate for one test.
func enableVFIOPassthrough(t *testing.T) {
	t.Helper()
	require.NoError(t, featuregates.FeatureGates().Set("VFIOPassthrough=true"))
	t.Cleanup(func() {
		_ = featuregates.FeatureGates().Set("VFIOPassthrough=false")
	})
}

// vfioClaim builds an allocated claim for device carrying an opaque
// VfioDeviceConfig. iommuJSON is the raw "iommu" field, or "" to omit it.
func vfioClaim(device, iommuJSON string) *resourceapi.ResourceClaim {
	return vfioClaimDevices(iommuJSON, device)
}

// vfioClaimDevices is vfioClaim for several allocated devices, in order.
func vfioClaimDevices(iommuJSON string, devices ...string) *resourceapi.ResourceClaim {
	var results []resourceapi.DeviceRequestAllocationResult
	for _, d := range devices {
		results = append(results, resourceapi.DeviceRequestAllocationResult{
			Request: "gpu", Driver: consts.DriverName, Pool: "node", Device: d,
		})
	}
	params := `{"apiVersion":"gpu.resource.amd.com/v1alpha1","kind":"VfioDeviceConfig"`
	if iommuJSON != "" {
		params += `,"iommu":` + iommuJSON
	}
	params += `}`
	return &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{UID: "claim-uid"},
		Status: resourceapi.ResourceClaimStatus{
			Allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Results: results,
					Config: []resourceapi.DeviceAllocationConfiguration{{
						Source: resourceapi.AllocationConfigSourceClaim,
						DeviceConfiguration: resourceapi.DeviceConfiguration{
							Opaque: &resourceapi.OpaqueDeviceConfiguration{
								Driver:     consts.DriverName,
								Parameters: runtime.RawExtension{Raw: []byte(params)},
							},
						},
					}},
				},
			},
		},
	}
}

// TestPrepareDevices_IOMMUBackend drives a claim with an opaque
// VfioDeviceConfig through decode, Normalize, Validate and applyVFIOConfig for
// a pre-bound (discovery-created) VFIO device.
func TestPrepareDevices_IOMMUBackend(t *testing.T) {
	enableVFIOPassthrough(t)

	const dev = "gpu-vfio-0"
	allNodes := []string{"dev/vfio/42", "dev/vfio/vfio", "dev/vfio/devices/vfio5", "dev/iommu"}
	// setup creates a pre-bound device with a sysfs cdev entry and the given
	// /dev nodes (all of them when none are given).
	setup := func(t *testing.T, iommuFDEnabled bool, nodes ...string) (*DeviceState, string) {
		root := setupFakeVfioSysfs(t)
		createPCIDevice(t, root, "0000:0d:00.0", "vfio-pci")
		createDriverDir(t, root, "vfio-pci")
		require.NoError(t, os.MkdirAll(
			filepath.Join(root, "sys/bus/pci/devices/0000:0d:00.0/vfio-dev/vfio5"), 0755))
		if len(nodes) == 0 {
			nodes = allNodes
		}
		for _, n := range nodes {
			createDevNode(t, root, n)
		}
		state := &DeviceState{
			cdi: &CDIHandler{},
			allocatable: AllocatableDevices{
				dev: {Vfio: &AmdGpuVFIOInfo{
					PCIAddress:         "0000:0d:00.0",
					IOMMUGroup:         "42",
					preConfigureDriver: "vfio-pci",
				}},
			},
			vfioManager: &VfioPciManager{iommuFDEnabled: iommuFDEnabled},
		}
		return state, root
	}
	nodePaths := func(pd PreparedDevices) []string {
		require.Len(t, pd, 1)
		var paths []string
		for _, n := range pd[0].ContainerEdits.ContainerEdits.DeviceNodes {
			paths = append(paths, n.Path)
		}
		return paths
	}

	t.Run("no iommu field defaults to legacy", func(t *testing.T) {
		state, root := setup(t, true)
		pd, err := state.prepareDevices(vfioClaim(dev, ""))
		require.NoError(t, err)
		assert.Equal(t, []string{
			filepath.Join(root, "dev/vfio/42"), filepath.Join(root, "dev/vfio/vfio"),
		}, nodePaths(pd))
	})

	t.Run("PreferIommuFD then legacy re-prepare does not reuse cdev", func(t *testing.T) {
		state, root := setup(t, true)
		pd, err := state.prepareDevices(vfioClaim(dev, `{"backendPolicy":"PreferIommuFD"}`))
		require.NoError(t, err)
		assert.Equal(t, []string{
			filepath.Join(root, "dev/vfio/devices/vfio5"), filepath.Join(root, "dev/iommu"),
		}, nodePaths(pd))

		require.NoError(t, state.unprepareDevices("claim-uid", pd))
		assert.Equal(t, "", state.allocatable[dev].Vfio.IommuFDCdev)

		pd, err = state.prepareDevices(vfioClaim(dev, `{"backendPolicy":"LegacyOnly"}`))
		require.NoError(t, err)
		assert.Equal(t, []string{
			filepath.Join(root, "dev/vfio/42"), filepath.Join(root, "dev/vfio/vfio"),
		}, nodePaths(pd))
	})

	t.Run("PreferIommuFD falls back when host lacks IOMMUFD", func(t *testing.T) {
		state, root := setup(t, false)
		pd, err := state.prepareDevices(vfioClaim(dev, `{"backendPolicy":"PreferIommuFD"}`))
		require.NoError(t, err)
		assert.Equal(t, []string{
			filepath.Join(root, "dev/vfio/42"), filepath.Join(root, "dev/vfio/vfio"),
		}, nodePaths(pd))
	})

	t.Run("RequireIommuFD fails when host lacks IOMMUFD", func(t *testing.T) {
		state, _ := setup(t, false)
		_, err := state.prepareDevices(vfioClaim(dev, `{"backendPolicy":"RequireIommuFD"}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "IOMMUFD required")
		assert.NotNil(t, state.allocatable[dev].Vfio, "pre-bound device stays VFIO after rollback")
	})

	t.Run("PreferIommuFD falls back when cdev node is missing", func(t *testing.T) {
		state, root := setup(t, true, "dev/vfio/42", "dev/vfio/vfio", "dev/iommu")
		pd, err := state.prepareDevices(vfioClaim(dev, `{"backendPolicy":"PreferIommuFD"}`))
		require.NoError(t, err)
		assert.Equal(t, []string{
			filepath.Join(root, "dev/vfio/42"), filepath.Join(root, "dev/vfio/vfio"),
		}, nodePaths(pd))
	})

	t.Run("RequireIommuFD fails when cdev node is missing", func(t *testing.T) {
		state, _ := setup(t, true, "dev/vfio/42", "dev/vfio/vfio", "dev/iommu")
		_, err := state.prepareDevices(vfioClaim(dev, `{"backendPolicy":"RequireIommuFD"}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cdev node")
	})

	t.Run("LegacyOnly fails when /dev/vfio/vfio is missing", func(t *testing.T) {
		state, _ := setup(t, true, "dev/vfio/42")
		_, err := state.prepareDevices(vfioClaim(dev, ""))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "common VFIO CDI edits")
	})

	t.Run("invalid policy is rejected by Validate", func(t *testing.T) {
		state, _ := setup(t, true)
		_, err := state.prepareDevices(vfioClaim(dev, `{"backendPolicy":"Bogus"}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "error validating VFIO config")
	})

	t.Run("unknown iommu field is rejected by strict decoding", func(t *testing.T) {
		state, _ := setup(t, true)
		_, err := state.prepareDevices(vfioClaim(dev, `{"backendPolicy":"LegacyOnly","enableAPIDevice":true}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "error getting opaque device configs")
	})
}

// setupConvertibleGPU creates a regular amdgpu-bound GPU in a fake sysfs, with
// an IOMMU group, a vfio cdev entry that appears after bind, and the given
// /dev nodes. It returns a DeviceState that lists the device as an AmdGpu, so
// a VfioDeviceConfig claim converts it to VFIO during prepare.
func setupConvertibleGPU(t *testing.T, iommuFDEnabled bool, nodes ...string) (*DeviceState, string) {
	t.Helper()
	root := setupFakeVfioSysfs(t)
	const addr = "0000:0d:00.0"
	createPCIDevice(t, root, addr, "amdgpu")
	createDriverDir(t, root, "amdgpu")
	createDriverDir(t, root, "vfio-pci")
	devDir := filepath.Join(root, "sys/bus/pci/devices", addr)
	require.NoError(t, os.Symlink("../../../kernel/iommu_groups/42", filepath.Join(devDir, "iommu_group")))
	require.NoError(t, os.MkdirAll(filepath.Join(devDir, "vfio-dev/vfio5"), 0755))
	for _, n := range nodes {
		createDevNode(t, root, n)
	}
	state := &DeviceState{
		allocatable: AllocatableDevices{
			"gpu-0-128": {AmdGpu: &AmdGpuInfo{PCIAddress: addr, cardIndex: 0, renderIndex: 128}},
		},
		vfioManager: &VfioPciManager{iommuFDEnabled: iommuFDEnabled},
	}
	return state, root
}

// assertRestoredGPU checks that a converted device is back to a plain AmdGpu
// and that no conversion is left recorded for the claim.
func assertRestoredGPU(t *testing.T, state *DeviceState, name string) {
	t.Helper()
	dev := state.allocatable[name]
	require.NotNil(t, dev)
	assert.NotNil(t, dev.AmdGpu, "device should be restored to AmdGpu")
	assert.Nil(t, dev.Vfio, "device should not remain VFIO")
	assert.Equal(t, consts.AmdGpuDeviceType, dev.Type())
	assert.Empty(t, state.claimVfioConversions)
}

// TestPrepareDevices_ConvertedGPU covers claims that convert a regular GPU to
// VFIO, including every failure path that must undo the conversion.
func TestPrepareDevices_ConvertedGPU(t *testing.T) {
	enableVFIOPassthrough(t)
	allNodes := []string{"dev/vfio/42", "dev/vfio/vfio", "dev/vfio/devices/vfio5", "dev/iommu"}

	t.Run("RequireIommuFD succeeds and unprepare restores the GPU", func(t *testing.T) {
		state, root := setupConvertibleGPU(t, true, allNodes...)
		state.cdi = &CDIHandler{}

		pd, err := state.prepareDevices(vfioClaim("gpu-0-128", `{"backendPolicy":"RequireIommuFD"}`))
		require.NoError(t, err)
		require.Len(t, pd, 1)
		var paths []string
		for _, n := range pd[0].ContainerEdits.ContainerEdits.DeviceNodes {
			paths = append(paths, n.Path)
		}
		assert.Equal(t, []string{
			filepath.Join(root, "dev/vfio/devices/vfio5"), filepath.Join(root, "dev/iommu"),
		}, paths)
		assert.Equal(t, consts.VfioDeviceType, state.allocatable["gpu-0-128"].Type())
		bound, err := os.ReadFile(filepath.Join(root, "sys/bus/pci/drivers/vfio-pci/bind"))
		require.NoError(t, err)
		assert.Equal(t, "0000:0d:00.0", string(bound))

		require.NoError(t, state.unprepareDevices("claim-uid", pd))
		assertRestoredGPU(t, state, "gpu-0-128")
	})

	t.Run("invalid policy restores the GPU without binding it", func(t *testing.T) {
		state, root := setupConvertibleGPU(t, true, allNodes...)

		_, err := state.prepareDevices(vfioClaim("gpu-0-128", `{"backendPolicy":"Bogus"}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "error validating VFIO config")
		assertRestoredGPU(t, state, "gpu-0-128")
		bound, err := os.ReadFile(filepath.Join(root, "sys/bus/pci/drivers/vfio-pci/bind"))
		require.NoError(t, err)
		assert.Empty(t, bound, "device must not be bound to vfio-pci")
	})

	t.Run("RequireIommuFD failure after bind restores the GPU", func(t *testing.T) {
		state, _ := setupConvertibleGPU(t, false, allNodes...)

		_, err := state.prepareDevices(vfioClaim("gpu-0-128", `{"backendPolicy":"RequireIommuFD"}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "IOMMUFD required")
		assertRestoredGPU(t, state, "gpu-0-128")
	})

	t.Run("unallocatable later result restores an earlier conversion", func(t *testing.T) {
		state, _ := setupConvertibleGPU(t, true, allNodes...)

		_, err := state.prepareDevices(vfioClaimDevices("", "gpu-0-128", "no-such-device"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not allocatable")
		assertRestoredGPU(t, state, "gpu-0-128")
	})
}

// newLifecycleState returns a converted-GPU DeviceState wired to a real CDI
// handler and checkpoint manager in temp dirs, as NewDeviceState would.
func newLifecycleState(t *testing.T, iommuFDEnabled bool, nodes ...string) (*DeviceState, string, string) {
	t.Helper()
	state, root := setupConvertibleGPU(t, iommuFDEnabled, nodes...)

	cdiRoot := t.TempDir()
	cdi, err := NewCDIHandler(&Config{flags: &Flags{cdiRoot: cdiRoot, nodeName: "node"}})
	require.NoError(t, err)
	state.cdi = cdi

	cm, err := checkpointmanager.NewCheckpointManager(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, newCheckpoint()))
	state.checkpointManager = cm
	return state, root, cdiRoot
}

func claimSpecFiles(t *testing.T, cdiRoot string) []string {
	t.Helper()
	entries, err := os.ReadDir(cdiRoot)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestPrepareUnprepare_VfioLifecycle drives Prepare and Unprepare end to end:
// claim decode, GPU->VFIO conversion, CDI spec file, checkpoint, idempotent
// re-prepare, and cleanup.
func TestPrepareUnprepare_VfioLifecycle(t *testing.T) {
	enableVFIOPassthrough(t)
	state, root, cdiRoot := newLifecycleState(t, true,
		"dev/vfio/42", "dev/vfio/vfio", "dev/vfio/devices/vfio5", "dev/iommu")
	claim := vfioClaim("gpu-0-128", `{"backendPolicy":"RequireIommuFD"}`)

	devices, err := state.Prepare(claim)
	require.NoError(t, err)
	require.Len(t, devices, 1)
	assert.Equal(t, "gpu-0-128", devices[0].DeviceName)

	files := claimSpecFiles(t, cdiRoot)
	require.Len(t, files, 1)
	spec, err := cdiapi.ReadSpec(filepath.Join(cdiRoot, files[0]), 0)
	require.NoError(t, err)
	require.Len(t, spec.Devices, 1)
	var paths []string
	for _, n := range spec.Devices[0].ContainerEdits.DeviceNodes {
		paths = append(paths, n.Path)
	}
	assert.Equal(t, []string{
		filepath.Join(root, "dev/vfio/devices/vfio5"), filepath.Join(root, "dev/iommu"),
	}, paths)

	cp := newCheckpoint()
	require.NoError(t, state.checkpointManager.GetCheckpoint(DriverPluginCheckpointFile, cp))
	assert.Contains(t, cp.V1.PreparedClaims, "claim-uid")

	// A repeated Prepare is served from the checkpoint.
	again, err := state.Prepare(claim)
	require.NoError(t, err)
	require.Len(t, again, 1)
	assert.Equal(t, devices[0].DeviceName, again[0].DeviceName)
	assert.Equal(t, devices[0].PoolName, again[0].PoolName)
	assert.Equal(t, devices[0].RequestNames, again[0].RequestNames)
	assert.Equal(t, devices[0].CdiDeviceIds, again[0].CdiDeviceIds)

	require.NoError(t, state.Unprepare("claim-uid"))
	assert.Empty(t, claimSpecFiles(t, cdiRoot))
	cp = newCheckpoint()
	require.NoError(t, state.checkpointManager.GetCheckpoint(DriverPluginCheckpointFile, cp))
	assert.NotContains(t, cp.V1.PreparedClaims, "claim-uid")
	assertRestoredGPU(t, state, "gpu-0-128")
}

// TestPrepare_CDIWriteFailureRestoresGPU checks that a failure after
// prepareDevices succeeds still undoes the GPU->VFIO conversion.
func TestPrepare_CDIWriteFailureRestoresGPU(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions are not enforced for root")
	}
	enableVFIOPassthrough(t)
	state, _, cdiRoot := newLifecycleState(t, true,
		"dev/vfio/42", "dev/vfio/vfio", "dev/vfio/devices/vfio5", "dev/iommu")
	require.NoError(t, os.Chmod(cdiRoot, 0500))
	t.Cleanup(func() { _ = os.Chmod(cdiRoot, 0700) })

	_, err := state.Prepare(vfioClaim("gpu-0-128", `{"backendPolicy":"PreferIommuFD"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CDI spec file")
	assertRestoredGPU(t, state, "gpu-0-128")
}
