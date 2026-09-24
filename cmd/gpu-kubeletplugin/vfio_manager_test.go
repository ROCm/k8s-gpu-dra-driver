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

	configapi "github.com/ROCm/k8s-gpu-dra-driver/api/amd.com/resource/gpu/v1alpha1"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/amdgpu"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
)

func setupFakeVfioSysfs(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	amdgpu.SetSysfsRoot(root)
	t.Cleanup(amdgpu.ResetSysfsRoot)
	return root
}

func createDriverDir(t *testing.T, root, driverName string) {
	t.Helper()
	driverDir := filepath.Join(root, "sys/bus/pci/drivers", driverName)
	require.NoError(t, os.MkdirAll(driverDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(driverDir, "bind"), nil, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(driverDir, "unbind"), nil, 0644))
}

func createPCIDevice(t *testing.T, root, pciAddr string, driverName string) {
	t.Helper()
	devPath := filepath.Join(root, "sys/bus/pci/devices", pciAddr)
	require.NoError(t, os.MkdirAll(devPath, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(devPath, "driver_override"), nil, 0644))
	if driverName != "" {
		require.NoError(t, os.Symlink(
			"../../../../bus/pci/drivers/"+driverName,
			filepath.Join(devPath, "driver"),
		))
	}
}

func TestIsValidDriverName(t *testing.T) {
	tests := map[string]struct {
		input    string
		expected bool
	}{
		"valid simple":       {input: "vfio-pci", expected: true},
		"valid underscore":   {input: "vfio_pci", expected: true},
		"valid alphanumeric": {input: "amdgpu123", expected: true},
		"empty":              {input: "", expected: false},
		"dot":                {input: ".", expected: false},
		"dotdot":             {input: "..", expected: false},
		"path traversal":     {input: "../etc", expected: false},
		"has slash":          {input: "foo/bar", expected: false},
		"has space":          {input: "foo bar", expected: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.expected, isValidDriverName(tc.input))
		})
	}
}

func TestBindToDriver(t *testing.T) {
	t.Run("successful bind clears driver_override", func(t *testing.T) {
		root := setupFakeVfioSysfs(t)
		createPCIDevice(t, root, "0000:0d:00.0", "")
		createDriverDir(t, root, "vfio-pci")

		err := bindToDriver("0000:0d:00.0", "vfio-pci")
		require.NoError(t, err)

		bindContent, err := os.ReadFile(filepath.Join(root, "sys/bus/pci/drivers/vfio-pci/bind"))
		require.NoError(t, err)
		assert.Equal(t, "0000:0d:00.0", string(bindContent))

		overrideContent, err := os.ReadFile(filepath.Join(root, "sys/bus/pci/devices/0000:0d:00.0/driver_override"))
		require.NoError(t, err)
		assert.Equal(t, "\n", string(overrideContent), "driver_override should be cleared after successful bind")
	})

	t.Run("bind failure clears driver_override", func(t *testing.T) {
		root := setupFakeVfioSysfs(t)
		createPCIDevice(t, root, "0000:0d:00.0", "")
		driverDir := filepath.Join(root, "sys/bus/pci/drivers/vfio-pci")
		require.NoError(t, os.MkdirAll(driverDir, 0755))
		// Make bind file unwritable
		require.NoError(t, os.WriteFile(filepath.Join(driverDir, "bind"), nil, 0444))

		err := bindToDriver("0000:0d:00.0", "vfio-pci")
		assert.Error(t, err)

		overrideContent, err := os.ReadFile(filepath.Join(root, "sys/bus/pci/devices/0000:0d:00.0/driver_override"))
		require.NoError(t, err)
		assert.Equal(t, "\n", string(overrideContent), "driver_override should be cleared on bind failure")
	})
}

func TestUnbindFromDriver(t *testing.T) {
	t.Run("successful unbind", func(t *testing.T) {
		root := setupFakeVfioSysfs(t)
		createPCIDevice(t, root, "0000:0d:00.0", "amdgpu")
		createDriverDir(t, root, "amdgpu")

		err := unbindFromDriver("0000:0d:00.0")
		require.NoError(t, err)

		unbindContent, err := os.ReadFile(filepath.Join(root, "sys/bus/pci/drivers/amdgpu/unbind"))
		require.NoError(t, err)
		assert.Equal(t, "0000:0d:00.0", string(unbindContent))
	})

	t.Run("no driver bound is no-op", func(t *testing.T) {
		root := setupFakeVfioSysfs(t)
		createPCIDevice(t, root, "0000:0d:00.0", "")

		err := unbindFromDriver("0000:0d:00.0")
		assert.NoError(t, err)
	})
}

func TestConfigure(t *testing.T) {
	t.Run("already on vfio-pci is no-op", func(t *testing.T) {
		root := setupFakeVfioSysfs(t)
		createPCIDevice(t, root, "0000:0d:00.0", "vfio-pci")
		createDriverDir(t, root, "vfio-pci")

		vm := &VfioPciManager{}
		info := &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0", preConfigureDriver: "vfio-pci"}

		err := vm.Configure(info)
		assert.NoError(t, err)
		assert.Equal(t, "vfio-pci", info.preConfigureDriver)
	})

	t.Run("bind from unbound", func(t *testing.T) {
		root := setupFakeVfioSysfs(t)
		createPCIDevice(t, root, "0000:0d:00.0", "")
		createDriverDir(t, root, "vfio-pci")

		vm := &VfioPciManager{}
		info := &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0"}

		err := vm.Configure(info)
		assert.NoError(t, err)
		assert.Equal(t, "", info.preConfigureDriver)

		bindContent, err := os.ReadFile(filepath.Join(root, "sys/bus/pci/drivers/vfio-pci/bind"))
		require.NoError(t, err)
		assert.Equal(t, "0000:0d:00.0", string(bindContent))
	})

	t.Run("rebind from amdgpu", func(t *testing.T) {
		root := setupFakeVfioSysfs(t)
		createPCIDevice(t, root, "0000:0d:00.0", "amdgpu")
		createDriverDir(t, root, "amdgpu")
		createDriverDir(t, root, "vfio-pci")

		vm := &VfioPciManager{}
		info := &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0", preConfigureDriver: "amdgpu"}

		err := vm.Configure(info)
		assert.NoError(t, err)
		assert.Equal(t, "amdgpu", info.preConfigureDriver)

		unbindContent, err := os.ReadFile(filepath.Join(root, "sys/bus/pci/drivers/amdgpu/unbind"))
		require.NoError(t, err)
		assert.Equal(t, "0000:0d:00.0", string(unbindContent))

		bindContent, err := os.ReadFile(filepath.Join(root, "sys/bus/pci/drivers/vfio-pci/bind"))
		require.NoError(t, err)
		assert.Equal(t, "0000:0d:00.0", string(bindContent))
	})

	t.Run("populates IommuFDCdev after bind", func(t *testing.T) {
		root := setupFakeVfioSysfs(t)
		createPCIDevice(t, root, "0000:0d:00.0", "")
		createDriverDir(t, root, "vfio-pci")
		vfioDevDir := filepath.Join(root, "sys/bus/pci/devices/0000:0d:00.0/vfio-dev/vfio99")
		require.NoError(t, os.MkdirAll(vfioDevDir, 0755))

		vm := &VfioPciManager{iommuFDEnabled: true}
		info := &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0"}

		err := vm.Configure(info)
		assert.NoError(t, err)
		assert.Equal(t, "vfio99", info.IommuFDCdev)
	})

	t.Run("no IommuFDCdev without vfio-dev", func(t *testing.T) {
		root := setupFakeVfioSysfs(t)
		createPCIDevice(t, root, "0000:0d:00.0", "")
		createDriverDir(t, root, "vfio-pci")

		vm := &VfioPciManager{iommuFDEnabled: true}
		info := &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0", IommuFDCdev: "vfio7"}

		err := vm.Configure(info)
		assert.NoError(t, err)
		assert.Equal(t, "", info.IommuFDCdev, "stale cdev must be cleared when lookup fails")
	})

	t.Run("skips cdev lookup when host lacks IOMMUFD", func(t *testing.T) {
		root := setupFakeVfioSysfs(t)
		createPCIDevice(t, root, "0000:0d:00.0", "vfio-pci")
		createDriverDir(t, root, "vfio-pci")
		require.NoError(t, os.MkdirAll(
			filepath.Join(root, "sys/bus/pci/devices/0000:0d:00.0/vfio-dev/vfio99"), 0755))

		vm := &VfioPciManager{iommuFDEnabled: false}
		info := &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0"}

		err := vm.Configure(info)
		assert.NoError(t, err)
		assert.Equal(t, "", info.IommuFDCdev)
	})
}

func TestUnconfigure_ClearsIommuFDCdev(t *testing.T) {
	root := setupFakeVfioSysfs(t)
	createPCIDevice(t, root, "0000:0d:00.0", "vfio-pci")
	createDriverDir(t, root, "vfio-pci")

	vm := &VfioPciManager{iommuFDEnabled: true}
	info := &AmdGpuVFIOInfo{
		PCIAddress:         "0000:0d:00.0",
		preConfigureDriver: "vfio-pci",
		IommuFDCdev:        "vfio5",
	}

	require.NoError(t, vm.Unconfigure(info))
	assert.Equal(t, "", info.IommuFDCdev)
}

func TestVfioPciManager_IommuFDEnabled(t *testing.T) {
	t.Run("enabled when /dev/iommu exists", func(t *testing.T) {
		root := setupFakeVfioSysfs(t)
		iommuPath := filepath.Join(root, "dev/iommu")
		require.NoError(t, os.MkdirAll(filepath.Dir(iommuPath), 0755))
		require.NoError(t, os.WriteFile(iommuPath, nil, 0644))
		iommuGroupDir := filepath.Join(root, "sys/kernel/iommu_groups/1")
		require.NoError(t, os.MkdirAll(iommuGroupDir, 0755))

		vm, err := NewVfioPciManager()
		assert.NoError(t, err)
		assert.True(t, vm.iommuFDEnabled)
	})

	t.Run("disabled when /dev/iommu missing", func(t *testing.T) {
		root := setupFakeVfioSysfs(t)
		iommuGroupDir := filepath.Join(root, "sys/kernel/iommu_groups/1")
		require.NoError(t, os.MkdirAll(iommuGroupDir, 0755))

		vm, err := NewVfioPciManager()
		assert.NoError(t, err)
		assert.False(t, vm.iommuFDEnabled)
	})
}

func TestUnconfigure(t *testing.T) {
	t.Run("pre-bound to vfio-pci is no-op", func(t *testing.T) {
		setupFakeVfioSysfs(t)
		vm := &VfioPciManager{}
		info := &AmdGpuVFIOInfo{
			PCIAddress:         "0000:0d:00.0",
			preConfigureDriver: "vfio-pci",
		}

		err := vm.Unconfigure(info)
		assert.NoError(t, err)
	})

	t.Run("pre-unbound device not on vfio is no-op", func(t *testing.T) {
		root := setupFakeVfioSysfs(t)
		// Device exists but has no driver (unbound)
		devPath := filepath.Join(root, "sys/bus/pci/devices/0000:0d:00.0")
		require.NoError(t, os.MkdirAll(devPath, 0755))

		vm := &VfioPciManager{}
		info := &AmdGpuVFIOInfo{
			PCIAddress:         "0000:0d:00.0",
			preConfigureDriver: "",
		}

		err := vm.Unconfigure(info)
		assert.NoError(t, err)
	})

	t.Run("pre-unbound device on vfio gets unbound", func(t *testing.T) {
		root := setupFakeVfioSysfs(t)
		createPCIDevice(t, root, "0000:0d:00.0", "vfio-pci")
		createDriverDir(t, root, "vfio-pci")

		vm := &VfioPciManager{}
		info := &AmdGpuVFIOInfo{
			PCIAddress:         "0000:0d:00.0",
			preConfigureDriver: "",
		}

		err := vm.Unconfigure(info)
		assert.NoError(t, err)

		// Should have written to unbind
		unbindContent, err := os.ReadFile(filepath.Join(root, "sys/bus/pci/drivers/vfio-pci/unbind"))
		require.NoError(t, err)
		assert.Equal(t, "0000:0d:00.0", string(unbindContent))
	})

	t.Run("rebind to original driver", func(t *testing.T) {
		root := setupFakeVfioSysfs(t)
		createPCIDevice(t, root, "0000:0d:00.0", "vfio-pci")
		createDriverDir(t, root, "vfio-pci")
		createDriverDir(t, root, "amdgpu")

		vm := &VfioPciManager{}
		info := &AmdGpuVFIOInfo{
			PCIAddress:         "0000:0d:00.0",
			preConfigureDriver: "amdgpu",
		}

		err := vm.Unconfigure(info)
		assert.NoError(t, err)

		unbindContent, err := os.ReadFile(filepath.Join(root, "sys/bus/pci/drivers/vfio-pci/unbind"))
		require.NoError(t, err)
		assert.Equal(t, "0000:0d:00.0", string(unbindContent))

		bindContent, err := os.ReadFile(filepath.Join(root, "sys/bus/pci/drivers/amdgpu/bind"))
		require.NoError(t, err)
		assert.Equal(t, "0000:0d:00.0", string(bindContent))
	})
}

// createDevNode creates a fake device node at root/rel as a symlink to
// /dev/null, which os.Stat resolves to a real character device (1:3).
func createDevNode(t *testing.T, root, rel string) {
	t.Helper()
	path := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.Symlink("/dev/null", path))
}

func TestUseIommuFD(t *testing.T) {
	legacy := configapi.IOMMUBackendPolicyLegacyOnly
	prefer := configapi.IOMMUBackendPolicyPreferIommuFD
	requireFD := configapi.IOMMUBackendPolicyRequireIommuFD
	allNodes := []string{"dev/iommu", "dev/vfio/devices/vfio5"}

	tests := map[string]struct {
		cdev           string
		nodes          []string
		policy         configapi.IOMMUBackendPolicy
		iommuFDEnabled bool
		expected       bool
		expectErr      bool
	}{
		"legacy policy ignores IOMMUFD": {cdev: "vfio5", nodes: allNodes, policy: legacy, iommuFDEnabled: true},
		"prefer, fully available":       {cdev: "vfio5", nodes: allNodes, policy: prefer, iommuFDEnabled: true, expected: true},
		"prefer, host disabled":         {cdev: "vfio5", nodes: allNodes, policy: prefer, iommuFDEnabled: false},
		"prefer, no sysfs cdev":         {cdev: "", nodes: allNodes, policy: prefer, iommuFDEnabled: true},
		"prefer, cdev node missing":     {cdev: "vfio5", nodes: []string{"dev/iommu"}, policy: prefer, iommuFDEnabled: true},
		"prefer, /dev/iommu missing":    {cdev: "vfio5", nodes: []string{"dev/vfio/devices/vfio5"}, policy: prefer, iommuFDEnabled: true},
		"require, fully available":      {cdev: "vfio5", nodes: allNodes, policy: requireFD, iommuFDEnabled: true, expected: true},
		"require, host disabled":        {cdev: "vfio5", nodes: allNodes, policy: requireFD, iommuFDEnabled: false, expectErr: true},
		"require, no sysfs cdev":        {cdev: "", nodes: allNodes, policy: requireFD, iommuFDEnabled: true, expectErr: true},
		"require, cdev node missing":    {cdev: "vfio5", nodes: []string{"dev/iommu"}, policy: requireFD, iommuFDEnabled: true, expectErr: true},
		"require, /dev/iommu missing":   {cdev: "vfio5", nodes: []string{"dev/vfio/devices/vfio5"}, policy: requireFD, iommuFDEnabled: true, expectErr: true},
		"unknown policy":                {cdev: "vfio5", nodes: allNodes, policy: "Bogus", iommuFDEnabled: true, expectErr: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			root := setupFakeVfioSysfs(t)
			for _, n := range tc.nodes {
				createDevNode(t, root, n)
			}
			info := &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0", IommuFDCdev: tc.cdev}

			got, err := UseIommuFD(info, tc.policy, tc.iommuFDEnabled)
			if tc.expectErr {
				assert.Error(t, err)
				assert.False(t, got)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestGetVfioCommonCDIEdits(t *testing.T) {
	for name, tc := range map[string]struct {
		useIommuFD bool
		node       string
	}{
		"legacy uses /dev/vfio/vfio": {useIommuFD: false, node: "dev/vfio/vfio"},
		"iommufd uses /dev/iommu":    {useIommuFD: true, node: "dev/iommu"},
	} {
		t.Run(name, func(t *testing.T) {
			root := setupFakeVfioSysfs(t)
			createDevNode(t, root, tc.node)

			edits, err := GetVfioCommonCDIEdits(tc.useIommuFD)
			require.NoError(t, err)
			require.Len(t, edits.ContainerEdits.DeviceNodes, 1)
			node := edits.ContainerEdits.DeviceNodes[0]
			assert.Equal(t, filepath.Join(root, tc.node), node.Path)
			assert.Equal(t, "c", node.Type)
			assert.Equal(t, int64(1), node.Major)
			assert.Equal(t, int64(3), node.Minor)
		})

		t.Run(name+" errors when node missing", func(t *testing.T) {
			setupFakeVfioSysfs(t)
			_, err := GetVfioCommonCDIEdits(tc.useIommuFD)
			assert.Error(t, err)
		})
	}
}

func TestGetVfioDeviceCDIEdits(t *testing.T) {
	t.Run("legacy group path", func(t *testing.T) {
		root := setupFakeVfioSysfs(t)
		createDevNode(t, root, "dev/vfio/42")
		info := &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0", IOMMUGroup: "42"}
		edits, err := GetVfioDeviceCDIEdits(info, false)
		require.NoError(t, err)
		require.Len(t, edits.ContainerEdits.DeviceNodes, 1)
		assert.Equal(t, filepath.Join(root, "dev/vfio/42"), edits.ContainerEdits.DeviceNodes[0].Path)
		assert.Equal(t, int64(1), edits.ContainerEdits.DeviceNodes[0].Major)
	})

	t.Run("iommufd cdev path", func(t *testing.T) {
		root := setupFakeVfioSysfs(t)
		createDevNode(t, root, "dev/vfio/devices/vfio5")
		info := &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0", IOMMUGroup: "42", IommuFDCdev: "vfio5"}
		edits, err := GetVfioDeviceCDIEdits(info, true)
		require.NoError(t, err)
		require.Len(t, edits.ContainerEdits.DeviceNodes, 1)
		assert.Equal(t, filepath.Join(root, "dev/vfio/devices/vfio5"), edits.ContainerEdits.DeviceNodes[0].Path)
	})

	t.Run("iommufd errors when cdev node missing", func(t *testing.T) {
		setupFakeVfioSysfs(t)
		info := &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0", IommuFDCdev: "vfio5"}
		_, err := GetVfioDeviceCDIEdits(info, true)
		assert.Error(t, err)
	})

	t.Run("legacy resolves missing IOMMU group from sysfs", func(t *testing.T) {
		root := setupFakeVfioSysfs(t)
		createPCIDevice(t, root, "0000:0d:00.0", "")
		require.NoError(t, os.Symlink("../../../kernel/iommu_groups/17",
			filepath.Join(root, "sys/bus/pci/devices/0000:0d:00.0/iommu_group")))

		info := &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0"}
		edits, err := GetVfioDeviceCDIEdits(info, false)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(root, "dev/vfio/17"), edits.ContainerEdits.DeviceNodes[0].Path)
	})

	t.Run("legacy errors when IOMMU group unresolvable", func(t *testing.T) {
		setupFakeVfioSysfs(t)
		info := &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0"}
		_, err := GetVfioDeviceCDIEdits(info, false)
		assert.Error(t, err)
	})

	t.Run("legacy rejects non-numeric IOMMU group", func(t *testing.T) {
		info := &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0", IOMMUGroup: "../etc"}
		_, err := GetVfioDeviceCDIEdits(info, false)
		assert.Error(t, err)
	})

	t.Run("legacy group node falls back to path-only", func(t *testing.T) {
		setupFakeVfioSysfs(t)
		info := &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0", IOMMUGroup: "42"}
		edits, err := GetVfioDeviceCDIEdits(info, false)
		require.NoError(t, err)
		node := edits.ContainerEdits.DeviceNodes[0]
		assert.Equal(t, node.Path, node.HostPath)
		assert.Equal(t, "c", node.Type)
		assert.Zero(t, node.Major)
	})
}

// TestApplyVFIOConfig_BackendConsistency checks that the per-device node and
// the common API node always come from the same IOMMU backend, that
// RequireIommuFD fails instead of falling back, and that a missing API node
// fails Prepare rather than producing an unusable spec.
func TestApplyVFIOConfig_BackendConsistency(t *testing.T) {
	legacyNodes := []string{"dev/vfio/42", "dev/vfio/vfio"}
	iommufdNodes := []string{"dev/vfio/devices/vfio5", "dev/iommu"}
	allNodes := append(append([]string{}, legacyNodes...), iommufdNodes...)

	tests := map[string]struct {
		policy         configapi.IOMMUBackendPolicy
		iommuFDEnabled bool
		sysfsCdev      bool
		nodes          []string
		expectedNodes  []string
		expectErr      bool
	}{
		"legacy policy": {
			policy: configapi.IOMMUBackendPolicyLegacyOnly, iommuFDEnabled: true, sysfsCdev: true,
			nodes: allNodes, expectedNodes: legacyNodes,
		},
		"legacy, /dev/vfio/vfio missing": {
			policy: configapi.IOMMUBackendPolicyLegacyOnly, iommuFDEnabled: true, sysfsCdev: true,
			nodes: []string{"dev/vfio/42"}, expectErr: true,
		},
		"prefer, fully available": {
			policy: configapi.IOMMUBackendPolicyPreferIommuFD, iommuFDEnabled: true, sysfsCdev: true,
			nodes: allNodes, expectedNodes: iommufdNodes,
		},
		"prefer, host capability false": {
			policy: configapi.IOMMUBackendPolicyPreferIommuFD, iommuFDEnabled: false, sysfsCdev: true,
			nodes: allNodes, expectedNodes: legacyNodes,
		},
		"prefer, no sysfs cdev": {
			policy: configapi.IOMMUBackendPolicyPreferIommuFD, iommuFDEnabled: true, sysfsCdev: false,
			nodes: allNodes, expectedNodes: legacyNodes,
		},
		"prefer, sysfs cdev but no device node": {
			policy: configapi.IOMMUBackendPolicyPreferIommuFD, iommuFDEnabled: true, sysfsCdev: true,
			nodes: append(append([]string{}, legacyNodes...), "dev/iommu"), expectedNodes: legacyNodes,
		},
		"require, fully available": {
			policy: configapi.IOMMUBackendPolicyRequireIommuFD, iommuFDEnabled: true, sysfsCdev: true,
			nodes: allNodes, expectedNodes: iommufdNodes,
		},
		"require, host capability false": {
			policy: configapi.IOMMUBackendPolicyRequireIommuFD, iommuFDEnabled: false, sysfsCdev: true,
			nodes: allNodes, expectErr: true,
		},
		"require, no sysfs cdev": {
			policy: configapi.IOMMUBackendPolicyRequireIommuFD, iommuFDEnabled: true, sysfsCdev: false,
			nodes: allNodes, expectErr: true,
		},
		"require, sysfs cdev but no device node": {
			policy: configapi.IOMMUBackendPolicyRequireIommuFD, iommuFDEnabled: true, sysfsCdev: true,
			nodes: append(append([]string{}, legacyNodes...), "dev/iommu"), expectErr: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			root := setupFakeVfioSysfs(t)
			createPCIDevice(t, root, "0000:0d:00.0", "vfio-pci")
			createDriverDir(t, root, "vfio-pci")
			if tc.sysfsCdev {
				require.NoError(t, os.MkdirAll(
					filepath.Join(root, "sys/bus/pci/devices/0000:0d:00.0/vfio-dev/vfio5"), 0755))
			}
			for _, n := range tc.nodes {
				createDevNode(t, root, n)
			}

			state := &DeviceState{
				allocatable: AllocatableDevices{
					"gpu-vfio-0": {Vfio: &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0", IOMMUGroup: "42"}},
				},
				vfioManager: &VfioPciManager{iommuFDEnabled: tc.iommuFDEnabled},
			}
			config := &configapi.VfioDeviceConfig{Iommu: &configapi.IOMMUConfig{BackendPolicy: tc.policy}}

			edits, err := state.applyVFIOConfig(&resourceapi.DeviceRequestAllocationResult{Device: "gpu-vfio-0"}, config)
			if tc.expectErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)

			var paths []string
			for _, n := range edits.ContainerEdits.DeviceNodes {
				paths = append(paths, n.Path)
			}
			var expected []string
			for _, p := range tc.expectedNodes {
				expected = append(expected, filepath.Join(root, p))
			}
			assert.Equal(t, expected, paths)
		})
	}
}
