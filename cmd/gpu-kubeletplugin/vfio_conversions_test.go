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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
)

const convertAddr = "0000:0d:00.0"

var allVfioNodes = []string{"dev/vfio/42", "dev/vfio/vfio", "dev/vfio/devices/vfio5", "dev/iommu"}

// setPCIDriver repoints a fake device's driver symlink, standing in for the
// kernel's bind state, which the fake sysfs bind/unbind files do not change.
func setPCIDriver(t *testing.T, root, pciAddr, driver string) {
	t.Helper()
	link := filepath.Join(root, "sys/bus/pci/devices", pciAddr, "driver")
	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Symlink("../../../../bus/pci/drivers/"+driver, link))
}

// breakVfioUnbind makes unbinding from vfio-pci fail until the returned func
// is called, simulating a device the kernel will not release.
func breakVfioUnbind(t *testing.T, root string) (fix func()) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("file permissions are not enforced for root")
	}
	unbind := filepath.Join(root, "sys/bus/pci/drivers/vfio-pci/unbind")
	require.NoError(t, os.Chmod(unbind, 0400))
	fix = func() { require.NoError(t, os.Chmod(unbind, 0600)) }
	t.Cleanup(func() { _ = os.Chmod(unbind, 0600) })
	return fix
}

// assertPublished checks the ResourceSlice devices built from allocatable:
// exactly the expected names, each with the expected "type" attribute.
func assertPublished(t *testing.T, allocatable AllocatableDevices, want map[string]string) {
	t.Helper()
	got := make(map[string]string)
	for _, d := range resourceSliceDevices(allocatable) {
		_, dup := got[d.Name]
		assert.False(t, dup, "duplicate device name %q in ResourceSlice", d.Name)
		got[d.Name] = *d.Attributes["type"].StringValue
	}
	assert.Equal(t, want, got)
}

func readCheckpoint(t *testing.T, state *DeviceState) *Checkpoint {
	t.Helper()
	cp := newCheckpoint()
	require.NoError(t, state.checkpointManager.GetCheckpoint(DriverPluginCheckpointFile, cp))
	return cp
}

// assertStrandedVFIO checks that a device whose rebind failed is still listed
// as VFIO and its conversion is still recorded for the claim.
func assertStrandedVFIO(t *testing.T, state *DeviceState, claimUID, name string) {
	t.Helper()
	dev := state.allocatable[name]
	require.NotNil(t, dev)
	assert.NotNil(t, dev.Vfio, "device whose rebind failed must stay VFIO")
	assert.Nil(t, dev.AmdGpu)
	assert.Contains(t, state.vfioConversions[claimUID], name)
}

// TestPrepare_FailedRebindKeepsDeviceConverted covers a rollback whose rebind
// fails: the device stays VFIO, the error is surfaced and persisted, and a
// retried Prepare finishes the rebind before converting again.
func TestPrepare_FailedRebindKeepsDeviceConverted(t *testing.T) {
	enableVFIOPassthrough(t)
	state, root, _ := newLifecycleState(t, false, allVfioNodes...)
	// The device is on vfio-pci once Configure runs; start it there so
	// rollback has to unbind it.
	setPCIDriver(t, root, convertAddr, "vfio-pci")
	fix := breakVfioUnbind(t, root)
	claim := vfioClaim("gpu-0-128", `{"backendPolicy":"RequireIommuFD"}`)

	_, err := state.Prepare(claim)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "IOMMUFD required")
	assert.Contains(t, err.Error(), "VFIO rollback incomplete")
	assertStrandedVFIO(t, state, "claim-uid", "gpu-0-128")

	cp := readCheckpoint(t, state)
	assert.NotContains(t, cp.V1.PreparedClaims, "claim-uid")
	require.Contains(t, cp.V1.VfioConversions, "claim-uid")
	assert.Equal(t, convertAddr, cp.V1.VfioConversions["claim-uid"]["gpu-0-128"].PCIAddress)

	// While the rebind keeps failing, a retry refuses to go further.
	_, err = state.Prepare(claim)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "previous failed prepare")
	assertStrandedVFIO(t, state, "claim-uid", "gpu-0-128")

	// Once the device can be released and IOMMUFD is available, the retry
	// finishes the rebind and then prepares the claim normally.
	fix()
	state.vfioManager.iommuFDEnabled = true
	devices, err := state.Prepare(claim)
	require.NoError(t, err)
	require.Len(t, devices, 1)
	bound, err := os.ReadFile(filepath.Join(root, "sys/bus/pci/drivers/amdgpu/bind"))
	require.NoError(t, err)
	assert.Equal(t, convertAddr, string(bound), "stranded device should have been rebound to amdgpu")
	assert.Contains(t, state.vfioConversions["claim-uid"], "gpu-0-128", "retry converts the GPU again")
}

// TestUnprepare_ReturnsStrandedDevice covers kubelet giving up on a claim whose
// Prepare failed with an incomplete rollback: Unprepare returns the device.
func TestUnprepare_ReturnsStrandedDevice(t *testing.T) {
	enableVFIOPassthrough(t)
	state, root, _ := newLifecycleState(t, false, allVfioNodes...)
	setPCIDriver(t, root, convertAddr, "vfio-pci")
	fix := breakVfioUnbind(t, root)

	_, err := state.Prepare(vfioClaim("gpu-0-128", `{"backendPolicy":"RequireIommuFD"}`))
	require.Error(t, err)
	assertStrandedVFIO(t, state, "claim-uid", "gpu-0-128")

	// Unprepare surfaces the failure while the device is still stuck...
	err = state.Unprepare("claim-uid")
	require.Error(t, err)
	assertStrandedVFIO(t, state, "claim-uid", "gpu-0-128")

	// ...and returns it once it can.
	fix()
	require.NoError(t, state.Unprepare("claim-uid"))
	assertRestoredGPU(t, state, "gpu-0-128")
	assert.Empty(t, readCheckpoint(t, state).V1.VfioConversions)
}

// TestVfioConversions_PerClaim checks that preparing a second claim does not
// forget the first claim's conversion, so unpreparing the first still
// restores its GPU.
func TestVfioConversions_PerClaim(t *testing.T) {
	enableVFIOPassthrough(t)
	state, root := setupConvertibleGPU(t, true, allVfioNodes...)
	state.cdi = &CDIHandler{}
	const addrB = "0000:0e:00.0"
	createPCIDevice(t, root, addrB, "amdgpu")
	require.NoError(t, os.Symlink("../../../kernel/iommu_groups/43",
		filepath.Join(root, "sys/bus/pci/devices", addrB, "iommu_group")))
	createDevNode(t, root, "dev/vfio/43")
	state.allocatable["gpu-1-129"] = &AllocatableDevice{
		AmdGpu: &AmdGpuInfo{PCIAddress: addrB, cardIndex: 1, renderIndex: 129},
	}

	claimA := vfioClaim("gpu-0-128", "")
	claimA.UID = types.UID("claim-a")
	claimB := vfioClaim("gpu-1-129", "")
	claimB.UID = types.UID("claim-b")

	pdA, err := state.prepareDevices(claimA)
	require.NoError(t, err)
	_, err = state.prepareDevices(claimB)
	require.NoError(t, err)

	require.NoError(t, state.unprepareDevices("claim-a", pdA))
	assert.NotNil(t, state.allocatable["gpu-0-128"].AmdGpu, "claim A's GPU must be restored")
	assert.NotNil(t, state.allocatable["gpu-1-129"].Vfio, "claim B's GPU stays converted")
	assert.NotContains(t, state.vfioConversions, "claim-a")
	assert.Contains(t, state.vfioConversions["claim-b"], "gpu-1-129")
}

// TestVfioConversions_RecoveredAfterRestart simulates a plugin restart while a
// claim holds a converted GPU: discovery lists the vfio-pci-bound GPU as a
// pre-bound VFIO device, recovery replaces that with the converted entry, and
// Unprepare rebinds the GPU to amdgpu.
func TestVfioConversions_RecoveredAfterRestart(t *testing.T) {
	enableVFIOPassthrough(t)
	state, root, _ := newLifecycleState(t, true, allVfioNodes...)
	_, err := state.Prepare(vfioClaim("gpu-0-128", `{"backendPolicy":"RequireIommuFD"}`))
	require.NoError(t, err)
	cp := readCheckpoint(t, state)
	require.Contains(t, cp.V1.VfioConversions["claim-uid"], "gpu-0-128")

	// Restart: fresh in-memory state over the same checkpoint and CDI dir,
	// with discovery's view of a GPU that is now bound to vfio-pci.
	setPCIDriver(t, root, convertAddr, "vfio-pci")
	restarted := &DeviceState{
		cdi:               state.cdi,
		checkpointManager: state.checkpointManager,
		vfioManager:       &VfioPciManager{iommuFDEnabled: true},
		allocatable: AllocatableDevices{
			"gpu-vfio-0": {Vfio: &AmdGpuVFIOInfo{
				PCIAddress: convertAddr, IOMMUGroup: "42", preConfigureDriver: consts.VFIODriverName,
			}},
		},
	}
	restarted.recoverVfioConversions(cp.V1.VfioConversions)

	assert.NotContains(t, restarted.allocatable, "gpu-vfio-0", "discovery's duplicate must be dropped")
	dev := restarted.allocatable["gpu-0-128"]
	require.NotNil(t, dev)
	require.NotNil(t, dev.Vfio)
	assert.Equal(t, "amdgpu", dev.Vfio.preConfigureDriver)
	assert.Equal(t, "42", dev.Vfio.IOMMUGroup)
	assertPublished(t, restarted.allocatable, map[string]string{"gpu-0-128": consts.AmdGpuDeviceType})

	require.NoError(t, restarted.Unprepare("claim-uid"))
	bound, err := os.ReadFile(filepath.Join(root, "sys/bus/pci/drivers/amdgpu/bind"))
	require.NoError(t, err)
	assert.Equal(t, convertAddr, string(bound), "GPU should be rebound to amdgpu")
	assertRestoredGPU(t, restarted, "gpu-0-128")
	assert.Equal(t, 128, restarted.allocatable["gpu-0-128"].AmdGpu.renderIndex)
	assert.Empty(t, readCheckpoint(t, restarted).V1.VfioConversions)
}

// TestConvertedGPU_AdvertisedAsOriginal checks that a GPU converted to VFIO
// for a claim keeps being published under its own name and attributes, and
// does not collide with a pre-bound VFIO device.
func TestConvertedGPU_AdvertisedAsOriginal(t *testing.T) {
	enableVFIOPassthrough(t)
	state, _ := setupConvertibleGPU(t, true, allVfioNodes...)
	state.cdi = &CDIHandler{}
	state.allocatable["gpu-vfio-0"] = &AllocatableDevice{Vfio: &AmdGpuVFIOInfo{
		PCIAddress: "0000:0e:00.0", Index: 0, preConfigureDriver: consts.VFIODriverName,
	}}
	want := map[string]string{
		"gpu-0-128":  consts.AmdGpuDeviceType,
		"gpu-vfio-0": consts.VfioDeviceType,
	}
	assertPublished(t, state.allocatable, want)

	pd, err := state.prepareDevices(vfioClaim("gpu-0-128", ""))
	require.NoError(t, err)
	require.Equal(t, consts.VfioDeviceType, state.allocatable["gpu-0-128"].Type())
	assertPublished(t, state.allocatable, want)

	require.NoError(t, state.unprepareDevices("claim-uid", pd))
	assertPublished(t, state.allocatable, want)
}
