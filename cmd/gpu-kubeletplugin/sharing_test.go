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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"
)

// rx9060xt matches the RDNA4 card the GPUSharing gate was tried on.
func rx9060xt() *AmdGpuInfo {
	return &AmdGpuInfo{MemoryBytes: 16304 << 20, ComputeUnits: 32, SimdUnits: 64, cardIndex: 1, renderIndex: 129}
}

func consumed(mem string, cus int64) map[resourceapi.QualifiedName]resource.Quantity {
	return map[resourceapi.QualifiedName]resource.Quantity{
		"memory":       resource.MustParse(mem),
		"computeUnits": *resource.NewQuantity(cus, resource.DecimalSI),
	}
}

func TestGPUSliceEdits(t *testing.T) {
	edits, cus, err := gpuSliceEdits(rx9060xt(), consumed("2000Mi", 16), nil, "/usr/local/vgpu/libamvgpu.so")
	require.NoError(t, err)
	assert.Len(t, cus, 16)
	assert.Equal(t, []string{"HSA_CU_MASK=0:0-15", "HIP_DEVICE_MEMORY_LIMIT_0=2000m", "LD_AUDIT=" + vgpuHookContainerPath}, edits.Env)
	require.Len(t, edits.Mounts, 1)
	assert.Equal(t, "/usr/local/vgpu/libamvgpu.so", edits.Mounts[0].HostPath)
	assert.Equal(t, vgpuHookContainerPath, edits.Mounts[0].ContainerPath)
}

func TestGPUSliceEditsSkipsBusyCUs(t *testing.T) {
	busy := map[int]bool{}
	for cu := 0; cu < 16; cu++ {
		busy[cu] = true
	}
	edits, cus, err := gpuSliceEdits(rx9060xt(), consumed("2000Mi", 16), busy, "")
	require.NoError(t, err)
	assert.Equal(t, 16, cus[0])
	// No hook configured: the CU mask is set, the memory limit is not.
	assert.Equal(t, []string{"HSA_CU_MASK=0:16-31"}, edits.Env)
	assert.Empty(t, edits.Mounts)

	_, _, err = gpuSliceEdits(rx9060xt(), consumed("2000Mi", 17), busy, "")
	assert.ErrorContains(t, err, "need 17 free CUs, only 16 of 32 are free")
}

func TestGPUSliceEditsWholeGPU(t *testing.T) {
	edits, cus, err := gpuSliceEdits(rx9060xt(), consumed("16304Mi", 32), nil, "/hook")
	require.NoError(t, err)
	assert.Nil(t, edits)
	assert.Len(t, cus, 32)

	// A result without consumed capacity (gate off, or an older scheduler) is left alone.
	edits, cus, err = gpuSliceEdits(rx9060xt(), nil, nil, "/hook")
	require.NoError(t, err)
	assert.Nil(t, edits)
	assert.Nil(t, cus)
}

func TestFormatCUs(t *testing.T) {
	assert.Equal(t, "0-3,8,10-11", formatCUs([]int{0, 1, 2, 3, 8, 10, 11}))
	assert.Equal(t, "5", formatCUs([]int{5}))
}

func TestShareGPUDevice(t *testing.T) {
	device := rx9060xt().GetDevice()
	shareGPUDevice(&device, 32)
	assert.True(t, *device.AllowMultipleAllocations)
	cu := device.Capacity["computeUnits"].RequestPolicy
	assert.Equal(t, int64(32), cu.Default.Value())
	assert.Equal(t, int64(1), cu.ValidRange.Step.Value())
	assert.Equal(t, int64(0), device.Capacity["simdUnits"].RequestPolicy.Default.Value())
	// Memory stays unconstrained, so a request without it takes the whole GPU.
	assert.Nil(t, device.Capacity["memory"].RequestPolicy)
}

func TestApplyGPUSliceAvoidsPreparedShares(t *testing.T) {
	state := &DeviceState{
		allocatable:      AllocatableDevices{"gpu-1-129": {AmdGpu: rx9060xt()}},
		vgpuHookHostPath: "/usr/local/vgpu/libamvgpu.so",
	}
	first := make([]int, 16)
	for i := range first {
		first[i] = i
	}
	prepared := PreparedClaims{
		"claim-a": {{Device: drapbv1.Device{DeviceName: "gpu-1-129"}, ComputeUnits: first}},
	}
	device := &PreparedDevice{Device: drapbv1.Device{DeviceName: "gpu-1-129"}}
	result := &resourceapi.DeviceRequestAllocationResult{Device: "gpu-1-129", ConsumedCapacity: consumed("2000Mi", 16)}

	require.NoError(t, state.applyGPUSlice(device, result, prepared, nil))
	assert.Equal(t, 16, device.ComputeUnits[0])
	assert.Contains(t, device.ContainerEdits.Env, "HSA_CU_MASK=0:16-31")
}
