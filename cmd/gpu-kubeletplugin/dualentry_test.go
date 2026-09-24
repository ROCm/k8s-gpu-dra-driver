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
	"os"
	"path/filepath"
	"testing"

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/consts"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/featuregates"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
)

const siblingPCI = "0000:0a:00.0"

// setVFIOPassthrough sets the VFIOPassthrough gate for one test.
func setVFIOPassthrough(t *testing.T, enabled bool) {
	t.Helper()
	val := "false"
	if enabled {
		val = "true"
	}
	require.NoError(t, featuregates.FeatureGates().Set("VFIOPassthrough="+val))
	t.Cleanup(func() { _ = featuregates.FeatureGates().Set("VFIOPassthrough=false") })
}

// createDevNode creates a fake device node at root/rel as a symlink to
// /dev/null, which os.Stat resolves to a real character device.
func createDevNode(t *testing.T, root, rel string) {
	t.Helper()
	path := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.Symlink("/dev/null", path))
}

// setPCIDriver repoints a fake device's driver symlink, standing in for the
// kernel's bind state, which the fake sysfs bind/unbind files do not change.
func setPCIDriver(t *testing.T, root, pciAddr, driver string) {
	t.Helper()
	link := filepath.Join(root, "sys/bus/pci/devices", pciAddr, "driver")
	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Symlink("../../../../bus/pci/drivers/"+driver, link))
}

// newDualEntryState returns a DeviceState for one amdgpu-bound compute GPU
// and its type=vfio sibling, as discovery builds them, wired to a real CDI
// handler and checkpoint manager in temp dirs.
func newDualEntryState(t *testing.T) (*DeviceState, string, string) {
	t.Helper()
	root := setupFakeVfioSysfs(t)
	createPCIDevice(t, root, siblingPCI, consts.AMDGPUDriverName)
	createDriverDir(t, root, consts.AMDGPUDriverName)
	createDriverDir(t, root, consts.VFIODriverName)
	require.NoError(t, os.Symlink("../../../kernel/iommu_groups/42",
		filepath.Join(root, "sys/bus/pci/devices", siblingPCI, "iommu_group")))
	createDevNode(t, root, "dev/vfio/42")
	createDevNode(t, root, "dev/vfio/vfio")

	compute := &AllocatableDevice{AmdGpu: &AmdGpuInfo{PCIAddress: siblingPCI, cardIndex: 0, renderIndex: 128}}
	sibling := newVFIOSibling(compute.AmdGpu, 0, 0)

	cdiRoot := t.TempDir()
	cdi, err := NewCDIHandler(&Config{flags: &Flags{cdiRoot: cdiRoot, nodeName: "node"}})
	require.NoError(t, err)
	cm, err := checkpointmanager.NewCheckpointManager(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, cm.CreateCheckpoint(DriverPluginCheckpointFile, newCheckpoint()))

	state := &DeviceState{
		cdi:               cdi,
		checkpointManager: cm,
		vfioManager:       &VfioPciManager{},
		nodeName:          "node",
		siblingCache:      make(AllocatableDevices),
		allocatable: AllocatableDevices{
			"gpu-0-128":  compute,
			"gpu-vfio-0": sibling,
		},
	}
	state.buildPCIIndex()
	return state, root, cdiRoot
}

// directClaim allocates device with no opaque config at all.
func directClaim(device string) *resourceapi.ResourceClaim {
	return &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{UID: "claim-uid"},
		Status: resourceapi.ResourceClaimStatus{
			Allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Results: []resourceapi.DeviceRequestAllocationResult{
						{Request: "gpu", Driver: consts.DriverName, Pool: "node", Device: device},
					},
				},
			},
		},
	}
}

func TestNewVFIOSibling(t *testing.T) {
	root := setupFakeVfioSysfs(t)
	createPCIDevice(t, root, siblingPCI, consts.AMDGPUDriverName)
	require.NoError(t, os.Symlink("../../../kernel/iommu_groups/42",
		filepath.Join(root, "sys/bus/pci/devices", siblingPCI, "iommu_group")))

	gpu := &AmdGpuInfo{
		PCIAddress: siblingPCI, DeviceID: "0x74a1", ProductName: "MI300X", NumaNode: 1,
		ParentPFAddress: siblingPCI, TotalVFs: 8, MemoryBytes: 192 << 30, ComputeUnits: 304, SimdUnits: 1216,
	}
	dev := newVFIOSibling(gpu, 3, 4)
	require.NotNil(t, dev.Vfio)
	v := dev.Vfio
	assert.Equal(t, consts.AMDGPUDriverName, v.preConfigureDriver, "release must rebind the GPU to amdgpu")
	assert.Equal(t, "gpu-vfio-3", dev.CanonicalName())
	assert.Equal(t, "42", v.IOMMUGroup)
	assert.Equal(t, consts.AMDVendorID, v.VendorID)
	assert.Equal(t, 4, v.NumVFs)
	assert.Equal(t, gpu.TotalVFs, v.TotalVFs)
	assert.Equal(t, gpu.ParentPFAddress, v.ParentPFAddress)
	assert.Equal(t, gpu.MemoryBytes, v.MemoryBytes)
	assert.Equal(t, gpu.ComputeUnits, v.ComputeUnits)
	assert.Equal(t, gpu.SimdUnits, v.SimdUnits)
}

// TestPrepareUnprepare_DirectVFIOSibling claims a GPU's type=vfio sibling
// directly, with no VfioDeviceConfig, as docs/installation.md documents. Prepare
// must bind it and return its VFIO CDI device, and withdraw the compute entry.
// Unprepare must rebind the GPU to amdgpu, not leave it unbound, and restore
// the compute entry.
func TestPrepareUnprepare_DirectVFIOSibling(t *testing.T) {
	setVFIOPassthrough(t, true)
	state, root, cdiRoot := newDualEntryState(t)

	devices, siblingChanged, err := state.Prepare(directClaim("gpu-vfio-0"))
	require.NoError(t, err)
	require.Len(t, devices, 1, "a direct type=vfio claim must prepare its device")
	assert.Equal(t, "gpu-vfio-0", devices[0].DeviceName)
	assert.True(t, siblingChanged)
	assert.NotContains(t, state.allocatable, "gpu-0-128", "compute sibling is withdrawn")

	bound, err := os.ReadFile(filepath.Join(root, "sys/bus/pci/drivers/vfio-pci/bind"))
	require.NoError(t, err)
	assert.Equal(t, siblingPCI, string(bound), "Prepare binds the GPU to vfio-pci")

	entries, err := os.ReadDir(cdiRoot)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	spec, err := cdiapi.ReadSpec(filepath.Join(cdiRoot, entries[0].Name()), 0)
	require.NoError(t, err)
	require.Len(t, spec.Devices, 1)
	var paths []string
	for _, n := range spec.Devices[0].ContainerEdits.DeviceNodes {
		paths = append(paths, n.Path)
	}
	assert.Equal(t, []string{filepath.Join(root, "dev/vfio/42"), filepath.Join(root, "dev/vfio/vfio")}, paths)

	// The kernel now has the GPU on vfio-pci.
	setPCIDriver(t, root, siblingPCI, consts.VFIODriverName)

	siblingChanged, err = state.Unprepare("claim-uid")
	require.NoError(t, err)
	assert.True(t, siblingChanged)
	rebound, err := os.ReadFile(filepath.Join(root, "sys/bus/pci/drivers/amdgpu/bind"))
	require.NoError(t, err)
	assert.Equal(t, siblingPCI, string(rebound), "release must rebind the GPU to amdgpu")
	assert.Contains(t, state.allocatable, "gpu-0-128", "compute sibling is restored")
	assert.Contains(t, state.allocatable, "gpu-vfio-0")
}

func TestPrepareDevices_VFIODeviceWithGateDisabled(t *testing.T) {
	setVFIOPassthrough(t, false)
	state, _, _ := newDualEntryState(t)

	_, err := state.prepareDevices(directClaim("gpu-vfio-0"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VFIOPassthrough")
}
