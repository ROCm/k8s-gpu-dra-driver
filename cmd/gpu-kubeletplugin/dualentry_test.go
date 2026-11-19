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
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/amdgpu"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/consts"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/featuregates"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
)

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

// gpuPCI returns the PCI address of the i-th fake dual-entry GPU.
func gpuPCI(i int) string { return fmt.Sprintf("0000:%02x:00.0", 0x0a+i) }

// vfioSiblingName returns the canonical name of the i-th fake dual-entry
// GPU's type=vfio sibling, derived from its PCI address the same way
// AmdGpuVFIOInfo.CanonicalName does.
func vfioSiblingName(i int) string { return "gpu-vfio-" + pciAddrToDNSLabel(gpuPCI(i)) }

// newDualEntryState returns a DeviceState for n amdgpu-bound compute GPUs,
// each with its type=vfio sibling, built and marked as discovery and
// NewDeviceState do, and wired to a real CDI handler and checkpoint manager
// in temp dirs. GPU i is gpu-<i>-<128+i> / gpu-vfio-<i> in IOMMU group 42+i.
func newDualEntryState(t *testing.T, n int) (*DeviceState, string, string) {
	t.Helper()
	root := setupFakeVfioSysfs(t)
	previousDiscovery := discoverAMDGPUs
	discoverAMDGPUs = func() map[string]map[string]interface{} {
		gpus := make(map[string]map[string]interface{})
		for i := 0; i < n; i++ {
			pci := gpuPCI(i)
			driver, _ := amdgpu.GetPCIDriver(pci)
			bound, _ := os.ReadFile(filepath.Join(root, "sys/bus/pci/drivers/amdgpu/bind"))
			if driver != consts.AMDGPUDriverName && string(bound) == pci {
				link := filepath.Join(root, "sys/bus/pci/devices", pci, "driver")
				if err := os.Remove(link); err != nil {
					t.Errorf("remove fake driver link: %v", err)
				}
				if err := os.Symlink("../../../../bus/pci/drivers/amdgpu", link); err != nil {
					t.Errorf("restore fake amdgpu link: %v", err)
				}
				driver = consts.AMDGPUDriverName
			}
			if driver != consts.AMDGPUDriverName {
				continue
			}
			gpus[pci] = map[string]interface{}{
				"card": i, "renderD": 128 + i, "kfdID": "", "deviceID": "",
				"driverVersion": "", "computePartitionType": "", "memoryPartitionType": "",
				"productName": "", "numaNode": 0, "pciAddr": pci,
			}
		}
		return gpus
	}
	t.Cleanup(func() { discoverAMDGPUs = previousDiscovery })
	createDriverDir(t, root, consts.AMDGPUDriverName)
	createDriverDir(t, root, consts.VFIODriverName)
	createDevNode(t, root, "dev/vfio/vfio")

	allocatable := AllocatableDevices{}
	for i := 0; i < n; i++ {
		pci := gpuPCI(i)
		createPCIDevice(t, root, pci, consts.AMDGPUDriverName)
		require.NoError(t, os.Symlink(fmt.Sprintf("../../../kernel/iommu_groups/%d", 42+i),
			filepath.Join(root, "sys/bus/pci/devices", pci, "iommu_group")))
		createDevNode(t, root, fmt.Sprintf("dev/vfio/%d", 42+i))
		compute := &AllocatableDevice{AmdGpu: &AmdGpuInfo{PCIAddress: pci, cardIndex: i, renderIndex: 128 + i}}
		allocatable[compute.CanonicalName()] = compute
		sibling := newVFIOSibling(compute.AmdGpu)
		allocatable[sibling.CanonicalName()] = sibling
	}
	markSiblingPairs(allocatable)

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
		allocatable:       allocatable,
	}
	return state, root, cdiRoot
}

// convertClaim allocates a compute GPU with a VfioDeviceConfig, so Prepare
// converts it to VFIO in place and Unprepare converts it back.
func convertClaim(uid, device string) *resourceapi.ResourceClaim {
	c := directClaim(uid, device)
	c.Status.Allocation.Devices.Config = []resourceapi.DeviceAllocationConfiguration{{
		Source: resourceapi.AllocationConfigSourceClaim,
		DeviceConfiguration: resourceapi.DeviceConfiguration{
			Opaque: &resourceapi.OpaqueDeviceConfiguration{
				Driver:     consts.DriverName,
				Parameters: runtime.RawExtension{Raw: []byte(`{"apiVersion":"gpu.resource.amd.com/v1alpha1","kind":"VfioDeviceConfig"}`)},
			},
		},
	}}
	return c
}

// directClaim allocates device with no opaque config at all.
func directClaim(uid, device string) *resourceapi.ResourceClaim {
	return &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID(uid)},
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
	pci := gpuPCI(0)
	createPCIDevice(t, root, pci, consts.AMDGPUDriverName)
	require.NoError(t, os.Symlink("../../../kernel/iommu_groups/42",
		filepath.Join(root, "sys/bus/pci/devices", pci, "iommu_group")))

	gpu := &AmdGpuInfo{
		PCIAddress: pci, DeviceID: "0x74a1", ProductName: "MI300X", NumaNode: 1,
		MemoryBytes: 192 << 30, ComputeUnits: 304, SimdUnits: 1216,
	}
	dev := newVFIOSibling(gpu)
	require.NotNil(t, dev.Vfio)
	v := dev.Vfio
	assert.Equal(t, consts.AMDGPUDriverName, v.preConfigureDriver, "release must rebind the GPU to amdgpu")
	assert.Equal(t, "gpu-vfio-"+pciAddrToDNSLabel(pci), dev.CanonicalName())
	assert.Equal(t, "42", v.IOMMUGroup)
	assert.Equal(t, consts.AMDVendorID, v.VendorID)
	assert.Equal(t, gpu.MemoryBytes, v.MemoryBytes)
	assert.Equal(t, gpu.ComputeUnits, v.ComputeUnits)
	assert.Equal(t, gpu.SimdUnits, v.SimdUnits)
}

// TestPrepareUnprepare_DirectVFIOSibling claims a GPU's type=vfio sibling
// directly, with no VfioDeviceConfig, as docs/installation.md documents. Prepare
// must bind it and return its VFIO CDI device while the compute entry stays
// advertised but unallocatable alongside it. Unprepare must rebind the GPU to
// amdgpu rather than leave it unbound.
func TestPrepareUnprepare_DirectVFIOSibling(t *testing.T) {
	setVFIOPassthrough(t, true)
	state, root, cdiRoot := newDualEntryState(t, 1)
	pci := gpuPCI(0)

	devices, err := state.Prepare(directClaim("claim-uid", vfioSiblingName(0)))
	require.NoError(t, err)
	require.Len(t, devices, 1, "a direct type=vfio claim must prepare its device")
	assert.Equal(t, vfioSiblingName(0), devices[0].DeviceName)

	bound, err := os.ReadFile(filepath.Join(root, "sys/bus/pci/drivers/vfio-pci/bind"))
	require.NoError(t, err)
	assert.Equal(t, pci, string(bound), "Prepare binds the GPU to vfio-pci")

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

	// Both entries stay advertised while the claim holds the GPU; the shared
	// counter is what keeps the scheduler from also allocating the compute
	// entry.
	pool := (&driver{state: state}).buildDriverResources("node").Pools["node"]
	assert.ElementsMatch(t, []string{"gpu-0-128", vfioSiblingName(0)}, publishedDeviceNames(pool))
	assert.False(t, canAllocateTogether(t, pool, "gpu-0-128", vfioSiblingName(0)))

	// The kernel now has the GPU on vfio-pci.
	setPCIDriver(t, root, pci, consts.VFIODriverName)

	require.NoError(t, state.Unprepare("claim-uid"))
	rebound, err := os.ReadFile(filepath.Join(root, "sys/bus/pci/drivers/amdgpu/bind"))
	require.NoError(t, err)
	assert.Equal(t, pci, string(rebound), "release must rebind the GPU to amdgpu")
	assert.Contains(t, state.allocatable, "gpu-0-128")
	assert.Contains(t, state.allocatable, vfioSiblingName(0))
}

func TestDirectVFIOSibling_RestartAndRelease(t *testing.T) {
	setVFIOPassthrough(t, true)
	state, root, _ := newDualEntryState(t, 1)
	name, pci := vfioSiblingName(0), gpuPCI(0)
	_, err := state.Prepare(directClaim("claim-uid", name))
	require.NoError(t, err)
	cp := readCheckpoint(t, state)
	require.NotNil(t, cp.V1.DirectVfioBindings["claim-uid"].Devices[name])
	assert.Equal(t, pci, cp.V1.DirectVfioBindings["claim-uid"].Devices[name].PCIAddress)

	setPCIDriver(t, root, pci, consts.VFIODriverName)
	restarted := &DeviceState{
		cdi: state.cdi, checkpointManager: state.checkpointManager, vfioManager: &VfioPciManager{},
		allocatable: AllocatableDevices{
			name: {Vfio: &AmdGpuVFIOInfo{PCIAddress: pci, IOMMUGroup: "42", preConfigureDriver: consts.VFIODriverName}},
		},
		directVfioBindings: cp.V1.DirectVfioBindings,
	}
	require.NoError(t, restarted.recoverDirectVfioBindings(cp))
	assertPublished(t, restarted.allocatable, map[string]string{name: consts.VfioDeviceType})
	assert.Equal(t, consts.AMDGPUDriverName, restarted.allocatable[name].Vfio.preConfigureDriver)
	assert.Empty(t, (&driver{state: restarted}).collectCounterSets())

	previousDiscovery := discoverAMDGPUs
	discoverAMDGPUs = func() map[string]map[string]interface{} {
		gpus := previousDiscovery()
		if gpu := gpus[pci]; gpu != nil {
			gpu["card"], gpu["renderD"] = 7, 135
		}
		return gpus
	}
	t.Cleanup(func() { discoverAMDGPUs = previousDiscovery })
	require.NoError(t, restarted.Unprepare("claim-uid"))
	assertPublished(t, restarted.allocatable, map[string]string{
		"gpu-7-135": consts.AmdGpuDeviceType, name: consts.VfioDeviceType,
	})
	assert.Equal(t, consts.AMDGPUDriverName, restarted.allocatable[name].Vfio.preConfigureDriver)
	assert.Len(t, (&driver{state: restarted}).collectCounterSets(), 1)
	assert.Empty(t, readCheckpoint(t, restarted).V1.DirectVfioBindings)
	assert.NotContains(t, readCheckpoint(t, restarted).V1.PreparedClaims, "claim-uid")
}

func TestDirectVFIOSibling_IntentCheckpointFailureDoesNotBind(t *testing.T) {
	setVFIOPassthrough(t, true)
	state, root, _ := newDualEntryState(t, 1)
	manager := state.checkpointManager
	state.checkpointManager = &observingCheckpointManager{
		CheckpointManager: manager,
		onCreate: func(checkpoint checkpointmanager.Checkpoint) error {
			if len(checkpoint.(*Checkpoint).V1.DirectVfioBindings) > 0 {
				return errors.New("direct binding checkpoint failed")
			}
			return nil
		},
	}
	_, err := state.Prepare(directClaim("claim-uid", vfioSiblingName(0)))
	require.ErrorContains(t, err, "direct binding checkpoint failed")
	bound, err := os.ReadFile(filepath.Join(root, "sys/bus/pci/drivers/vfio-pci/bind"))
	require.NoError(t, err)
	assert.Empty(t, bound)
	assert.Empty(t, readCheckpoint(t, state).V1.DirectVfioBindings)
}

func TestDirectVFIOSibling_FailedRollbackSurvivesRestart(t *testing.T) {
	setVFIOPassthrough(t, true)
	state, root, _ := newDualEntryState(t, 1)
	name, pci := vfioSiblingName(0), gpuPCI(0)
	unbind := filepath.Join(root, "sys/bus/pci/drivers/vfio-pci/unbind")
	manager := state.checkpointManager
	interrupted := false
	state.checkpointManager = &observingCheckpointManager{
		CheckpointManager: manager,
		onCreate: func(checkpoint checkpointmanager.Checkpoint) error {
			if !interrupted && len(checkpoint.(*Checkpoint).V1.DirectVfioBindings) > 0 {
				interrupted = true
				setPCIDriver(t, root, pci, consts.VFIODriverName)
				require.NoError(t, os.Remove(unbind))
				require.NoError(t, os.Mkdir(unbind, 0755))
			}
			return nil
		},
	}
	claim := convertClaim("claim-uid", name)
	claim.Status.Allocation.Devices.Config[0].DeviceConfiguration.Opaque.Parameters.Raw = []byte(`{"apiVersion":"gpu.resource.amd.com/v1alpha1","kind":"VfioDeviceConfig","iommu":{"backendPolicy":"RequireIommuFD"}}`)
	_, err := state.Prepare(claim)
	require.ErrorContains(t, err, "VFIO rollback incomplete")
	assert.NotContains(t, readCheckpoint(t, state).V1.PreparedClaims, "claim-uid")
	cp := readCheckpoint(t, state)
	require.NotNil(t, cp.V1.DirectVfioBindings["claim-uid"].Devices[name])
	assert.NotContains(t, state.allocatable, name)
	assert.NotContains(t, state.allocatable, "gpu-0-128")

	restarted := &DeviceState{
		cdi: state.cdi, checkpointManager: state.checkpointManager, vfioManager: &VfioPciManager{},
		allocatable: AllocatableDevices{
			name: {Vfio: &AmdGpuVFIOInfo{PCIAddress: pci, preConfigureDriver: consts.VFIODriverName}},
		},
		directVfioBindings: cp.V1.DirectVfioBindings,
	}
	require.NoError(t, restarted.recoverDirectVfioBindings(cp))
	assert.Empty(t, restarted.allocatable, "stranded PF must not be advertised")
	assert.Contains(t, readCheckpoint(t, restarted).V1.DirectVfioBindings, "claim-uid")
	require.NoError(t, os.Remove(unbind))
	require.NoError(t, os.WriteFile(unbind, nil, 0644))
	require.NoError(t, restarted.Unprepare("claim-uid"))
	assertPublished(t, restarted.allocatable, map[string]string{
		"gpu-0-128": consts.AmdGpuDeviceType, name: consts.VfioDeviceType,
	})
	assert.Empty(t, readCheckpoint(t, restarted).V1.DirectVfioBindings)
}

func TestDirectVFIOSibling_InterruptedMultiDeviceRelease(t *testing.T) {
	setVFIOPassthrough(t, true)
	state, root, _ := newDualEntryState(t, 2)
	claim := directClaim("claim-uid", vfioSiblingName(0))
	claim.Status.Allocation.Devices.Results = append(claim.Status.Allocation.Devices.Results,
		resourceapi.DeviceRequestAllocationResult{Request: "gpu", Driver: consts.DriverName, Pool: "node", Device: vfioSiblingName(1)})
	_, err := state.Prepare(claim)
	require.NoError(t, err)
	setPCIDriver(t, root, gpuPCI(0), consts.AMDGPUDriverName) // first PF was already rebound
	setPCIDriver(t, root, gpuPCI(1), consts.VFIODriverName)
	cp := readCheckpoint(t, state)
	cp.V1.DirectVfioBindings["claim-uid"].Releasing = true
	require.NoError(t, state.checkpointManager.CreateCheckpoint(DriverPluginCheckpointFile, cp))

	restarted := &DeviceState{
		cdi: state.cdi, checkpointManager: state.checkpointManager, vfioManager: &VfioPciManager{},
		allocatable: AllocatableDevices{}, directVfioBindings: cp.V1.DirectVfioBindings,
	}
	_, err = restarted.Prepare(claim)
	require.ErrorContains(t, err, "being released")
	require.NoError(t, restarted.recoverDirectVfioBindings(cp))
	assertPublished(t, restarted.allocatable, map[string]string{
		"gpu-0-128": consts.AmdGpuDeviceType, vfioSiblingName(0): consts.VfioDeviceType,
		"gpu-1-129": consts.AmdGpuDeviceType, vfioSiblingName(1): consts.VfioDeviceType,
	})
	assert.Empty(t, readCheckpoint(t, restarted).V1.DirectVfioBindings)
	assert.NotContains(t, readCheckpoint(t, restarted).V1.PreparedClaims, "claim-uid")
}

func TestDirectVFIOSibling_ReleasePublicationFailureRetries(t *testing.T) {
	setVFIOPassthrough(t, true)
	state, root, _ := newDualEntryState(t, 1)
	name := vfioSiblingName(0)
	_, err := state.Prepare(directClaim("claim-uid", name))
	require.NoError(t, err)
	setPCIDriver(t, root, gpuPCI(0), consts.VFIODriverName)
	failed := false
	state.driver = &driver{state: state, nodeName: "node", publish: func(context.Context, resourceslice.DriverResources) error {
		if !failed {
			failed = true
			return errors.New("publication failed")
		}
		return nil
	}}
	err = state.Unprepare("claim-uid")
	require.ErrorContains(t, err, "publication failed")
	cp := readCheckpoint(t, state)
	assert.True(t, cp.V1.DirectVfioBindings["claim-uid"].Releasing)
	assert.Contains(t, cp.V1.PreparedClaims, "claim-uid")
	assert.NotContains(t, state.allocatable, name)
	require.NoError(t, state.Unprepare("claim-uid"))
	assert.Empty(t, readCheckpoint(t, state).V1.DirectVfioBindings)
	assert.Contains(t, state.allocatable, name)
}

func TestDirectVFIOSibling_FinalCheckpointFailureRetries(t *testing.T) {
	setVFIOPassthrough(t, true)
	state, root, _ := newDualEntryState(t, 1)
	claim := directClaim("claim-uid", vfioSiblingName(0))
	_, err := state.Prepare(claim)
	require.NoError(t, err)
	setPCIDriver(t, root, gpuPCI(0), consts.VFIODriverName)
	manager := state.checkpointManager
	failFinal := true
	state.checkpointManager = &observingCheckpointManager{
		CheckpointManager: manager,
		onCreate: func(checkpoint checkpointmanager.Checkpoint) error {
			cp := checkpoint.(*Checkpoint)
			if failFinal && cp.V1.PreparedClaims["claim-uid"] == nil && cp.V1.DirectVfioBindings["claim-uid"] == nil {
				failFinal = false
				return errors.New("final checkpoint failed")
			}
			return nil
		},
	}
	err = state.Unprepare("claim-uid")
	require.ErrorContains(t, err, "final checkpoint failed")
	cp := readCheckpoint(t, state)
	assert.True(t, cp.V1.DirectVfioBindings["claim-uid"].Releasing)
	assert.Contains(t, cp.V1.PreparedClaims, "claim-uid")
	_, err = state.Prepare(claim)
	require.ErrorContains(t, err, "being released")
	require.NoError(t, state.Unprepare("claim-uid"))
	assert.Empty(t, readCheckpoint(t, state).V1.DirectVfioBindings)
}

func TestDirectVFIOSibling_ManagerUnavailableKeepsClaim(t *testing.T) {
	setVFIOPassthrough(t, true)
	state, root, _ := newDualEntryState(t, 1)
	name, pci := vfioSiblingName(0), gpuPCI(0)
	claim := directClaim("claim-uid", name)
	_, err := state.Prepare(claim)
	require.NoError(t, err)
	setPCIDriver(t, root, pci, consts.VFIODriverName)
	cp := readCheckpoint(t, state)
	restarted := &DeviceState{
		cdi: state.cdi, checkpointManager: state.checkpointManager,
		allocatable: AllocatableDevices{}, directVfioBindings: cp.V1.DirectVfioBindings,
	}
	require.NoError(t, restarted.recoverDirectVfioBindings(cp))
	assert.Empty(t, restarted.allocatable)
	_, err = restarted.Prepare(claim)
	require.ErrorContains(t, err, "VFIO manager unavailable")
	err = restarted.Unprepare("claim-uid")
	require.ErrorContains(t, err, "VFIO manager unavailable")
	cp = readCheckpoint(t, restarted)
	assert.True(t, cp.V1.DirectVfioBindings["claim-uid"].Releasing)
	assert.Contains(t, cp.V1.PreparedClaims, "claim-uid")

	restarted.vfioManager = &VfioPciManager{}
	require.NoError(t, restarted.Unprepare("claim-uid"))
	assert.Empty(t, readCheckpoint(t, restarted).V1.DirectVfioBindings)
	assert.Contains(t, restarted.allocatable, "gpu-0-128")
}

func TestDirectVFIOSibling_RefusesVFAtRecordedPFAddress(t *testing.T) {
	setVFIOPassthrough(t, true)
	state, root, _ := newDualEntryState(t, 1)
	name, pci := vfioSiblingName(0), gpuPCI(0)
	_, err := state.Prepare(directClaim("claim-uid", name))
	require.NoError(t, err)
	setPCIDriver(t, root, pci, consts.VFIODriverName)
	physfn := filepath.Join(root, "sys/bus/pci/devices", pci, "physfn")
	require.NoError(t, os.Symlink("../0000:09:00.0", physfn))
	err = state.Unprepare("claim-uid")
	require.ErrorContains(t, err, "now an SR-IOV VF")
	assert.Contains(t, readCheckpoint(t, state).V1.DirectVfioBindings, "claim-uid")
	require.NoError(t, os.Remove(physfn))
	require.NoError(t, state.Unprepare("claim-uid"))
}

func TestPreboundVFIOPFDoesNotCreateDirectBinding(t *testing.T) {
	setVFIOPassthrough(t, true)
	state, root, _ := newDualEntryState(t, 1)
	name, pci := vfioSiblingName(0), gpuPCI(0)
	setPCIDriver(t, root, pci, consts.VFIODriverName)
	state.allocatable = AllocatableDevices{
		name: {Vfio: &AmdGpuVFIOInfo{
			PCIAddress: pci, IOMMUGroup: "42", preConfigureDriver: consts.VFIODriverName,
		}},
	}
	_, err := state.Prepare(directClaim("claim-uid", name))
	require.NoError(t, err)
	assert.Empty(t, readCheckpoint(t, state).V1.DirectVfioBindings)
	require.NoError(t, state.Unprepare("claim-uid"))
	driver, err := amdgpu.GetPCIDriver(pci)
	require.NoError(t, err)
	assert.Equal(t, consts.VFIODriverName, driver)
}

func TestAutoPartition_ReservedVfioBDFKeepsGPUIndex(t *testing.T) {
	setVFIOPassthrough(t, true)
	setupFakeVfioSysfs(t)
	const lower = "0000:0a:00.0"
	const higher = "0000:0c:00.0"
	const syntheticName = "gpu-2-cpx-nps4"
	previousDiscovery := discoverAMDGPUs
	discoverAMDGPUs = func() map[string]map[string]interface{} {
		return map[string]map[string]interface{}{
			higher: {
				"card": 1, "renderD": 129, "kfdID": "", "deviceID": "",
				"driverVersion": "", "computePartitionType": "spx", "memoryPartitionType": "nps1",
				"productName": "MI300X", "numaNode": 0, "pciAddr": higher,
			},
		}
	}
	t.Cleanup(func() { discoverAMDGPUs = previousDiscovery })
	cp := newCheckpoint()
	cp.V1.DirectVfioBindings = map[string]*DirectVfioClaim{
		"direct": {Devices: map[string]*DirectVfioBinding{
			directVFIOName(lower): {PCIAddress: lower, OriginalDriver: consts.AMDGPUDriverName},
		}},
	}
	cp.V1.VfioConversions = map[string]map[string]*VfioConversionRecord{
		"converted": {"gpu-0-128": {PCIAddress: "0000:0b:00.0"}},
	}
	allocatable, gpuAddresses, partitionable, err := enumerateAllPossibleDevices(true, checkpointReservedBDFs(cp.V1)...)
	require.NoError(t, err)
	assert.Equal(t, map[int]string{2: higher}, gpuAddresses)
	assert.Equal(t, []int{2}, partitionable)
	assert.Contains(t, allocatable, syntheticName)
	assert.NotContains(t, gpuAddresses, 0, "VFIO-bound PF has no AMD SMI handle")
	assert.NotContains(t, gpuAddresses, 1, "converted VFIO PF has no AMD SMI handle")
	shareKey := ShareKey("synthetic", "gpu", syntheticName, nil)
	ps := NewPartitionState(gpuAddresses, partitionable, allocatable, false, nil)
	prepared := PreparedClaims{"synthetic": {&PreparedDevice{
		Device: drapbv1.Device{DeviceName: syntheticName}, ShareKey: shareKey,
	}}}
	ps.RecoverFromCheckpoint("nps4", map[int]string{2: "cpx"}, nil, prepared,
		map[int]map[string]int{2: {shareKey: 2}})
	slot, ok := ps.GetAssignedSlot(2, shareKey)
	assert.True(t, ok)
	assert.Equal(t, 2, slot)
	assert.True(t, ps.HasReservation("synthetic"))
	d := &driver{
		state:                    &DeviceState{allocatable: allocatable, partitionState: ps},
		enableSyntheticPartition: true, partitionableGPUs: partitionable, nodeName: "node",
	}
	pool := d.buildResources().Pools["node"]
	assert.Contains(t, publishedDeviceNames(pool), syntheticName)
	var counterNames []string
	for _, sl := range pool.Slices {
		for _, cs := range sl.SharedCounters {
			counterNames = append(counterNames, cs.Name)
		}
	}
	assert.Contains(t, counterNames, mutexCounterSetName(2))
	assert.NotContains(t, counterNames, mutexCounterSetName(0))
	assert.NotContains(t, counterNames, mutexCounterSetName(1))
}

func TestDirectVFIOSibling_LocalPFConflict(t *testing.T) {
	setVFIOPassthrough(t, true)
	state, _, _ := newDualEntryState(t, 1)
	_, err := state.Prepare(directClaim("claim-a", vfioSiblingName(0)))
	require.NoError(t, err)
	_, err = state.Prepare(directClaim("claim-b", "gpu-0-128"))
	require.ErrorContains(t, err, "already prepared")

	state, _, _ = newDualEntryState(t, 1)
	dual := directClaim("claim-c", "gpu-0-128")
	dual.Status.Allocation.Devices.Results = append(dual.Status.Allocation.Devices.Results,
		resourceapi.DeviceRequestAllocationResult{Request: "gpu", Driver: consts.DriverName, Pool: "node", Device: vfioSiblingName(0)})
	_, err = state.Prepare(dual)
	require.ErrorContains(t, err, "through both")
}

func TestMarkSiblingPairs_ConvertedComputeKeepsCounter(t *testing.T) {
	pci := gpuPCI(0)
	original := &AmdGpuInfo{PCIAddress: pci, cardIndex: 0, renderIndex: 128}
	converted := &AmdGpuVFIOInfo{PCIAddress: pci, convertedFrom: original}
	sibling := &AmdGpuVFIOInfo{PCIAddress: pci, preConfigureDriver: consts.AMDGPUDriverName}
	devices := AllocatableDevices{
		"gpu-0-128":        {Vfio: converted},
		vfioSiblingName(0): {Vfio: sibling},
	}
	markSiblingPairs(devices)
	assert.True(t, original.siblingExclusive)
	assert.True(t, converted.siblingExclusive)
	assert.True(t, sibling.siblingExclusive)
	delete(devices, vfioSiblingName(0))
	markSiblingPairs(devices)
	assert.False(t, original.siblingExclusive)
	assert.False(t, converted.siblingExclusive)
}

func TestRollbackVfioConversions(t *testing.T) {
	root := setupFakeVfioSysfs(t)
	pci := gpuPCI(0)
	createPCIDevice(t, root, pci, consts.VFIODriverName)
	createDriverDir(t, root, consts.VFIODriverName)
	createDriverDir(t, root, consts.AMDGPUDriverName)

	original := &AmdGpuInfo{PCIAddress: pci, cardIndex: 0, renderIndex: 128}
	state := &DeviceState{
		allocatable: AllocatableDevices{
			"gpu-0-128": {Vfio: &AmdGpuVFIOInfo{
				PCIAddress:         pci,
				preConfigureDriver: consts.AMDGPUDriverName,
			}},
		},
		vfioConversions: map[string]map[string]*AmdGpuInfo{
			"claim-uid": {"gpu-0-128": original},
		},
		vfioManager: &VfioPciManager{},
	}

	require.NoError(t, state.releaseClaimVfio("claim-uid", []string{"gpu-0-128"}))

	allocDev := state.allocatable["gpu-0-128"]
	assert.Same(t, original, allocDev.AmdGpu)
	assert.Nil(t, allocDev.Vfio)
	assert.Empty(t, state.vfioConversions)
	bindContent, err := os.ReadFile(filepath.Join(root, "sys/bus/pci/drivers", consts.AMDGPUDriverName, "bind"))
	require.NoError(t, err)
	assert.Equal(t, pci, string(bindContent))
}

func TestPrepareDevicesWithoutVfioManagerDoesNotConvert(t *testing.T) {
	setVFIOPassthrough(t, true)
	state := &DeviceState{
		allocatable: AllocatableDevices{
			"gpu-0-128": {AmdGpu: &AmdGpuInfo{PCIAddress: gpuPCI(0), cardIndex: 0, renderIndex: 128}},
		},
	}

	_, err := state.prepareDevices(convertClaim("claim-uid", "gpu-0-128"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VFIO manager not available")
	assert.Equal(t, consts.AmdGpuDeviceType, state.allocatable["gpu-0-128"].Type())
}

func TestPrepareDevices_VFIODeviceWithGateDisabled(t *testing.T) {
	setVFIOPassthrough(t, false)
	state, _, _ := newDualEntryState(t, 1)

	_, err := state.prepareDevices(directClaim("claim-uid", vfioSiblingName(0)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VFIOPassthrough")
}

// publishedDeviceNames lists the device names in a published pool.
func publishedDeviceNames(pool resourceslice.Pool) []string {
	var names []string
	for _, sl := range pool.Slices {
		for _, d := range sl.Devices {
			names = append(names, d.Name)
		}
	}
	return names
}

// TestDriver_ConcurrentPrepareUnprepareAndPublish runs the kubelet plugin's
// Prepare/Unprepare RPC handlers for several claims in parallel, with
// DeviceMetadata enabled, while another goroutine rebuilds and publishes the
// ResourceSlices as a partition-taint republish does. Odd GPUs are claimed
// through their compute entry with a VfioDeviceConfig, so Prepare/Unprepare
// convert the device in place; even GPUs are claimed through their type=vfio
// entry. Run with -race: every read of the device state (slice building and
// the DeviceMetadata lookup) must be synchronized with those conversions.
func TestDriver_ConcurrentPrepareUnprepareAndPublish(t *testing.T) {
	setVFIOPassthrough(t, true)
	require.NoError(t, featuregates.FeatureGates().Set("DeviceMetadata=true"))
	t.Cleanup(func() { _ = featuregates.FeatureGates().Set("DeviceMetadata=false") })

	const gpus, rounds = 4, 20
	state, _, _ := newDualEntryState(t, gpus)
	var publishes atomic.Int64
	d := &driver{state: state, nodeName: "node", publish: func(context.Context, resourceslice.DriverResources) error {
		publishes.Add(1)
		return nil
	}}
	state.driver = d
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < gpus; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			uid := fmt.Sprintf("claim-%d", i)
			for r := 0; r < rounds; r++ {
				claim := directClaim(uid, vfioSiblingName(i))
				if i%2 == 1 {
					claim = convertClaim(uid, fmt.Sprintf("gpu-%d-%d", i, 128+i))
				}
				res := d.prepareResourceClaim(ctx, claim)
				if !assert.NoError(t, res.Err) || !assert.Len(t, res.Devices, 1) {
					return
				}
				assert.NotNil(t, res.Devices[0].Metadata, "DeviceMetadata attributes are returned")
				assert.NoError(t, d.unprepareResourceClaim(ctx, kubeletplugin.NamespacedObject{UID: types.UID(uid)}))
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for r := 0; r < gpus*rounds; r++ {
			state.Lock()
			assert.NoError(t, d.republishResourcesLocked(ctx))
			state.Unlock()
		}
	}()
	wg.Wait()
	// Direct VFIO release republishes each restored compute/VFIO pair.
	assert.Equal(t, int64(gpus*rounds+(gpus/2)*rounds), publishes.Load())

	// Every claim was released, so every GPU is back to its original entry
	// type, including the compute GPUs other claims' prepares ran between.
	for i := 0; i < gpus; i++ {
		assert.Equal(t, consts.AmdGpuDeviceType, state.allocatable[fmt.Sprintf("gpu-%d-%d", i, 128+i)].Type())
		assert.Equal(t, consts.VfioDeviceType, state.allocatable[vfioSiblingName(i)].Type())
	}
	assert.Empty(t, state.vfioConversions)
}

// TestConversions_SurviveOtherClaims converts a compute GPU for claim A,
// prepares claim B (converting another GPU) before A is released, and checks
// that releasing A still restores its GPU's compute entry.
func TestConversions_SurviveOtherClaims(t *testing.T) {
	setVFIOPassthrough(t, true)
	state, _, _ := newDualEntryState(t, 2)

	_, err := state.Prepare(convertClaim("claim-a", "gpu-0-128"))
	require.NoError(t, err)
	_, err = state.Prepare(convertClaim("claim-b", "gpu-1-129"))
	require.NoError(t, err)
	require.Equal(t, consts.VfioDeviceType, state.allocatable["gpu-0-128"].Type())

	require.NoError(t, state.Unprepare("claim-a"))
	assert.Equal(t, consts.AmdGpuDeviceType, state.allocatable["gpu-0-128"].Type(), "claim A's GPU must be restored")
	assert.Equal(t, consts.VfioDeviceType, state.allocatable["gpu-1-129"].Type(), "claim B's GPU stays converted")
	assert.NotContains(t, state.vfioConversions, "claim-a")
	assert.Contains(t, state.vfioConversions["claim-b"], "gpu-1-129")

	require.NoError(t, state.Unprepare("claim-b"))
	assert.Equal(t, consts.AmdGpuDeviceType, state.allocatable["gpu-1-129"].Type())
	assert.Empty(t, state.vfioConversions)
}

func TestAddVFIOSibling(t *testing.T) {
	tests := map[string]struct {
		gate  bool
		isVF  bool
		numVF int
		want  bool
	}{
		"plain GPU":            {gate: true, want: true},
		"PF with active VFs":   {gate: true, numVF: 2, want: false},
		"VF":                   {gate: true, isVF: true, want: false},
		"gate disabled":        {gate: false, want: false},
		"gate disabled, VFs>0": {gate: false, numVF: 2, want: false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			setVFIOPassthrough(t, tc.gate)
			devices := AllocatableDevices{}
			gpu := &AmdGpuInfo{PCIAddress: "0000:0a:00.0", cardIndex: 0, renderIndex: 128}

			assert.Equal(t, tc.want, addVFIOSibling(devices, gpu, tc.isVF, tc.numVF))
			if tc.want {
				assert.Contains(t, devices, "gpu-vfio-0000-0a-00-0")
			} else {
				assert.Empty(t, devices)
			}
		})
	}
}
