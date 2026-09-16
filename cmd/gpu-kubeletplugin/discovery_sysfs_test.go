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
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/amdgpu"
	"github.com/stretchr/testify/require"
)

// fakeGPU describes one physical AMD GPU to lay out under a fake sysfs root.
type fakeGPU struct {
	pciAddr      string
	card, render int
	computeMode  string
	memoryMode   string
	bus, dev     int
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// fakeSysfs lays out the sysfs entries GetAMDGPUs reads and points the package
// path variables at it, so discovery can run with no AMD hardware present.
func fakeSysfs(t *testing.T, gpus []fakeGPU) {
	t.Helper()
	root := t.TempDir()
	amdgpu.SetSysfsRoot(root)
	t.Cleanup(amdgpu.ResetSysfsRoot)

	node := 1
	for _, g := range gpus {
		dir := filepath.Join(root, "sys/module/amdgpu/drivers/pci:amdgpu", g.pciAddr)
		writeFile(t, filepath.Join(dir, "current_compute_partition"), g.computeMode)
		writeFile(t, filepath.Join(dir, "current_memory_partition"), g.memoryMode)
		writeFile(t, filepath.Join(dir, "numa_node"), "0")
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "drm", fmt.Sprintf("card%d", g.card)), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "drm", fmt.Sprintf("renderD%d", g.render)), 0o755))

		writeFile(t, filepath.Join(root, "sys/class/drm", fmt.Sprintf("card%d", g.card), "device/product_name"), "Instinct MI300A\n")

		// location_id packs bus and device; discovery derives the kfd id from it.
		locationID := g.bus<<8 | g.dev<<3
		props := fmt.Sprintf("drm_render_minor %d\nlocation_id %d\ndomain 0\nsimd_count 304\nsimd_per_cu 4\nsize_in_bytes 137438953472\n", g.render, locationID)
		writeFile(t, filepath.Join(root, "sys/class/kfd/kfd/topology/nodes", fmt.Sprint(node), "properties"), props)
		node++
	}
}

// A GPU reporting a partition mode this driver was not written against must still be
// discovered. tpx is the concrete case: it is valid on MI300A.
func TestEnumerateDiscoversUnlistedPartitionMode(t *testing.T) {
	for _, mode := range []string{"dpx", "tpx", "qpx", "cpx"} {
		t.Run(mode, func(t *testing.T) {
			fakeSysfs(t, []fakeGPU{
				{pciAddr: "0000:19:00.0", card: 0, render: 128, computeMode: mode, memoryMode: "nps1", bus: 0x19, dev: 0},
			})

			devices, err := enumerateAllPossibleDevices()
			require.NoError(t, err)
			require.Len(t, devices, 1, "the %s device must be discovered", mode)

			for name, d := range devices {
				require.NotNil(t, d.AmdPartition, "%s must be modelled as a partition, got %s", mode, d.Type())
				require.Equal(t, mode+"_nps1", d.AmdPartition.PartitionProfile)
				t.Logf("%s -> %s profile=%s", mode, name, d.AmdPartition.PartitionProfile)
			}
		})
	}
}

// fakeXCP adds a platform XCP partition that belongs to the physical GPU at the same
// bus/device, which is how discovery ties a partition to its parent.
func fakeXCP(t *testing.T, xcpIndex, card, render, bus, dev, node int) {
	t.Helper()
	root := filepath.Dir(filepath.Dir(filepath.Dir(amdgpu.PlatformDevicesPath)))
	xcp := filepath.Join(amdgpu.PlatformDevicesPath, fmt.Sprintf("amdgpu_xcp_%d", xcpIndex))
	require.NoError(t, os.MkdirAll(filepath.Join(xcp, "drm", fmt.Sprintf("card%d", card)), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(xcp, "drm", fmt.Sprintf("renderD%d", render)), 0o755))

	locationID := bus<<8 | dev<<3
	props := fmt.Sprintf("drm_render_minor %d\nlocation_id %d\ndomain 0\nsimd_count 38\nsimd_per_cu 4\nsize_in_bytes 17179869184\n", render, locationID)
	writeFile(t, filepath.Join(root, "sys/class/kfd/kfd/topology/nodes", fmt.Sprint(node), "properties"), props)
}

// Every partition must inherit the PCI address of the physical GPU that shares its kfd
// id, and must do so on every run. The parent lookup used to scan a map the loop was
// appending partitions to, so a later partition could match an earlier one instead.
func TestEnumerateAttachesPartitionsToPhysicalParent(t *testing.T) {
	for run := 0; run < 40; run++ {
		func() {
			fakeSysfs(t, []fakeGPU{
				{pciAddr: "0000:19:00.0", card: 0, render: 128, computeMode: "cpx", memoryMode: "nps1", bus: 0x19, dev: 0},
			})
			fakeXCP(t, 30, 1, 129, 0x19, 0, 10)
			fakeXCP(t, 31, 2, 130, 0x19, 0, 11)
			fakeXCP(t, 32, 3, 131, 0x19, 0, 12)

			devices, err := enumerateAllPossibleDevices()
			require.NoError(t, err)

			partitions := 0
			for name, d := range devices {
				if d.AmdPartition == nil {
					continue
				}
				partitions++
				require.NotNil(t, d.AmdPartition.Parent, "%s has no parent", name)
				require.Equal(t, "0000:19:00.0", d.AmdPartition.Parent.PCIAddress,
					"run %d: %s did not inherit the physical parent BDF", run, name)
			}
			require.Equal(t, 4, partitions, "run %d: expected 4 partitions (the cpx physical entry plus 3 XCPs), got %d", run, partitions)
		}()
	}
}

// A GPU whose DRM entries are missing must be skipped, and must not be published with
// the previous GPU's card, render, or kfd identity. This is the carry-over this PR
// removes, checked through discovery rather than the helper alone.
func TestEnumerateSkipsIncompleteIdentityWithoutBorrowing(t *testing.T) {
	defer amdgpu.SetDiscoveryRetry(1, 0)() // the device stays incomplete; no need to wait out the backoff
	fakeSysfs(t, []fakeGPU{
		{pciAddr: "0000:19:00.0", card: 0, render: 128, computeMode: "spx", memoryMode: "nps1", bus: 0x19, dev: 0},
	})
	// A second amdgpu-bound device with no drm entries and no topology node.
	incomplete := filepath.Join(amdgpu.AMDGPUDriversPath, "pci:amdgpu", "0000:1a:00.0")
	writeFile(t, filepath.Join(incomplete, "current_compute_partition"), "spx")
	writeFile(t, filepath.Join(incomplete, "current_memory_partition"), "nps1")
	writeFile(t, filepath.Join(incomplete, "numa_node"), "0")

	devices, err := enumerateAllPossibleDevices()
	require.NoError(t, err)

	for name, d := range devices {
		t.Logf("discovered %s (%s)", name, d.Type())
	}
	require.Len(t, devices, 1, "only the complete GPU may be published")
	_, ok := devices["gpu-0-128"]
	require.True(t, ok, "the complete GPU keeps its own identity")
}

// A mode the driver does not know must stop discovery rather than be published as a
// partition, since nothing downstream can interpret its allocation semantics.
func TestEnumerateRejectsUnknownPartitionMode(t *testing.T) {
	for _, mode := range []string{"zpx", "invalid", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			fakeSysfs(t, []fakeGPU{
				{pciAddr: "0000:19:00.0", card: 0, render: 128, computeMode: mode, memoryMode: "nps1", bus: 0x19, dev: 0},
			})
			_, err := enumerateAllPossibleDevices()
			require.Error(t, err, "%q must not be published as a partition", mode)
			require.Contains(t, err.Error(), mode)
		})
	}
}

// An unreadable partition file must not read as "no partitioning support": that would
// publish a partitioned GPU as one whole allocatable device.
func TestEnumerateSkipsDeviceWithUnreadablePartitionFile(t *testing.T) {
	defer amdgpu.SetDiscoveryRetry(1, 0)() // the device stays incomplete; no need to wait out the backoff
	fakeSysfs(t, []fakeGPU{
		{pciAddr: "0000:19:00.0", card: 0, render: 128, computeMode: "cpx", memoryMode: "nps1", bus: 0x19, dev: 0},
	})
	// Replace the file with a directory so the read fails as EISDIR for any user.
	p := filepath.Join(amdgpu.AMDGPUDriversPath, "pci:amdgpu", "0000:19:00.0", "current_compute_partition")
	require.NoError(t, os.Remove(p))
	require.NoError(t, os.MkdirAll(p, 0o755))

	devices, err := enumerateAllPossibleDevices()
	require.NoError(t, err)
	require.Empty(t, devices, "an unreadable partition mode must not become a whole GPU")
}

// A GPU in a partition mode whose KFD identity is not ready yet must not be published:
// its partitions are skipped for the same missing id, so it would advertise part of a
// partitioned GPU as if it were the whole topology.
func TestEnumerateSkipsPartitionedGPUWithoutKFDIdentity(t *testing.T) {
	defer amdgpu.SetDiscoveryRetry(1, 0)() // the identity never appears in this test
	for _, mode := range []string{"dpx", "tpx"} {
		t.Run(mode, func(t *testing.T) {
			fakeSysfs(t, []fakeGPU{
				{pciAddr: "0000:19:00.0", card: 0, render: 128, computeMode: mode, memoryMode: "nps1", bus: 0x19, dev: 0},
			})
			// Drop the topology node so the kfd identity cannot resolve.
			require.NoError(t, os.RemoveAll(filepath.Join(amdgpu.KFDTopologyPath, "topology")))

			devices, err := enumerateAllPossibleDevices()
			require.NoError(t, err)
			require.Empty(t, devices, "no partial topology may be published for %s", mode)
		})
	}
}

// A GPU bound to amdgpu whose DRM entries appear a moment after the plugin starts must
// end up published. Discovery runs once, so without a retry it would stay missing until
// the process restarts even though the hardware is fine.
func TestEnumerateRetriesUntilDRMEntriesAppear(t *testing.T) {
	defer amdgpu.SetDiscoveryRetry(20, 20*time.Millisecond)()
	fakeSysfs(t, []fakeGPU{
		{pciAddr: "0000:19:00.0", card: 0, render: 128, computeMode: "spx", memoryMode: "nps1", bus: 0x19, dev: 0},
	})

	// Take the drm entries away, then put them back while discovery is retrying.
	drm := filepath.Join(amdgpu.AMDGPUDriversPath, "pci:amdgpu", "0000:19:00.0", "drm")
	require.NoError(t, os.RemoveAll(drm))
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = os.MkdirAll(filepath.Join(drm, "card0"), 0o755)
		_ = os.MkdirAll(filepath.Join(drm, "renderD128"), 0o755)
	}()

	devices, err := enumerateAllPossibleDevices()
	require.NoError(t, err)
	require.Contains(t, devices, "gpu-0-128", "the GPU must be published once its entries appear")
}

// Without the retry the same GPU is lost, which is what makes the retry load-bearing
// rather than a delay that happens to be harmless.
func TestEnumerateWithoutRetryLosesLateDRMEntries(t *testing.T) {
	defer amdgpu.SetDiscoveryRetry(1, 0)()
	fakeSysfs(t, []fakeGPU{
		{pciAddr: "0000:19:00.0", card: 0, render: 128, computeMode: "spx", memoryMode: "nps1", bus: 0x19, dev: 0},
	})
	require.NoError(t, os.RemoveAll(filepath.Join(amdgpu.AMDGPUDriversPath, "pci:amdgpu", "0000:19:00.0", "drm")))

	devices, err := enumerateAllPossibleDevices()
	require.NoError(t, err)
	require.Empty(t, devices, "a single attempt cannot see entries that appear later")
}

// Only one of the two partition files present is a partial read of a changing tree, not
// a GPU without partitioning support, so it must be retried rather than published whole.
func TestEnumerateRetriesOnHalfPresentPartitionState(t *testing.T) {
	defer amdgpu.SetDiscoveryRetry(1, 0)()
	for _, missing := range []string{"current_memory_partition", "current_compute_partition"} {
		t.Run("missing "+missing, func(t *testing.T) {
			fakeSysfs(t, []fakeGPU{
				{pciAddr: "0000:19:00.0", card: 0, render: 128, computeMode: "dpx", memoryMode: "nps1", bus: 0x19, dev: 0},
			})
			require.NoError(t, os.Remove(filepath.Join(amdgpu.AMDGPUDriversPath, "pci:amdgpu", "0000:19:00.0", missing)))

			devices, err := enumerateAllPossibleDevices()
			require.NoError(t, err)
			require.Empty(t, devices, "a half-present partition state must not be published")
		})
	}
}

// A memory mode the driver cannot interpret must not be paired with a known compute mode
// into a profile the scheduler can select on.
func TestEnumerateRejectsUnknownMemoryMode(t *testing.T) {
	defer amdgpu.SetDiscoveryRetry(1, 0)()
	for _, mode := range []string{"unknown", "garbage"} {
		t.Run(mode, func(t *testing.T) {
			fakeSysfs(t, []fakeGPU{
				{pciAddr: "0000:19:00.0", card: 0, render: 128, computeMode: "dpx", memoryMode: mode, bus: 0x19, dev: 0},
			})
			_, err := enumerateAllPossibleDevices()
			require.Error(t, err)
			require.Contains(t, err.Error(), mode)
		})
	}
}

// The whole-GPU path concatenates the memory mode into the profile as well, so a mode the
// driver cannot interpret has to be rejected there too rather than published as spx_<mode>.
func TestEnumerateRejectsUnknownMemoryModeOnWholeGPU(t *testing.T) {
	defer amdgpu.SetDiscoveryRetry(1, 0)()
	for _, mode := range []string{"unknown", "garbage", "nps5"} {
		t.Run(mode, func(t *testing.T) {
			fakeSysfs(t, []fakeGPU{
				{pciAddr: "0000:19:00.0", card: 0, render: 128, computeMode: "spx", memoryMode: mode, bus: 0x19, dev: 0},
			})
			_, err := enumerateAllPossibleDevices()
			require.Error(t, err)
			require.Contains(t, err.Error(), mode)
		})
	}
}

// A GPU with no partitioning support reports neither mode, and an empty memory mode is the
// kernel saying so rather than a value to validate. It must still be published.
func TestEnumeratePublishesGPUWithoutPartitionSupport(t *testing.T) {
	defer amdgpu.SetDiscoveryRetry(1, 0)()
	fakeSysfs(t, []fakeGPU{
		{pciAddr: "0000:19:00.0", card: 0, render: 128, computeMode: "", memoryMode: "", bus: 0x19, dev: 0},
	})

	devices, err := enumerateAllPossibleDevices()
	require.NoError(t, err)
	require.Len(t, devices, 1, "a GPU that does not support partitioning must still be published")
}

// The amdgpu sysfs root disappearing during a driver reload must stay retryable instead
// of reading as a completed discovery that happens to have found nothing.
func TestEnumerateDoesNotAcceptAVanishedDriverRoot(t *testing.T) {
	defer amdgpu.SetDiscoveryRetry(20, 20*time.Millisecond)()
	fakeSysfs(t, []fakeGPU{
		{pciAddr: "0000:19:00.0", card: 0, render: 128, computeMode: "spx", memoryMode: "nps1", bus: 0x19, dev: 0},
	})
	root := amdgpu.AMDGPUDriversPath
	stash := root + ".away"
	require.NoError(t, os.Rename(root, stash))
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = os.Rename(stash, root)
	}()

	devices, err := enumerateAllPossibleDevices()
	require.NoError(t, err)
	require.Contains(t, devices, "gpu-0-128", "the GPU must be found once the driver root is back")
}
