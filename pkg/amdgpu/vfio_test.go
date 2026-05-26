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

package amdgpu

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupFakeSysfs(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	SetSysfsRoot(root)
	t.Cleanup(ResetSysfsRoot)
	return root
}

func createFakePCIDevice(t *testing.T, root, pciAddr string, files map[string]string, symlinks map[string]string) string {
	t.Helper()
	devPath := filepath.Join(root, "sys/bus/pci/devices", pciAddr)
	require.NoError(t, os.MkdirAll(devPath, 0755))
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(devPath, name), []byte(content), 0644))
	}
	for name, target := range symlinks {
		require.NoError(t, os.Symlink(target, filepath.Join(devPath, name)))
	}
	return devPath
}

func TestGetPCIDriver(t *testing.T) {
	tests := map[string]struct {
		symlinks       map[string]string
		expectedDriver string
	}{
		"driver bound": {
			symlinks:       map[string]string{"driver": "../../../../bus/pci/drivers/amdgpu"},
			expectedDriver: "amdgpu",
		},
		"no driver bound": {
			symlinks:       nil,
			expectedDriver: "",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			root := setupFakeSysfs(t)
			createFakePCIDevice(t, root, "0000:0d:00.0", nil, tc.symlinks)
			driver, err := GetPCIDriver("0000:0d:00.0")
			assert.NoError(t, err)
			assert.Equal(t, tc.expectedDriver, driver)
		})
	}
}

func TestGetPCIDriver_DeviceMissing(t *testing.T) {
	setupFakeSysfs(t)
	driver, err := GetPCIDriver("0000:ff:00.0")
	assert.NoError(t, err)
	assert.Equal(t, "", driver)
}

func TestGetIOMMUGroup(t *testing.T) {
	tests := map[string]struct {
		symlinks      map[string]string
		expectedGroup string
		expectErr     bool
	}{
		"valid iommu group": {
			symlinks:      map[string]string{"iommu_group": "../../../../kernel/iommu_groups/42"},
			expectedGroup: "42",
		},
		"no iommu group": {
			symlinks:  nil,
			expectErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			root := setupFakeSysfs(t)
			createFakePCIDevice(t, root, "0000:0d:00.0", nil, tc.symlinks)
			group, err := GetIOMMUGroup("0000:0d:00.0")
			if tc.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedGroup, group)
			}
		})
	}
}

func TestCheckVFIOModuleLoaded(t *testing.T) {
	t.Run("module loaded", func(t *testing.T) {
		root := setupFakeSysfs(t)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "sys/module/vfio_pci"), 0755))
		assert.True(t, CheckVFIOModuleLoaded())
	})

	t.Run("module not loaded", func(t *testing.T) {
		setupFakeSysfs(t)
		assert.False(t, CheckVFIOModuleLoaded())
	})
}

func TestCheckIOMMUEnabled(t *testing.T) {
	t.Run("enabled", func(t *testing.T) {
		root := setupFakeSysfs(t)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "sys/kernel/iommu_groups/0"), 0755))
		assert.True(t, CheckIOMMUEnabled())
	})

	t.Run("disabled - empty dir", func(t *testing.T) {
		root := setupFakeSysfs(t)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "sys/kernel/iommu_groups"), 0755))
		assert.False(t, CheckIOMMUEnabled())
	})

	t.Run("disabled - missing dir", func(t *testing.T) {
		setupFakeSysfs(t)
		assert.False(t, CheckIOMMUEnabled())
	})
}

func TestCheckGIMDriverLoaded(t *testing.T) {
	t.Run("loaded", func(t *testing.T) {
		root := setupFakeSysfs(t)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "sys/bus/pci/drivers/gim"), 0755))
		assert.True(t, CheckGIMDriverLoaded())
	})

	t.Run("not loaded", func(t *testing.T) {
		setupFakeSysfs(t)
		assert.False(t, CheckGIMDriverLoaded())
	})
}

func TestCheckVFIODriverLoaded(t *testing.T) {
	t.Run("loaded", func(t *testing.T) {
		root := setupFakeSysfs(t)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "sys/bus/pci/drivers/vfio-pci"), 0755))
		assert.True(t, CheckVFIODriverLoaded())
	})

	t.Run("not loaded", func(t *testing.T) {
		setupFakeSysfs(t)
		assert.False(t, CheckVFIODriverLoaded())
	})
}

func setupGIMPFWithVFs(t *testing.T, root, pfAddr string, vfAddrs []string, vfDrivers map[string]string) {
	t.Helper()
	pfPath := createFakePCIDevice(t, root, pfAddr,
		map[string]string{"vendor": AMDVendorID},
		map[string]string{"driver": "../../../../bus/pci/drivers/gim"},
	)

	for i, vfAddr := range vfAddrs {
		vfSymlinks := map[string]string{
			"physfn": filepath.Join("../", pfAddr),
		}
		if driver, ok := vfDrivers[vfAddr]; ok && driver != "" {
			vfSymlinks["driver"] = "../../../../bus/pci/drivers/" + driver
		}
		vfPath := createFakePCIDevice(t, root, vfAddr,
			map[string]string{
				"vendor": AMDVendorID,
				"device": "0x740f",
			},
			vfSymlinks,
		)

		iommuGroupDir := filepath.Join(root, "sys/kernel/iommu_groups", fmt.Sprintf("%d", 100+i))
		require.NoError(t, os.MkdirAll(iommuGroupDir, 0755))
		require.NoError(t, os.Symlink(iommuGroupDir, filepath.Join(vfPath, "iommu_group")))

		require.NoError(t, os.Symlink(filepath.Join("../", vfAddr), filepath.Join(pfPath, fmt.Sprintf("virtfn%d", i))))
	}
}

func TestGetVFMapping(t *testing.T) {
	t.Run("VF unbound is included", func(t *testing.T) {
		root := setupFakeSysfs(t)
		setupGIMPFWithVFs(t, root, "0000:0a:00.0", []string{"0000:0b:00.0"}, map[string]string{})
		vfMap, err := GetVFMapping()
		require.NoError(t, err)
		total := 0
		for _, vfs := range vfMap {
			total += len(vfs)
		}
		assert.Equal(t, 1, total)
	})

	t.Run("VF on vfio-pci is included", func(t *testing.T) {
		root := setupFakeSysfs(t)
		setupGIMPFWithVFs(t, root, "0000:0a:00.0", []string{"0000:0b:00.0"},
			map[string]string{"0000:0b:00.0": VFIODriverName})
		vfMap, err := GetVFMapping()
		require.NoError(t, err)
		total := 0
		for _, vfs := range vfMap {
			total += len(vfs)
		}
		assert.Equal(t, 1, total)
	})

	t.Run("VF on amdgpu is skipped", func(t *testing.T) {
		root := setupFakeSysfs(t)
		setupGIMPFWithVFs(t, root, "0000:0a:00.0", []string{"0000:0b:00.0"},
			map[string]string{"0000:0b:00.0": "amdgpu"})
		vfMap, err := GetVFMapping()
		require.NoError(t, err)
		total := 0
		for _, vfs := range vfMap {
			total += len(vfs)
		}
		assert.Equal(t, 0, total)
	})

	t.Run("mixed VFs - only eligible included", func(t *testing.T) {
		root := setupFakeSysfs(t)
		setupGIMPFWithVFs(t, root, "0000:0a:00.0",
			[]string{"0000:0b:00.0", "0000:0b:01.0", "0000:0b:02.0"},
			map[string]string{
				"0000:0b:00.0": "",         // unbound — included
				"0000:0b:01.0": "amdgpu",   // in use — skipped
				"0000:0b:02.0": "vfio-pci", // pre-bound — included
			})
		vfMap, err := GetVFMapping()
		require.NoError(t, err)
		total := 0
		for _, vfs := range vfMap {
			total += len(vfs)
		}
		assert.Equal(t, 2, total)
	})

	t.Run("non-AMD vendor skipped", func(t *testing.T) {
		root := setupFakeSysfs(t)
		createFakePCIDevice(t, root, "0000:0a:00.0",
			map[string]string{"vendor": "0x10de"},
			map[string]string{"driver": "../../../../bus/pci/drivers/gim"},
		)
		vfMap, err := GetVFMapping()
		require.NoError(t, err)
		assert.Empty(t, vfMap)
	})

	t.Run("PF not on GIM skipped", func(t *testing.T) {
		root := setupFakeSysfs(t)
		createFakePCIDevice(t, root, "0000:0a:00.0",
			map[string]string{"vendor": AMDVendorID},
			map[string]string{"driver": "../../../../bus/pci/drivers/amdgpu"},
		)
		vfMap, err := GetVFMapping()
		require.NoError(t, err)
		assert.Empty(t, vfMap)
	})
}

func TestGetPFMapping(t *testing.T) {
	t.Run("PF on vfio-pci discovered", func(t *testing.T) {
		root := setupFakeSysfs(t)
		devPath := createFakePCIDevice(t, root, "0000:0c:00.0",
			map[string]string{
				"vendor": AMDVendorID,
				"device": "0x740f",
			},
			map[string]string{"driver": "../../../../bus/pci/drivers/vfio-pci"},
		)
		iommuDir := filepath.Join(root, "sys/kernel/iommu_groups/50")
		require.NoError(t, os.MkdirAll(iommuDir, 0755))
		require.NoError(t, os.Symlink(iommuDir, filepath.Join(devPath, "iommu_group")))

		pfMap, err := GetPFMapping()
		require.NoError(t, err)
		total := 0
		for _, pfs := range pfMap {
			total += len(pfs)
		}
		assert.Equal(t, 1, total)
	})

	t.Run("VF with physfn skipped", func(t *testing.T) {
		root := setupFakeSysfs(t)
		devPath := createFakePCIDevice(t, root, "0000:0c:00.0",
			map[string]string{
				"vendor": AMDVendorID,
				"device": "0x740f",
			},
			map[string]string{
				"driver": "../../../../bus/pci/drivers/vfio-pci",
				"physfn": "../0000:0a:00.0",
			},
		)
		iommuDir := filepath.Join(root, "sys/kernel/iommu_groups/50")
		require.NoError(t, os.MkdirAll(iommuDir, 0755))
		require.NoError(t, os.Symlink(iommuDir, filepath.Join(devPath, "iommu_group")))

		pfMap, err := GetPFMapping()
		require.NoError(t, err)
		assert.Empty(t, pfMap)
	})

	t.Run("non-vfio driver skipped", func(t *testing.T) {
		root := setupFakeSysfs(t)
		createFakePCIDevice(t, root, "0000:0c:00.0",
			map[string]string{"vendor": AMDVendorID},
			map[string]string{"driver": "../../../../bus/pci/drivers/amdgpu"},
		)
		pfMap, err := GetPFMapping()
		require.NoError(t, err)
		assert.Empty(t, pfMap)
	})
}
