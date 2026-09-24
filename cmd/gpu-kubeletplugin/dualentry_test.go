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
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

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

// gpuPCI returns the PCI address of the i-th fake dual-entry GPU.
func gpuPCI(i int) string { return fmt.Sprintf("0000:%02x:00.0", 0x0a+i) }

// newDualEntryState returns a DeviceState for n amdgpu-bound compute GPUs,
// each with its type=vfio sibling, built and marked as discovery and
// NewDeviceState do, and wired to a real CDI handler and checkpoint manager
// in temp dirs. GPU i is gpu-<i>-<128+i> / gpu-vfio-<i> in IOMMU group 42+i.
func newDualEntryState(t *testing.T, n int) (*DeviceState, string, string) {
	t.Helper()
	root := setupFakeVfioSysfs(t)
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
		compute := &AllocatableDevice{AmdGpu: &AmdGpuInfo{PCIAddress: pci, ParentPFAddress: pci, cardIndex: i, renderIndex: 128 + i}}
		allocatable[compute.CanonicalName()] = compute
		sibling := newVFIOSibling(compute.AmdGpu, i, 0)
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
		ParentPFAddress: pci, TotalVFs: 8, MemoryBytes: 192 << 30, ComputeUnits: 304, SimdUnits: 1216,
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
// must bind it and return its VFIO CDI device while the compute entry stays
// advertised but unallocatable alongside it. Unprepare must rebind the GPU to
// amdgpu rather than leave it unbound.
func TestPrepareUnprepare_DirectVFIOSibling(t *testing.T) {
	setVFIOPassthrough(t, true)
	state, root, cdiRoot := newDualEntryState(t, 1)
	pci := gpuPCI(0)

	devices, err := state.Prepare(directClaim("claim-uid", "gpu-vfio-0"))
	require.NoError(t, err)
	require.Len(t, devices, 1, "a direct type=vfio claim must prepare its device")
	assert.Equal(t, "gpu-vfio-0", devices[0].DeviceName)

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
	assert.ElementsMatch(t, []string{"gpu-0-128", "gpu-vfio-0"}, publishedDeviceNames(pool))
	assert.False(t, canAllocateTogether(t, pool, "gpu-0-128", "gpu-vfio-0"))

	// The kernel now has the GPU on vfio-pci.
	setPCIDriver(t, root, pci, consts.VFIODriverName)

	require.NoError(t, state.Unprepare("claim-uid"))
	rebound, err := os.ReadFile(filepath.Join(root, "sys/bus/pci/drivers/amdgpu/bind"))
	require.NoError(t, err)
	assert.Equal(t, pci, string(rebound), "release must rebind the GPU to amdgpu")
	assert.Contains(t, state.allocatable, "gpu-0-128")
	assert.Contains(t, state.allocatable, "gpu-vfio-0")
}

func TestPrepareDevices_VFIODeviceWithGateDisabled(t *testing.T) {
	setVFIOPassthrough(t, false)
	state, _, _ := newDualEntryState(t, 1)

	_, err := state.prepareDevices(directClaim("claim-uid", "gpu-vfio-0"))
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
				claim := directClaim(uid, fmt.Sprintf("gpu-vfio-%d", i))
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
	assert.Equal(t, int64(gpus*rounds), publishes.Load())
}
