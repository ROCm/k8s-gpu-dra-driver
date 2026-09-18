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

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/amdgpu"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetMemoryBytes(t *testing.T) {
	withVram := map[string]interface{}{"vramBytes": uint64(16 * 1024 * 1024 * 1024)}
	assert.Equal(t, uint64(16*1024*1024*1024), getMemoryBytes(withVram, "device", "0000:00:00.0"))

	// Unreadable VRAM reports 0 instead of a fabricated capacity.
	assert.Equal(t, uint64(0), getMemoryBytes(map[string]interface{}{}, "device", "0000:00:00.0"))
	assert.Equal(t, uint64(0), getMemoryBytes(map[string]interface{}{"vramBytes": uint64(0)}, "partition", "0000:00:00.0"))
}

func TestGetCounterIdentityForVF(t *testing.T) {
	root := t.TempDir()
	amdgpu.SetSysfsRoot(root)
	t.Cleanup(amdgpu.ResetSysfsRoot)

	devices := filepath.Join(root, "sys/bus/pci/devices")
	pf := filepath.Join(devices, "0000:01:00.0")
	vf := filepath.Join(devices, "0000:01:00.2")
	require.NoError(t, os.MkdirAll(pf, 0o755))
	require.NoError(t, os.MkdirAll(vf, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pf, "sriov_totalvfs"), []byte("8\n"), 0o644))
	require.NoError(t, os.Symlink("../0000:01:00.0", filepath.Join(vf, "physfn")))

	parent, total, isVF := getCounterIdentity("0000:01:00.2")
	assert.Equal(t, "0000:01:00.0", parent)
	assert.Equal(t, 8, total)
	assert.True(t, isVF)
}

func TestGetCounterIdentityForPF(t *testing.T) {
	root := t.TempDir()
	amdgpu.SetSysfsRoot(root)
	t.Cleanup(amdgpu.ResetSysfsRoot)

	pf := filepath.Join(root, "sys/bus/pci/devices/0000:01:00.0")
	require.NoError(t, os.MkdirAll(pf, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pf, "sriov_totalvfs"), []byte("4\n"), 0o644))

	parent, total, isVF := getCounterIdentity("0000:01:00.0")
	assert.Equal(t, "0000:01:00.0", parent)
	assert.Equal(t, 4, total)
	assert.False(t, isVF)
}
