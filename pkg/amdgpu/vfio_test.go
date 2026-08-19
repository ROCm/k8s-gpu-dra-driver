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

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/consts"

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
	assert.Error(t, err)
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
		map[string]string{"vendor": consts.AMDVendorID},
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
				"vendor": consts.AMDVendorID,
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
			map[string]string{"0000:0b:00.0": consts.VFIODriverName})
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
			map[string]string{"vendor": consts.AMDVendorID},
			map[string]string{"driver": "../../../../bus/pci/drivers/amdgpu"},
		)
		vfMap, err := GetVFMapping()
		require.NoError(t, err)
		assert.Empty(t, vfMap)
	})
}

func TestReadSRIOVNumVFs(t *testing.T) {
	tests := map[string]struct {
		fileContent string
		createFile  bool
		expected    int
	}{
		"8 VFs active": {fileContent: "8\n", createFile: true, expected: 8},
		"4 VFs active": {fileContent: "4\n", createFile: true, expected: 4},
		"1 VF active":  {fileContent: "1\n", createFile: true, expected: 1},
		"0 VFs active": {fileContent: "0\n", createFile: true, expected: 0},
		"file missing": {createFile: false, expected: 0},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			root := setupFakeSysfs(t)
			pciAddr := "0000:0a:00.0"
			files := map[string]string{}
			if tc.createFile {
				files["sriov_numvfs"] = tc.fileContent
			}
			createFakePCIDevice(t, root, pciAddr, files, nil)
			assert.Equal(t, tc.expected, ReadSRIOVNumVFs(pciAddr))
		})
	}
}

func TestReadPFCapacity(t *testing.T) {
	t.Run("all files present", func(t *testing.T) {
		root := setupFakeSysfs(t)
		pciAddr := "0000:0a:00.0"
		createFakePCIDevice(t, root, pciAddr, map[string]string{
			"mem_info_vram_total": "206158430208\n",
			"device":              "0x740f",
		}, nil)
		cap := ReadPFCapacity(pciAddr)
		assert.Equal(t, uint64(206158430208), cap[0])
		assert.Equal(t, uint64(304), cap[1])
		assert.Equal(t, uint64(1216), cap[2])
	})

	t.Run("unknown device ID", func(t *testing.T) {
		root := setupFakeSysfs(t)
		pciAddr := "0000:0a:00.0"
		createFakePCIDevice(t, root, pciAddr, map[string]string{
			"mem_info_vram_total": "1000000\n",
			"device":              "0x9999",
		}, nil)
		cap := ReadPFCapacity(pciAddr)
		assert.Equal(t, uint64(1000000), cap[0])
		assert.Equal(t, uint64(0), cap[1])
		assert.Equal(t, uint64(0), cap[2])
	})

	t.Run("missing files", func(t *testing.T) {
		root := setupFakeSysfs(t)
		pciAddr := "0000:0a:00.0"
		createFakePCIDevice(t, root, pciAddr, nil, nil)
		cap := ReadPFCapacity(pciAddr)
		assert.Equal(t, [3]uint64{0, 0, 0}, cap)
	})
}

func TestGpuCapacityByDeviceID(t *testing.T) {
	tests := map[string]struct {
		deviceID     string
		expectedCU   int
		expectedSIMD int
	}{
		"MI355X":  {deviceID: "0x75a3", expectedCU: 256, expectedSIMD: 1024},
		"MI300X":  {deviceID: "0x740f", expectedCU: 304, expectedSIMD: 1216},
		"MI325X":  {deviceID: "0x74a0", expectedCU: 304, expectedSIMD: 1216},
		"unknown": {deviceID: "0x9999", expectedCU: 0, expectedSIMD: 0},
		"empty":   {deviceID: "", expectedCU: 0, expectedSIMD: 0},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cu, simd := gpuCapacityByDeviceID(tc.deviceID)
			assert.Equal(t, tc.expectedCU, cu)
			assert.Equal(t, tc.expectedSIMD, simd)
		})
	}
}

func TestGetPFMapping(t *testing.T) {
	t.Run("PF on vfio-pci discovered", func(t *testing.T) {
		root := setupFakeSysfs(t)
		devPath := createFakePCIDevice(t, root, "0000:0c:00.0",
			map[string]string{
				"vendor": consts.AMDVendorID,
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
				"vendor": consts.AMDVendorID,
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
			map[string]string{"vendor": consts.AMDVendorID},
			map[string]string{"driver": "../../../../bus/pci/drivers/amdgpu"},
		)
		pfMap, err := GetPFMapping()
		require.NoError(t, err)
		assert.Empty(t, pfMap)
	})
}
