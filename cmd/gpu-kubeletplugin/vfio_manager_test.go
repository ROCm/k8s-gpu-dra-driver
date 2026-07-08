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

	t.Run("pre-unbound is no-op", func(t *testing.T) {
		setupFakeVfioSysfs(t)
		vm := &VfioPciManager{}
		info := &AmdGpuVFIOInfo{
			PCIAddress:         "0000:0d:00.0",
			preConfigureDriver: "",
		}

		err := vm.Unconfigure(info)
		assert.NoError(t, err)
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
