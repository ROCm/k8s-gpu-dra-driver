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
		map[string]string{
			"vendor":       consts.AMDVendorID,
			"sriov_numvfs": fmt.Sprintf("%d\n", len(vfAddrs)),
		},
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
			for _, vf := range vfs {
				assert.Equal(t, 1, vf.NumVFs)
			}
		}
		assert.Equal(t, 1, total)
	})

	t.Run("eight VFs remain eight distinct devices", func(t *testing.T) {
		root := setupFakeSysfs(t)
		vfAddrs := make([]string, 8)
		for i := range vfAddrs {
			vfAddrs[i] = fmt.Sprintf("0000:0b:%02x.0", i)
		}
		setupGIMPFWithVFs(t, root, "0000:0a:00.0", vfAddrs, nil)

		vfMap, err := GetVFMapping()
		require.NoError(t, err)
		seen := make(map[string]bool)
		for _, vfs := range vfMap {
			for _, vf := range vfs {
				assert.Equal(t, 8, vf.NumVFs)
				assert.False(t, seen[vf.PCIAddress], "duplicate VF %s", vf.PCIAddress)
				seen[vf.PCIAddress] = true
			}
		}
		assert.Len(t, seen, 8)
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
		expectErr   bool
	}{
		"8 VFs active": {fileContent: "8\n", createFile: true, expected: 8},
		"4 VFs active": {fileContent: "4\n", createFile: true, expected: 4},
		"1 VF active":  {fileContent: "1\n", createFile: true, expected: 1},
		"0 VFs active": {fileContent: "0\n", createFile: true, expected: 0},
		"file missing": {createFile: false, expected: 0},
		"malformed":    {fileContent: "bad\n", createFile: true, expectErr: true},
		"negative":     {fileContent: "-1\n", createFile: true, expectErr: true},
		"empty":        {fileContent: "\n", createFile: true, expectErr: true},
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
			got, err := ReadSRIOVNumVFs(pciAddr)
			if tc.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.expected, got)
		})
	}

	t.Run("device missing", func(t *testing.T) {
		setupFakeSysfs(t)
		_, err := ReadSRIOVNumVFs("0000:ff:00.0")
		require.Error(t, err)
	})

	t.Run("attribute unreadable", func(t *testing.T) {
		root := setupFakeSysfs(t)
		pciAddr := "0000:0a:00.0"
		pciPath := createFakePCIDevice(t, root, pciAddr, nil, nil)
		require.NoError(t, os.Mkdir(filepath.Join(pciPath, "sriov_numvfs"), 0755))
		_, err := ReadSRIOVNumVFs(pciAddr)
		require.Error(t, err)
	})
}

func TestGetVFMapping_ValidatesVFCount(t *testing.T) {
	for _, tc := range []struct {
		name    string
		count   string
		wantVFs int
	}{
		{name: "malformed", count: "bad\n"},
		{name: "missing"},
		{name: "zero", count: "0\n"},
		{name: "fewer than links", count: "1\n"},
		{name: "more than links", count: "3\n", wantVFs: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := setupFakeSysfs(t)
			const pfAddr = "0000:0a:00.0"
			setupGIMPFWithVFs(t, root, pfAddr, []string{"0000:0b:00.0", "0000:0b:01.0"}, nil)
			countPath := filepath.Join(root, "sys/bus/pci/devices", pfAddr, "sriov_numvfs")
			if tc.count == "" {
				require.NoError(t, os.Remove(countPath))
			} else {
				require.NoError(t, os.WriteFile(countPath, []byte(tc.count), 0644))
			}
			vfMap, err := GetVFMapping()
			require.NoError(t, err)
			got := 0
			for _, vfs := range vfMap {
				got += len(vfs)
				for _, vf := range vfs {
					assert.Equal(t, 3, vf.NumVFs)
				}
			}
			assert.Equal(t, tc.wantVFs, got)
		})
	}
}

func TestReadPFCapacity(t *testing.T) {
	for name, tc := range map[string]struct {
		deviceID string
		cu       uint64
		simd     uint64
	}{
		"MI300X":          {deviceID: "0x74a1", cu: 304, simd: 1216},
		"MI325X":          {deviceID: "0x74a5", cu: 304, simd: 1216},
		"MI355X":          {deviceID: "0x75a3", cu: 256, simd: 1024},
		"MI210 unmapped":  {deviceID: "0x740f"},
		"MI300A unmapped": {deviceID: "0x74a0"},
		"unknown":         {deviceID: "0x9999"},
		"empty":           {deviceID: ""},
	} {
		t.Run(name, func(t *testing.T) {
			root := setupFakeSysfs(t)
			pciAddr := "0000:0a:00.0"
			createFakePCIDevice(t, root, pciAddr, map[string]string{
				"mem_info_vram_total": "206158430208\n",
				"device":              tc.deviceID,
			}, nil)
			assert.Equal(t, [3]uint64{206158430208, tc.cu, tc.simd}, ReadPFCapacity(pciAddr))
		})
	}

	t.Run("missing files", func(t *testing.T) {
		root := setupFakeSysfs(t)
		pciAddr := "0000:0a:00.0"
		createFakePCIDevice(t, root, pciAddr, nil, nil)
		cap := ReadPFCapacity(pciAddr)
		assert.Equal(t, [3]uint64{0, 0, 0}, cap)
	})

	t.Run("missing device ID preserves memory", func(t *testing.T) {
		root := setupFakeSysfs(t)
		pciAddr := "0000:0a:00.0"
		createFakePCIDevice(t, root, pciAddr, map[string]string{
			"mem_info_vram_total": "206158430208\n",
		}, nil)
		assert.Equal(t, [3]uint64{206158430208, 0, 0}, ReadPFCapacity(pciAddr))
	})
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

	t.Run("PF VF count controls publication", func(t *testing.T) {
		for numVFs, wantTotal := range map[string]int{"0\n": 1, "2\n": 0, "bad\n": 0} {
			root := setupFakeSysfs(t)
			devPath := createFakePCIDevice(t, root, "0000:0c:00.0",
				map[string]string{
					"vendor":       consts.AMDVendorID,
					"device":       "0x740f",
					"sriov_numvfs": numVFs,
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
			assert.Equal(t, wantTotal, total, "sriov_numvfs=%q", numVFs)
		}
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

func TestCheckIommuFDEnabled_Present(t *testing.T) {
	root := setupFakeSysfs(t)
	iommuPath := filepath.Join(root, "dev/iommu")
	require.NoError(t, os.MkdirAll(filepath.Dir(iommuPath), 0755))
	require.NoError(t, os.WriteFile(iommuPath, nil, 0644))
	assert.True(t, CheckIommuFDEnabled())
}

func TestCheckIommuFDEnabled_Absent(t *testing.T) {
	setupFakeSysfs(t)
	assert.False(t, CheckIommuFDEnabled())
}

func TestGetIommuFDCdev_Found(t *testing.T) {
	root := setupFakeSysfs(t)
	vfioDevDir := filepath.Join(root, "sys/bus/pci/devices/0000:0d:00.0/vfio-dev/vfio42")
	require.NoError(t, os.MkdirAll(vfioDevDir, 0755))

	cdev, err := GetIommuFDCdev("0000:0d:00.0")
	assert.NoError(t, err)
	assert.Equal(t, "vfio42", cdev)
}

func TestGetIommuFDCdev_NotBound(t *testing.T) {
	root := setupFakeSysfs(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sys/bus/pci/devices/0000:0d:00.0"), 0755))

	_, err := GetIommuFDCdev("0000:0d:00.0")
	assert.Error(t, err)
}

func TestGetIommuFDCdev_EmptyDir(t *testing.T) {
	root := setupFakeSysfs(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sys/bus/pci/devices/0000:0d:00.0/vfio-dev"), 0755))

	_, err := GetIommuFDCdev("0000:0d:00.0")
	assert.Error(t, err)
}
