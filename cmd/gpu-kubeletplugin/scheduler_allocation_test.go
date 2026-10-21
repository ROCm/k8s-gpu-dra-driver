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

// These tests run the scheduler's own DRA allocator
// (k8s.io/dynamic-resource-allocation/structured, the code kube-scheduler
// uses) against the ResourceSlices this driver publishes, to show that the
// published KEP-4815 counters make the scheduler enforce sibling and PF/VF
// exclusion at allocation time.

import (
	"context"
	"fmt"
	"testing"

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/consts"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/dynamic-resource-allocation/cel"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	"k8s.io/dynamic-resource-allocation/structured"
	"k8s.io/utils/ptr"
)

const schedNode = "node"

// CEL selectors for the device types this driver advertises.
var (
	selCompute = fmt.Sprintf(`device.attributes[%q].type == %q`, consts.DriverName, consts.AmdGpuDeviceType)
	selVFIO    = fmt.Sprintf(`device.attributes[%q].type == %q`, consts.DriverName, consts.VfioDeviceType)
	selVFIOVF  = selVFIO + fmt.Sprintf(` && device.attributes[%q].isVF`, consts.DriverName)
	selVFIOPF  = selVFIO + fmt.Sprintf(` && !device.attributes[%q].isVF`, consts.DriverName)
)

type fakeClassLister map[string]*resourceapi.DeviceClass

func (l fakeClassLister) List() ([]*resourceapi.DeviceClass, error) {
	var out []*resourceapi.DeviceClass
	for _, c := range l {
		out = append(out, c)
	}
	return out, nil
}

func (l fakeClassLister) Get(name string) (*resourceapi.DeviceClass, error) {
	if c, ok := l[name]; ok {
		return c, nil
	}
	return nil, apierrors.NewNotFound(resourceapi.Resource("deviceclasses"), name)
}

// toResourceSlices turns the driver's published pool into the ResourceSlice
// objects the resourceslice controller would create for this node.
func toResourceSlices(pool resourceslice.Pool) []*resourceapi.ResourceSlice {
	var out []*resourceapi.ResourceSlice
	for i, sl := range pool.Slices {
		out = append(out, &resourceapi.ResourceSlice{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%d", schedNode, i)},
			Spec: resourceapi.ResourceSliceSpec{
				Driver: consts.DriverName,
				Pool: resourceapi.ResourcePool{
					Name:               schedNode,
					Generation:         1,
					ResourceSliceCount: int64(len(pool.Slices)),
				},
				NodeName:       ptr.To(schedNode),
				Devices:        sl.Devices,
				SharedCounters: sl.SharedCounters,
			},
		})
	}
	return out
}

// schedClaim is a claim with one exactly-one-device request per selector.
func schedClaim(name string, selectors ...string) *resourceapi.ResourceClaim {
	claim := &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(name)},
	}
	for i, sel := range selectors {
		claim.Spec.Devices.Requests = append(claim.Spec.Devices.Requests, resourceapi.DeviceRequest{
			Name: fmt.Sprintf("req-%d", i),
			Exactly: &resourceapi.ExactDeviceRequest{
				DeviceClassName: consts.DriverName,
				AllocationMode:  resourceapi.DeviceAllocationModeExactCount,
				Count:           1,
				Selectors:       []resourceapi.DeviceSelector{{CEL: &resourceapi.CELDeviceSelector{Expression: sel}}},
			},
		})
	}
	return claim
}

// scheduler allocates claims against a fixed published pool, tracking what
// earlier claims already hold like the scheduler's allocated-device state.
type scheduler struct {
	t         *testing.T
	slices    []*resourceapi.ResourceSlice
	allocated sets.Set[structured.DeviceID]
}

func newScheduler(t *testing.T, allocatable AllocatableDevices) *scheduler {
	t.Helper()
	markSiblingPairs(allocatable)
	pool := (&driver{state: &DeviceState{allocatable: allocatable}}).buildDriverResources(schedNode).Pools[schedNode]
	return &scheduler{t: t, slices: toResourceSlices(pool), allocated: sets.New[structured.DeviceID]()}
}

// allocate runs the scheduler's allocator for one claim. It returns the
// allocated device names, or nil if the claim cannot be allocated on the
// node. On success the devices are recorded as allocated.
func (s *scheduler) allocate(claim *resourceapi.ResourceClaim) []string {
	s.t.Helper()
	ctx := context.Background()
	classes := fakeClassLister{consts.DriverName: {
		ObjectMeta: metav1.ObjectMeta{Name: consts.DriverName},
		Spec: resourceapi.DeviceClassSpec{Selectors: []resourceapi.DeviceSelector{{
			CEL: &resourceapi.CELDeviceSelector{Expression: fmt.Sprintf(`device.driver == %q`, consts.DriverName)},
		}}},
	}}
	allocator, err := structured.NewAllocator(ctx,
		structured.Features{PartitionableDevices: true},
		structured.AllocatedState{
			AllocatedDevices:         s.allocated,
			AllocatedSharedDeviceIDs: sets.New[structured.SharedDeviceID](),
			AggregatedCapacity:       structured.NewConsumedCapacityCollection(),
		},
		classes, s.slices, cel.NewCache(10, cel.Features{}))
	require.NoError(s.t, err)

	results, err := allocator.Allocate(ctx, &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: schedNode}}, []*resourceapi.ResourceClaim{claim})
	require.NoError(s.t, err)
	if results == nil {
		return nil
	}
	require.Len(s.t, results, 1)
	var names []string
	for _, r := range results[0].Devices.Results {
		names = append(names, r.Device)
		s.allocated.Insert(structured.MakeDeviceID(r.Driver, r.Pool, r.Device))
	}
	return names
}

func dualGPU(i int, totalVFs int) (string, *AllocatableDevice, string, *AllocatableDevice) {
	pci := gpuPCI(i)
	compute := &AllocatableDevice{AmdGpu: &AmdGpuInfo{PCIAddress: pci, ParentPFAddress: pci, TotalVFs: totalVFs, cardIndex: i, renderIndex: 128 + i}}
	vfio := &AllocatableDevice{Vfio: &AmdGpuVFIOInfo{PCIAddress: pci, ParentPFAddress: pci, TotalVFs: totalVFs, Index: i}}
	return compute.CanonicalName(), compute, vfio.CanonicalName(), vfio
}

func TestScheduler_SiblingExclusion(t *testing.T) {
	t.Run("GPU without SR-IOV: compute then VFIO", func(t *testing.T) {
		cName, c, vName, v := dualGPU(0, 0)
		s := newScheduler(t, AllocatableDevices{cName: c, vName: v})
		require.Equal(t, []string{cName}, s.allocate(schedClaim("a", selCompute)))
		require.Nil(t, s.allocate(schedClaim("b", selVFIO)), "the VFIO sibling of an allocated compute GPU must not be allocatable")
	})

	t.Run("GPU without SR-IOV: VFIO then compute", func(t *testing.T) {
		cName, c, vName, v := dualGPU(0, 0)
		s := newScheduler(t, AllocatableDevices{cName: c, vName: v})
		require.Equal(t, []string{vName}, s.allocate(schedClaim("a", selVFIO)))
		require.Nil(t, s.allocate(schedClaim("b", selCompute)), "the compute sibling of an allocated VFIO GPU must not be allocatable")
	})

	t.Run("one claim cannot take both entries of one GPU", func(t *testing.T) {
		cName, c, vName, v := dualGPU(0, 0)
		s := newScheduler(t, AllocatableDevices{cName: c, vName: v})
		require.Nil(t, s.allocate(schedClaim("a", selCompute, selVFIO)))
	})

	t.Run("the scheduler moves to the other GPU", func(t *testing.T) {
		c0, cd0, v0, vd0 := dualGPU(0, 0)
		c1, cd1, v1, vd1 := dualGPU(1, 0)
		s := newScheduler(t, AllocatableDevices{c0: cd0, v0: vd0, c1: cd1, v1: vd1})
		first := s.allocate(schedClaim("a", selCompute))
		require.Len(t, first, 1)
		second := s.allocate(schedClaim("b", selVFIO))
		require.Len(t, second, 1)
		other := map[string]string{c0: v1, c1: v0}
		require.Equal(t, other[first[0]], second[0], "VFIO must land on the GPU the compute claim did not take")
		require.Nil(t, s.allocate(schedClaim("c", selVFIO)), "no GPU left for a third claim")
	})

	t.Run("SR-IOV PF: compute then VFIO", func(t *testing.T) {
		cName, c, vName, v := dualGPU(0, 4)
		s := newScheduler(t, AllocatableDevices{cName: c, vName: v})
		require.Equal(t, []string{cName}, s.allocate(schedClaim("a", selCompute)))
		require.Nil(t, s.allocate(schedClaim("b", selVFIO)))
	})
}

// TestScheduler_MixedComputeVFIOAllocationForOneVF covers the scenario the
// review explicitly asked for: a single SR-IOV VF advertised as both a
// compute device (IsVF=true) and a VFIO device (IsVF=true) sharing one PCI
// address. The scheduler must not allocate both to different claims.
func TestScheduler_MixedComputeVFIOAllocationForOneVF(t *testing.T) {
	const pf, vf = "0000:0a:00.0", "0000:0b:00.1"
	build := func() AllocatableDevices {
		return AllocatableDevices{
			"gpu-1-129":  {AmdGpu: &AmdGpuInfo{PCIAddress: vf, ParentPFAddress: pf, TotalVFs: 4, IsVF: true, cardIndex: 1, renderIndex: 129}},
			"gpu-vfio-1": {Vfio: &AmdGpuVFIOInfo{PCIAddress: vf, ParentPFAddress: pf, TotalVFs: 4, IsVF: true, Index: 1}},
		}
	}

	t.Run("compute claimed first blocks VFIO", func(t *testing.T) {
		s := newScheduler(t, build())
		require.Equal(t, []string{"gpu-1-129"}, s.allocate(schedClaim("a", selCompute)))
		require.Nil(t, s.allocate(schedClaim("b", selVFIOVF)), "the VF's VFIO entry must not be allocatable once its compute entry is")
	})

	t.Run("VFIO claimed first blocks compute", func(t *testing.T) {
		s := newScheduler(t, build())
		require.Equal(t, []string{"gpu-vfio-1"}, s.allocate(schedClaim("a", selVFIOVF)))
		require.Nil(t, s.allocate(schedClaim("b", selCompute)), "the VF's compute entry must not be allocatable once its VFIO entry is")
	})
}

func TestScheduler_PFVFExclusion(t *testing.T) {
	pf := gpuPCI(0)
	build := func() AllocatableDevices {
		devs := AllocatableDevices{
			"gpu-vfio-0": {Vfio: &AmdGpuVFIOInfo{PCIAddress: pf, ParentPFAddress: pf, TotalVFs: 2, Index: 0}},
		}
		for i := 1; i <= 2; i++ {
			devs[fmt.Sprintf("gpu-vfio-%d", i)] = &AllocatableDevice{Vfio: &AmdGpuVFIOInfo{
				PCIAddress: fmt.Sprintf("0000:0b:00.%d", i), ParentPFAddress: pf, TotalVFs: 2, IsVF: true, Index: i,
			}}
		}
		return devs
	}

	t.Run("VFs fill the PF's slots, then the PF is unavailable", func(t *testing.T) {
		s := newScheduler(t, build())
		require.Len(t, s.allocate(schedClaim("a", selVFIOVF)), 1)
		require.Len(t, s.allocate(schedClaim("b", selVFIOVF)), 1)
		require.Nil(t, s.allocate(schedClaim("c", selVFIOVF)), "only TotalVFs VFs fit")
		require.Nil(t, s.allocate(schedClaim("d", selVFIOPF)), "the PF needs every slot")
	})

	t.Run("an allocated PF blocks every VF", func(t *testing.T) {
		s := newScheduler(t, build())
		require.Equal(t, []string{"gpu-vfio-0"}, s.allocate(schedClaim("a", selVFIOPF)))
		require.Nil(t, s.allocate(schedClaim("b", selVFIOVF)))
	})
}
