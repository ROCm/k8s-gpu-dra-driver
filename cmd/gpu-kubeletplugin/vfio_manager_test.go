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

		vm := &VfioPciManager{}
		info := &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0"}

		err := vm.Configure(info)
		assert.NoError(t, err)
		assert.Equal(t, "vfio99", info.IommuFDCdev)
	})

	t.Run("no IommuFDCdev without vfio-dev", func(t *testing.T) {
		root := setupFakeVfioSysfs(t)
		createPCIDevice(t, root, "0000:0d:00.0", "")
		createDriverDir(t, root, "vfio-pci")

		vm := &VfioPciManager{}
		info := &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0"}

		err := vm.Configure(info)
		assert.NoError(t, err)
		assert.Equal(t, "", info.IommuFDCdev)
	})
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

func TestUseIommuFD(t *testing.T) {
	withCdev := &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0", IommuFDCdev: "vfio5"}
	noCdev := &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0"}

	tests := map[string]struct {
		info           *AmdGpuVFIOInfo
		preferIommuFD  bool
		iommuFDEnabled bool
		expected       bool
	}{
		"legacy policy":                          {info: withCdev, preferIommuFD: false, iommuFDEnabled: true, expected: false},
		"preferred, host enabled, cdev present":  {info: withCdev, preferIommuFD: true, iommuFDEnabled: true, expected: true},
		"preferred, host disabled, cdev present": {info: withCdev, preferIommuFD: true, iommuFDEnabled: false, expected: false},
		"preferred, host enabled, no cdev":       {info: noCdev, preferIommuFD: true, iommuFDEnabled: true, expected: false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.expected, UseIommuFD(tc.info, tc.preferIommuFD, tc.iommuFDEnabled))
		})
	}
}

func TestGetVfioCommonCDIEdits(t *testing.T) {
	t.Run("legacy uses /dev/vfio/vfio", func(t *testing.T) {
		edits := GetVfioCommonCDIEdits(false)
		require.Len(t, edits.ContainerEdits.DeviceNodes, 1)
		assert.Equal(t, "/dev/vfio/vfio", edits.ContainerEdits.DeviceNodes[0].Path)
	})

	t.Run("iommufd uses /dev/iommu", func(t *testing.T) {
		edits := GetVfioCommonCDIEdits(true)
		require.Len(t, edits.ContainerEdits.DeviceNodes, 1)
		assert.Equal(t, "/dev/iommu", edits.ContainerEdits.DeviceNodes[0].Path)
	})
}

func TestGetVfioDeviceCDIEdits(t *testing.T) {
	t.Run("legacy group path", func(t *testing.T) {
		info := &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0", IOMMUGroup: "42"}
		edits, err := GetVfioDeviceCDIEdits(info, false)
		require.NoError(t, err)
		require.Len(t, edits.ContainerEdits.DeviceNodes, 1)
		assert.Equal(t, "/dev/vfio/42", edits.ContainerEdits.DeviceNodes[0].Path)
	})

	t.Run("iommufd cdev path", func(t *testing.T) {
		info := &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0", IOMMUGroup: "42", IommuFDCdev: "vfio5"}
		edits, err := GetVfioDeviceCDIEdits(info, true)
		require.NoError(t, err)
		require.Len(t, edits.ContainerEdits.DeviceNodes, 1)
		assert.Equal(t, "/dev/vfio/devices/vfio5", edits.ContainerEdits.DeviceNodes[0].Path)
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

	t.Run("path-only node when device attrs unreadable", func(t *testing.T) {
		info := &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0", IOMMUGroup: "42"}
		edits, err := GetVfioDeviceCDIEdits(info, false)
		require.NoError(t, err)
		node := edits.ContainerEdits.DeviceNodes[0]
		assert.Equal(t, node.Path, node.HostPath)
		assert.Equal(t, "c", node.Type)
	})
}

// TestApplyVFIOConfig_BackendConsistency checks that the per-device node and
// the common API node always come from the same IOMMU backend.
func TestApplyVFIOConfig_BackendConsistency(t *testing.T) {
	tests := map[string]struct {
		policy         configapi.IOMMUBackendPolicy
		iommuFDEnabled bool
		hasCdev        bool
		expectedNodes  []string
	}{
		"legacy policy": {
			policy: configapi.IOMMUBackendPolicyLegacyOnly, iommuFDEnabled: true, hasCdev: true,
			expectedNodes: []string{"dev/vfio/42", "dev/vfio/vfio"},
		},
		"iommufd fully available": {
			policy: configapi.IOMMUBackendPolicyPreferIommuFD, iommuFDEnabled: true, hasCdev: true,
			expectedNodes: []string{"dev/vfio/devices/vfio5", "dev/iommu"},
		},
		"cdev present, host capability false": {
			policy: configapi.IOMMUBackendPolicyPreferIommuFD, iommuFDEnabled: false, hasCdev: true,
			expectedNodes: []string{"dev/vfio/42", "dev/vfio/vfio"},
		},
		"host capable, no cdev": {
			policy: configapi.IOMMUBackendPolicyPreferIommuFD, iommuFDEnabled: true, hasCdev: false,
			expectedNodes: []string{"dev/vfio/42", "dev/vfio/vfio"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			root := setupFakeVfioSysfs(t)
			createPCIDevice(t, root, "0000:0d:00.0", "vfio-pci")
			createDriverDir(t, root, "vfio-pci")
			if tc.hasCdev {
				require.NoError(t, os.MkdirAll(
					filepath.Join(root, "sys/bus/pci/devices/0000:0d:00.0/vfio-dev/vfio5"), 0755))
			}

			state := &DeviceState{
				allocatable: AllocatableDevices{
					"gpu-vfio-0": {Vfio: &AmdGpuVFIOInfo{PCIAddress: "0000:0d:00.0", IOMMUGroup: "42"}},
				},
				vfioManager: &VfioPciManager{iommuFDEnabled: tc.iommuFDEnabled},
			}
			config := &configapi.VfioDeviceConfig{Iommu: &configapi.IOMMUConfig{BackendPolicy: tc.policy}}

			edits, err := state.applyVFIOConfig(&resourceapi.DeviceRequestAllocationResult{Device: "gpu-vfio-0"}, config)
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
