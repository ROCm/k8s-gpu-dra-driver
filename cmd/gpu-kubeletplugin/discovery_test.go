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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/dynamic-resource-allocation/deviceattribute"

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/featuregates"
)

func TestNUMAAttributeForm(t *testing.T) {
	assert.Equal(t, deviceattribute.ScalarAttribute, numaAttributeForm(false))
	assert.Equal(t, deviceattribute.ListAttribute, numaAttributeForm(true))
}

func TestConfiguredNUMAAttributeForm(t *testing.T) {
	fg := featuregates.FeatureGates()
	wasEnabled := fg.Enabled(featuregates.DRAListTypeAttributes)
	restore := "false"
	if wasEnabled {
		restore = "true"
	}
	t.Cleanup(func() {
		require.NoError(t, fg.Set(string(featuregates.DRAListTypeAttributes)+"="+restore))
	})

	require.NoError(t, fg.Set(string(featuregates.DRAListTypeAttributes)+"=false"))
	assert.Equal(t, deviceattribute.ScalarAttribute, configuredNUMAAttributeForm())

	require.NoError(t, fg.Set(string(featuregates.DRAListTypeAttributes)+"=true"))
	assert.Equal(t, deviceattribute.ListAttribute, configuredNUMAAttributeForm())
}

// TestGetPcieInfoKeepsNUMAWhenPCIeRootFails covers a sysfs layout where the
// PCIe-root symlink resolution fails (e.g. an unsupported device path) but
// numa_node is still directly readable. Both lookups must run independently
// so the NUMA data survives the PCIe-root failure.
func TestGetPcieInfoKeepsNUMAWhenPCIeRootFails(t *testing.T) {
	const pciAddr = "0000:04:1f.0"
	root := t.TempDir()
	devDir := filepath.Join(root, "bus", "pci", "devices", pciAddr)
	require.NoError(t, os.MkdirAll(devDir, 0o755))
	// A plain directory, not a symlink into /sys/devices, so
	// GetPCIeRootAttributeByPCIBusID fails to resolve the PCIe root.
	require.NoError(t, os.WriteFile(filepath.Join(devDir, "numa_node"), []byte("7\n"), 0o644))

	topo, err := getPcieInfo(pciAddr, deviceattribute.WithFSFromRoot(root))

	require.Error(t, err)
	assert.Empty(t, topo.pcieRoot.Name, "PCIe root attribute should be empty on resolution failure")
	require.NotEmpty(t, topo.numaNode.Name, "NUMA attribute should survive a PCIe-root failure")
	require.NotNil(t, topo.numaNode.Value.IntValue)
	assert.Equal(t, int64(7), *topo.numaNode.Value.IntValue)
}

func TestGetMemoryBytes(t *testing.T) {
	withVram := map[string]interface{}{"vramBytes": uint64(16 * 1024 * 1024 * 1024)}
	assert.Equal(t, uint64(16*1024*1024*1024), getMemoryBytes(withVram, "device", "0000:00:00.0"))

	// Unreadable VRAM reports 0 instead of a fabricated capacity.
	assert.Equal(t, uint64(0), getMemoryBytes(map[string]interface{}{}, "device", "0000:00:00.0"))
	assert.Equal(t, uint64(0), getMemoryBytes(map[string]interface{}{"vramBytes": uint64(0)}, "partition", "0000:00:00.0"))
}
