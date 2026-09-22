/*
 * Copyright 2025 The Kubernetes Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

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
	"strconv"
	"testing"

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/consts"
	resourceapi "k8s.io/api/resource/v1"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"
)

// newTestPartitionState creates a PartitionState for testing with pre-populated
// gpuComputeModes and activeMemoryMode so that ApplyPartition skips amd-smi calls.
// gpuPCIAddresses must contain entries for all GPU indices that will be used.
func newTestPartitionState(gpuPCIAddresses map[int]string, partitionableGPUs []int, allocatable AllocatableDevices) *PartitionState {
	return NewPartitionState(gpuPCIAddresses, partitionableGPUs, allocatable, false, nil)
}

// testShares builds one PartitionShare per device name, giving each a distinct
// share key so they occupy distinct partition slots (as separate requests in a
// claim would).
func testShares(claimUID string, deviceNames ...string) []PartitionShare {
	shares := make([]PartitionShare, 0, len(deviceNames))
	for i, deviceName := range deviceNames {
		shares = append(shares, PartitionShare{
			DeviceName: deviceName,
			ShareKey:   ShareKey(claimUID, "req"+strconv.Itoa(i), deviceName, nil),
		})
	}
	return shares
}

// testShareKey builds a single share key for direct ReservePartition/
// ReleasePartition calls.
func testShareKey(claimUID, deviceName string) string {
	return ShareKey(claimUID, "req0", deviceName, nil)
}

// prepopulatePartitionState bypasses amd-smi by directly setting the modes.
// Use this to simulate a PartitionState that already has active allocations.
func prepopulatePartitionState(ps *PartitionState, memoryMode string, computeModes map[int]string, allocCounts map[int]int, total int) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.activeMemoryMode = memoryMode
	for k, v := range computeModes {
		ps.gpuComputeModes[k] = v
	}
	for k, v := range allocCounts {
		ps.gpuAllocCounts[k] = v
	}
	ps.totalAllocCount = total
}

// ---- Tests for PartitionState.ReservePartition ----

// TestPartitionState_ReservePartition_SecondAllocationIsNoOp verifies that when the GPU already
// has a compute mode set (matching the requested mode) and the node already has the matching
// memory mode, ReservePartition simply increments counts and reports no taint change.
func TestPartitionState_ReservePartition_SecondAllocationIsNoOp(t *testing.T) {
	allocatable := make(AllocatableDevices)
	gpuPCIAddresses := map[int]string{0: "0000:19:00.0"}
	ps := newTestPartitionState(gpuPCIAddresses, []int{0}, allocatable)

	// Simulate that first allocation was already done (cpx-nps4 on GPU 0)
	prepopulatePartitionState(ps, consts.MemoryPartitionNPS4,
		map[int]string{0: consts.ComputePartitionCPX},
		map[int]int{0: 1},
		1,
	)

	// Second allocation on same GPU with same compute+memory mode
	deviceName := "gpu-0-cpx-nps4"
	taintsChanged, err := ps.ReservePartition(deviceName, testShareKey("claim-test", deviceName))
	if err != nil {
		t.Fatalf("expected no error for second allocation with same mode, got: %v", err)
	}
	if taintsChanged {
		t.Error("expected taintsChanged=false when memory mode already active")
	}

	// Verify counts were incremented
	if ps.gpuAllocCounts[0] != 2 {
		t.Errorf("expected gpuAllocCounts[0]=2, got %d", ps.gpuAllocCounts[0])
	}
	if ps.totalAllocCount != 2 {
		t.Errorf("expected totalAllocCount=2, got %d", ps.totalAllocCount)
	}
	if ps.activeMemoryMode != consts.MemoryPartitionNPS4 {
		t.Errorf("expected activeMemoryMode=%q, got %q", consts.MemoryPartitionNPS4, ps.activeMemoryMode)
	}
}

// TestPartitionState_ReservePartition_ConflictingComputeModeReturnsError verifies that
// requesting a different compute mode when one is already active returns an error.
func TestPartitionState_ReservePartition_ConflictingComputeModeReturnsError(t *testing.T) {
	allocatable := make(AllocatableDevices)
	gpuPCIAddresses := map[int]string{0: "0000:19:00.0"}
	ps := newTestPartitionState(gpuPCIAddresses, []int{0}, allocatable)

	// Simulate GPU 0 is already in CPX mode
	prepopulatePartitionState(ps, consts.MemoryPartitionNPS4,
		map[int]string{0: consts.ComputePartitionCPX},
		map[int]int{0: 1},
		1,
	)

	// Try to allocate with SPX (conflicting compute mode)
	deviceName := "gpu-0-spx-nps1"
	_, err := ps.ReservePartition(deviceName, testShareKey("claim-test", deviceName))
	if err == nil {
		t.Fatal("expected error for conflicting compute mode, got nil")
	}
}

// TestPartitionState_ReservePartition_ConflictingMemoryModeReturnsError verifies that
// requesting a different memory mode when one is already active returns an error.
func TestPartitionState_ReservePartition_ConflictingMemoryModeReturnsError(t *testing.T) {
	allocatable := make(AllocatableDevices)
	gpuPCIAddresses := map[int]string{0: "0000:19:00.0", 1: "0000:20:00.0"}
	ps := newTestPartitionState(gpuPCIAddresses, []int{0, 1}, allocatable)

	// GPU 0 has cpx+nps4 active. GPU 1 also has cpx pre-set, but nps1 conflicts
	// with the active nps4 memory mode.
	prepopulatePartitionState(ps, consts.MemoryPartitionNPS4,
		map[int]string{0: consts.ComputePartitionCPX, 1: consts.ComputePartitionCPX},
		map[int]int{0: 1, 1: 0},
		1,
	)

	// Try to allocate GPU 1 with CPX but NPS1 memory (conflicting memory mode)
	deviceName := "gpu-1-cpx-nps1"
	_, err := ps.ReservePartition(deviceName, testShareKey("claim-test", deviceName))
	if err == nil {
		t.Fatal("expected error for conflicting memory mode, got nil")
	}
}

// TestPartitionState_ReservePartition_FirstAllocationStampsTaints verifies that the
// first reservation on an idle node sets the memory mode and reports taintsChanged=true.
func TestPartitionState_ReservePartition_FirstAllocationStampsTaints(t *testing.T) {
	allocatable := make(AllocatableDevices)
	gpuPCIAddresses := map[int]string{0: "0000:19:00.0"}
	ps := newTestPartitionState(gpuPCIAddresses, []int{0}, allocatable)

	taintsChanged, err := ps.ReservePartition("gpu-0-cpx-nps4", testShareKey("claim-test", "gpu-0-cpx-nps4"))
	if err != nil {
		t.Fatalf("expected no error for first reservation, got: %v", err)
	}
	if !taintsChanged {
		t.Error("expected taintsChanged=true when first reservation sets memory mode")
	}
	if ps.activeMemoryMode != consts.MemoryPartitionNPS4 {
		t.Errorf("expected activeMemoryMode=%q, got %q", consts.MemoryPartitionNPS4, ps.activeMemoryMode)
	}
	if ps.gpuComputeModes[0] != consts.ComputePartitionCPX {
		t.Errorf("expected compute mode cpx on GPU 0, got %q", ps.gpuComputeModes[0])
	}
	if ps.totalAllocCount != 1 {
		t.Errorf("expected totalAllocCount=1, got %d", ps.totalAllocCount)
	}
}

// TestPartitionState_ReservePartition_InvalidDeviceNameReturnsError verifies that
// a malformed device name returns a parse error.
func TestPartitionState_ReservePartition_InvalidDeviceNameReturnsError(t *testing.T) {
	allocatable := make(AllocatableDevices)
	gpuPCIAddresses := map[int]string{0: "0000:19:00.0"}
	ps := newTestPartitionState(gpuPCIAddresses, []int{0}, allocatable)

	_, err := ps.ReservePartition("not-a-valid-name", testShareKey("claim-test", "not-a-valid-name"))
	if err == nil {
		t.Fatal("expected error for invalid device name, got nil")
	}
}

// ---- Tests for PartitionState.ReserveClaim / ReleaseClaim (per-claim idempotency) ----

// TestPartitionState_ReserveClaim_RetryIsIdempotent verifies that re-driving
// ReserveClaim for the SAME claim (as kubelet does on every retry while an async
// memory reload is still converging) does NOT double-count allocations. Without
// a per-claim guard, each retry re-runs the reservation and inflates
// gpuAllocCounts/totalAllocCount, which later corrupts the partition-index math
// in discoverPartitionDeviceNodes and prevents taints/memory mode from ever
// clearing on release.
func TestPartitionState_ReserveClaim_RetryIsIdempotent(t *testing.T) {
	allocatable := make(AllocatableDevices)
	gpuPCIAddresses := map[int]string{0: "0000:19:00.0"}
	ps := newTestPartitionState(gpuPCIAddresses, []int{0}, allocatable)

	claimUID := "claim-abc"
	devices := []string{"gpu-0-cpx-nps4"}

	// First Prepare attempt: reserve.
	if _, err := ps.ReserveClaim(claimUID, testShares(claimUID, devices...)); err != nil {
		t.Fatalf("first ReserveClaim failed: %v", err)
	}
	// Simulate several kubelet retries while the async reload converges.
	for i := 0; i < 3; i++ {
		if _, err := ps.ReserveClaim(claimUID, testShares(claimUID, devices...)); err != nil {
			t.Fatalf("retry %d ReserveClaim failed: %v", i, err)
		}
	}

	if ps.gpuAllocCounts[0] != 1 {
		t.Errorf("expected gpuAllocCounts[0]=1 after retries, got %d", ps.gpuAllocCounts[0])
	}
	if ps.totalAllocCount != 1 {
		t.Errorf("expected totalAllocCount=1 after retries, got %d", ps.totalAllocCount)
	}
}

// TestPartitionState_ReserveClaim_DistinctClaimsAccumulate verifies that the
// idempotency guard is per-claim: two different claims on the same GPU each count.
func TestPartitionState_ReserveClaim_DistinctClaimsAccumulate(t *testing.T) {
	allocatable := make(AllocatableDevices)
	gpuPCIAddresses := map[int]string{0: "0000:19:00.0"}
	ps := newTestPartitionState(gpuPCIAddresses, []int{0}, allocatable)

	if _, err := ps.ReserveClaim("claim-1", testShares("claim-1", "gpu-0-cpx-nps4")); err != nil {
		t.Fatalf("ReserveClaim claim-1 failed: %v", err)
	}
	if _, err := ps.ReserveClaim("claim-2", testShares("claim-2", "gpu-0-cpx-nps4")); err != nil {
		t.Fatalf("ReserveClaim claim-2 failed: %v", err)
	}

	if ps.gpuAllocCounts[0] != 2 {
		t.Errorf("expected gpuAllocCounts[0]=2 for two distinct claims, got %d", ps.gpuAllocCounts[0])
	}
	if ps.totalAllocCount != 2 {
		t.Errorf("expected totalAllocCount=2 for two distinct claims, got %d", ps.totalAllocCount)
	}
}

// TestPartitionState_ReserveClaim_ConflictRollsBackWholeClaim verifies that if a
// claim reserves multiple devices and a later device conflicts, the devices
// already reserved for THIS claim are rolled back (no partial reservation) and
// the claim is not marked reserved.
func TestPartitionState_ReserveClaim_ConflictRollsBackWholeClaim(t *testing.T) {
	allocatable := make(AllocatableDevices)
	gpuPCIAddresses := map[int]string{0: "0000:19:00.0", 1: "0000:20:00.0"}
	ps := newTestPartitionState(gpuPCIAddresses, []int{0, 1}, allocatable)

	// A prior claim already locked node memory mode to nps1 via GPU 1.
	if _, err := ps.ReserveClaim("claim-prior", testShares("claim-prior", "gpu-1-spx-nps1")); err != nil {
		t.Fatalf("prior ReserveClaim failed: %v", err)
	}

	// New claim: first device is fine (nps1), second requests nps4 -> conflict.
	_, err := ps.ReserveClaim("claim-new", testShares("claim-new", "gpu-0-spx-nps1", "gpu-0-cpx-nps4"))
	if err == nil {
		t.Fatal("expected conflict error for mixed memory modes, got nil")
	}

	// GPU 0 must have NO leftover reservation from the failed claim.
	if ps.gpuAllocCounts[0] != 0 {
		t.Errorf("expected gpuAllocCounts[0]=0 after rollback, got %d", ps.gpuAllocCounts[0])
	}
	// Only the prior claim's single allocation should remain.
	if ps.totalAllocCount != 1 {
		t.Errorf("expected totalAllocCount=1 after rollback, got %d", ps.totalAllocCount)
	}
	// A retry of the same failing claim must still fail (not treated as reserved).
	if _, err := ps.ReserveClaim("claim-new", testShares("claim-new", "gpu-0-spx-nps1", "gpu-0-cpx-nps4")); err == nil {
		t.Fatal("expected failing claim to remain unreserved on retry, got nil error")
	}
}

// TestPartitionState_ReleaseClaim_Idempotent verifies that ReleaseClaim decrements
// exactly once per claim regardless of how many times it is called, and reports a
// taint change only when the last allocation on the node is released.
func TestPartitionState_ReleaseClaim_Idempotent(t *testing.T) {
	allocatable := make(AllocatableDevices)
	gpuPCIAddresses := map[int]string{0: "0000:19:00.0"}
	ps := newTestPartitionState(gpuPCIAddresses, []int{0}, allocatable)

	if _, err := ps.ReserveClaim("claim-1", testShares("claim-1", "gpu-0-cpx-nps4")); err != nil {
		t.Fatalf("ReserveClaim failed: %v", err)
	}

	taintsChanged, err := ps.ReleaseClaim("claim-1", testShares("claim-1", "gpu-0-cpx-nps4"))
	if err != nil {
		t.Fatalf("ReleaseClaim failed: %v", err)
	}
	if !taintsChanged {
		t.Error("expected taintsChanged=true when last allocation released")
	}
	// Second release for the same claim must be a no-op.
	taintsChanged, err = ps.ReleaseClaim("claim-1", testShares("claim-1", "gpu-0-cpx-nps4"))
	if err != nil {
		t.Fatalf("second ReleaseClaim failed: %v", err)
	}
	if taintsChanged {
		t.Error("expected taintsChanged=false on repeated release of same claim")
	}
	if ps.totalAllocCount != 0 {
		t.Errorf("expected totalAllocCount=0, got %d", ps.totalAllocCount)
	}
}

// ---- Tests for PartitionState.ReleasePartition ----

// TestPartitionState_ReleasePartition_DecrementsCounters verifies that
// unpreparing a device decrements both per-GPU and total allocation counts.
func TestPartitionState_ReleasePartition_DecrementsCounters(t *testing.T) {
	allocatable := make(AllocatableDevices)
	gpuPCIAddresses := map[int]string{0: "0000:19:00.0"}
	ps := newTestPartitionState(gpuPCIAddresses, []int{0}, allocatable)

	// Simulate 2 active allocations on GPU 0 with cpx-nps4
	prepopulatePartitionState(ps, consts.MemoryPartitionNPS4,
		map[int]string{0: consts.ComputePartitionCPX},
		map[int]int{0: 2},
		2,
	)

	taintsChanged, err := ps.ReleasePartition("gpu-0-cpx-nps4", testShareKey("claim-test", "gpu-0-cpx-nps4"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Still 1 allocation left, so taintsChanged should be false
	if taintsChanged {
		t.Error("expected taintsChanged=false when allocations still remain")
	}
	if ps.gpuAllocCounts[0] != 1 {
		t.Errorf("expected gpuAllocCounts[0]=1, got %d", ps.gpuAllocCounts[0])
	}
	if ps.totalAllocCount != 1 {
		t.Errorf("expected totalAllocCount=1, got %d", ps.totalAllocCount)
	}
}

// TestPartitionState_ReleasePartition_LastAllocationClearsMode verifies that
// releasing the last allocation clears the memory mode and sets taintsChanged=true.
func TestPartitionState_ReleasePartition_LastAllocationClearsMode(t *testing.T) {
	allocatable := make(AllocatableDevices)
	gpuPCIAddresses := map[int]string{0: "0000:19:00.0"}
	ps := newTestPartitionState(gpuPCIAddresses, []int{0}, allocatable)

	// Simulate exactly 1 active allocation on GPU 0
	prepopulatePartitionState(ps, consts.MemoryPartitionNPS4,
		map[int]string{0: consts.ComputePartitionCPX},
		map[int]int{0: 1},
		1,
	)

	taintsChanged, err := ps.ReleasePartition("gpu-0-cpx-nps4", testShareKey("claim-test", "gpu-0-cpx-nps4"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Last allocation released — taintsChanged must be true
	if !taintsChanged {
		t.Error("expected taintsChanged=true when last allocation released")
	}
	if ps.activeMemoryMode != "" {
		t.Errorf("expected activeMemoryMode to be cleared, got %q", ps.activeMemoryMode)
	}
	if ps.totalAllocCount != 0 {
		t.Errorf("expected totalAllocCount=0, got %d", ps.totalAllocCount)
	}
	if _, exists := ps.gpuComputeModes[0]; exists {
		t.Error("expected GPU 0 compute mode to be cleared after last allocation")
	}
}

// TestPartitionState_ReleasePartition_ClearsComputeModeWhenGPUDrained verifies
// that clearing the last allocation on a GPU removes that GPU's entry in gpuComputeModes.
func TestPartitionState_ReleasePartition_ClearsComputeModeWhenGPUDrained(t *testing.T) {
	allocatable := make(AllocatableDevices)
	gpuPCIAddresses := map[int]string{0: "0000:19:00.0", 1: "0000:20:00.0"}
	ps := newTestPartitionState(gpuPCIAddresses, []int{0, 1}, allocatable)

	// GPU 0 has 1 allocation, GPU 1 has 2 allocations; total = 3
	prepopulatePartitionState(ps, consts.MemoryPartitionNPS4,
		map[int]string{0: consts.ComputePartitionCPX, 1: consts.ComputePartitionCPX},
		map[int]int{0: 1, 1: 2},
		3,
	)

	// Release the single allocation on GPU 0
	taintsChanged, err := ps.ReleasePartition("gpu-0-cpx-nps4", testShareKey("claim-test", "gpu-0-cpx-nps4"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if taintsChanged {
		t.Error("expected taintsChanged=false since GPU 1 still has allocations")
	}
	if _, exists := ps.gpuComputeModes[0]; exists {
		t.Error("expected GPU 0 compute mode cleared after its last allocation")
	}
	if _, exists := ps.gpuComputeModes[1]; !exists {
		t.Error("expected GPU 1 compute mode to still be present")
	}
}

// TestPartitionState_ReleasePartition_InvalidDeviceNameReturnsError tests that
// an invalid device name returns a parse error.
func TestPartitionState_ReleasePartition_InvalidDeviceNameReturnsError(t *testing.T) {
	allocatable := make(AllocatableDevices)
	gpuPCIAddresses := map[int]string{0: "0000:19:00.0"}
	ps := newTestPartitionState(gpuPCIAddresses, []int{0}, allocatable)

	_, err := ps.ReleasePartition("invalid-name", testShareKey("claim-test", "invalid-name"))
	if err == nil {
		t.Fatal("expected error for invalid device name, got nil")
	}
}

// ---- Tests for PartitionState.HasTaints / applyMemoryTaints ----

// TestPartitionState_HasTaints_TrueWhenMemoryModeActive verifies that HasTaints returns
// true whenever there is an active memory mode.
func TestPartitionState_HasTaints_TrueWhenMemoryModeActive(t *testing.T) {
	allocatable := make(AllocatableDevices)
	gpuPCIAddresses := map[int]string{0: "0000:19:00.0"}
	ps := newTestPartitionState(gpuPCIAddresses, []int{0}, allocatable)

	if ps.HasTaints() {
		t.Error("expected HasTaints=false when no memory mode is active")
	}

	prepopulatePartitionState(ps, consts.MemoryPartitionNPS4,
		map[int]string{0: consts.ComputePartitionCPX},
		map[int]int{0: 1},
		1,
	)

	if !ps.HasTaints() {
		t.Error("expected HasTaints=true when memory mode is active")
	}
}

// TestPartitionState_ApplyMemoryTaints_TaintsIncompatibleDevices verifies that when
// activeMemoryMode is "nps4", synthetic-partition devices with other memory modes
// (nps1, nps2) receive a NoExecute taint, while nps4 devices have no taint.
func TestPartitionState_ApplyMemoryTaints_TaintsIncompatibleDevices(t *testing.T) {
	// Build allocatable map with one device per valid partition config
	allocatable := make(AllocatableDevices)

	for _, cfg := range consts.ValidPartitionConfigs {
		sp := &SyntheticPartitionDevice{
			GPUIndex:         0,
			ComputePartition: cfg.Compute,
			MemoryPartition:  cfg.Memory,
			PartitionCount:   cfg.PartitionCount,
		}
		ad := &AllocatableDevice{SyntheticPartition: sp}
		allocatable[ad.CanonicalName()] = ad
	}

	gpuPCIAddresses := map[int]string{0: "0000:19:00.0"}
	ps := newTestPartitionState(gpuPCIAddresses, []int{0}, allocatable)
	ps.allocatable = allocatable

	// Apply nps4 as the active memory mode
	ps.mu.Lock()
	ps.applyMemoryTaints(consts.MemoryPartitionNPS4)
	ps.mu.Unlock()

	for _, device := range allocatable {
		sp := device.SyntheticPartition
		if sp.MemoryPartition == consts.MemoryPartitionNPS4 {
			if len(sp.Taints) != 0 {
				t.Errorf("device %s (nps4) should have no taint, got %v", sp.CanonicalName(), sp.Taints)
			}
		} else {
			if len(sp.Taints) == 0 {
				t.Errorf("device %s (%s) should have a taint when active mode is nps4", sp.CanonicalName(), sp.MemoryPartition)
			} else {
				taint := sp.Taints[0]
				if taint.Effect != resourceapi.DeviceTaintEffectNoExecute {
					t.Errorf("device %s: expected NoExecute taint effect, got %v", sp.CanonicalName(), taint.Effect)
				}
				if taint.Key != consts.MemoryPartitionTaintKey {
					t.Errorf("device %s: expected taint key %q, got %q", sp.CanonicalName(), consts.MemoryPartitionTaintKey, taint.Key)
				}
				if taint.Value != consts.MemoryPartitionNPS4 {
					t.Errorf("device %s: expected taint value %q, got %q", sp.CanonicalName(), consts.MemoryPartitionNPS4, taint.Value)
				}
			}
		}
	}
}

// TestPartitionState_RemoveMemoryTaints_ClearsAllTaints verifies that removeMemoryTaints
// clears all taints from synthetic-partition devices.
func TestPartitionState_RemoveMemoryTaints_ClearsAllTaints(t *testing.T) {
	allocatable := make(AllocatableDevices)
	sp := &SyntheticPartitionDevice{
		GPUIndex:         0,
		ComputePartition: consts.ComputePartitionCPX,
		MemoryPartition:  consts.MemoryPartitionNPS1,
		PartitionCount:   8,
		Taints: []resourceapi.DeviceTaint{
			{
				Key:    consts.MemoryPartitionTaintKey,
				Value:  consts.MemoryPartitionNPS4,
				Effect: resourceapi.DeviceTaintEffectNoExecute,
			},
		},
	}
	allocatable["gpu-0-cpx-nps1"] = &AllocatableDevice{SyntheticPartition: sp}

	gpuPCIAddresses := map[int]string{0: "0000:19:00.0"}
	ps := newTestPartitionState(gpuPCIAddresses, []int{0}, allocatable)
	ps.allocatable = allocatable

	ps.mu.Lock()
	ps.removeMemoryTaints()
	ps.mu.Unlock()

	if len(sp.Taints) != 0 {
		t.Errorf("expected all taints removed, but got: %v", sp.Taints)
	}
}

// ---- Tests for PartitionState.RecoverFromCheckpoint ----

// TestPartitionState_RecoverFromCheckpoint_RestoresState verifies that RecoverFromCheckpoint
// correctly restores activeMemoryMode, gpuComputeModes, and reconstructs allocation counts
// from the prepared claims.
func TestPartitionState_RecoverFromCheckpoint_RestoresState(t *testing.T) {
	allocatable := make(AllocatableDevices)
	gpuPCIAddresses := map[int]string{0: "0000:19:00.0", 1: "0000:20:00.0"}
	ps := newTestPartitionState(gpuPCIAddresses, []int{0, 1}, allocatable)

	// Simulate a checkpoint with 2 allocations on GPU 0 (cpx-nps4) and 1 on GPU 1 (cpx-nps4)
	preparedClaims := PreparedClaims{
		"claim-uid-1": PreparedDevices{
			{Device: drapbv1.Device{DeviceName: "gpu-0-cpx-nps4"}},
			{Device: drapbv1.Device{DeviceName: "gpu-0-cpx-nps4"}},
		},
		"claim-uid-2": PreparedDevices{
			{Device: drapbv1.Device{DeviceName: "gpu-1-cpx-nps4"}},
		},
	}

	gpuComputeModes := map[int]string{
		0: consts.ComputePartitionCPX,
		1: consts.ComputePartitionCPX,
	}

	ps.RecoverFromCheckpoint(consts.MemoryPartitionNPS4, gpuComputeModes, nil, preparedClaims, nil)

	if ps.activeMemoryMode != consts.MemoryPartitionNPS4 {
		t.Errorf("expected activeMemoryMode=%q, got %q", consts.MemoryPartitionNPS4, ps.activeMemoryMode)
	}
	if ps.gpuComputeModes[0] != consts.ComputePartitionCPX {
		t.Errorf("expected GPU 0 compute mode=%q, got %q", consts.ComputePartitionCPX, ps.gpuComputeModes[0])
	}
	if ps.gpuComputeModes[1] != consts.ComputePartitionCPX {
		t.Errorf("expected GPU 1 compute mode=%q, got %q", consts.ComputePartitionCPX, ps.gpuComputeModes[1])
	}
	if ps.gpuAllocCounts[0] != 2 {
		t.Errorf("expected GPU 0 alloc count=2, got %d", ps.gpuAllocCounts[0])
	}
	if ps.gpuAllocCounts[1] != 1 {
		t.Errorf("expected GPU 1 alloc count=1, got %d", ps.gpuAllocCounts[1])
	}
	if ps.totalAllocCount != 3 {
		t.Errorf("expected totalAllocCount=3, got %d", ps.totalAllocCount)
	}
}

// TestPartitionState_RecoverFromCheckpoint_MarksClaimsReserved verifies that a
// claim recovered from the checkpoint is marked reserved, so a post-restart
// ReleaseClaim (Unprepare) actually decrements the counts instead of no-oping.
func TestPartitionState_RecoverFromCheckpoint_MarksClaimsReserved(t *testing.T) {
	allocatable := make(AllocatableDevices)
	gpuPCIAddresses := map[int]string{0: "0000:19:00.0"}
	ps := newTestPartitionState(gpuPCIAddresses, []int{0}, allocatable)

	preparedClaims := PreparedClaims{
		"claim-uid-1": PreparedDevices{
			{Device: drapbv1.Device{DeviceName: "gpu-0-cpx-nps4"}},
		},
	}
	ps.RecoverFromCheckpoint(consts.MemoryPartitionNPS4,
		map[int]string{0: consts.ComputePartitionCPX}, nil, preparedClaims, nil)

	// A recovered claim must be releasable exactly once.
	taintsChanged, err := ps.ReleaseClaim("claim-uid-1", testShares("claim-uid-1", "gpu-0-cpx-nps4"))
	if err != nil {
		t.Fatalf("ReleaseClaim after recovery failed: %v", err)
	}
	if !taintsChanged {
		t.Error("expected taintsChanged=true releasing the last recovered allocation")
	}
	if ps.totalAllocCount != 0 {
		t.Errorf("expected totalAllocCount=0 after releasing recovered claim, got %d", ps.totalAllocCount)
	}
}

// TestPartitionState_RecoverFromCheckpoint_EmptyCheckpoint verifies that recovering
// from an empty checkpoint leaves the state at zero.
func TestPartitionState_RecoverFromCheckpoint_EmptyCheckpoint(t *testing.T) {
	allocatable := make(AllocatableDevices)
	gpuPCIAddresses := map[int]string{0: "0000:19:00.0"}
	ps := newTestPartitionState(gpuPCIAddresses, []int{0}, allocatable)

	ps.RecoverFromCheckpoint("", nil, nil, PreparedClaims{}, nil)

	if ps.activeMemoryMode != "" {
		t.Errorf("expected empty activeMemoryMode, got %q", ps.activeMemoryMode)
	}
	if ps.totalAllocCount != 0 {
		t.Errorf("expected totalAllocCount=0, got %d", ps.totalAllocCount)
	}
}

// TestPartitionState_RecoverFromCheckpoint_NonPartitionDevicesSkipped verifies that
// non-synthetic-partition device names in the checkpoint are skipped without error.
func TestPartitionState_RecoverFromCheckpoint_NonPartitionDevicesSkipped(t *testing.T) {
	allocatable := make(AllocatableDevices)
	gpuPCIAddresses := map[int]string{0: "0000:19:00.0"}
	ps := newTestPartitionState(gpuPCIAddresses, []int{0}, allocatable)

	// Mix of synthetic-partition and real GPU device names
	preparedClaims := PreparedClaims{
		"claim-1": PreparedDevices{
			{Device: drapbv1.Device{DeviceName: "gpu-0-128"}},      // real GPU, not synthetic
			{Device: drapbv1.Device{DeviceName: "gpu-0-cpx-nps4"}}, // synthetic partition
		},
	}

	ps.RecoverFromCheckpoint(consts.MemoryPartitionNPS4,
		map[int]string{0: consts.ComputePartitionCPX},
		nil,
		preparedClaims,
		nil,
	)

	// Only the synthetic partition device should be counted
	if ps.totalAllocCount != 1 {
		t.Errorf("expected totalAllocCount=1 (only synthetic partition device counted), got %d", ps.totalAllocCount)
	}
	if ps.gpuAllocCounts[0] != 1 {
		t.Errorf("expected GPU 0 alloc count=1, got %d", ps.gpuAllocCounts[0])
	}
}

// TestPartitionState_GetActiveMemoryMode verifies GetActiveMemoryMode returns the current mode.
func TestPartitionState_GetActiveMemoryMode(t *testing.T) {
	allocatable := make(AllocatableDevices)
	gpuPCIAddresses := map[int]string{0: "0000:19:00.0"}
	ps := newTestPartitionState(gpuPCIAddresses, []int{0}, allocatable)

	if mode := ps.GetActiveMemoryMode(); mode != "" {
		t.Errorf("expected empty mode initially, got %q", mode)
	}

	prepopulatePartitionState(ps, consts.MemoryPartitionNPS1,
		map[int]string{0: consts.ComputePartitionSPX},
		map[int]int{0: 1},
		1,
	)

	if mode := ps.GetActiveMemoryMode(); mode != consts.MemoryPartitionNPS1 {
		t.Errorf("expected %q, got %q", consts.MemoryPartitionNPS1, mode)
	}
}

// TestPartitionState_GetGPUComputeModes verifies GetGPUComputeModes returns a copy.
func TestPartitionState_GetGPUComputeModes(t *testing.T) {
	allocatable := make(AllocatableDevices)
	gpuPCIAddresses := map[int]string{0: "0000:19:00.0", 1: "0000:20:00.0"}
	ps := newTestPartitionState(gpuPCIAddresses, []int{0, 1}, allocatable)

	prepopulatePartitionState(ps, consts.MemoryPartitionNPS4,
		map[int]string{0: consts.ComputePartitionCPX, 1: consts.ComputePartitionDPX},
		map[int]int{0: 1, 1: 1},
		2,
	)

	modes := ps.GetGPUComputeModes()
	if modes[0] != consts.ComputePartitionCPX {
		t.Errorf("expected GPU 0 mode=%q, got %q", consts.ComputePartitionCPX, modes[0])
	}
	if modes[1] != consts.ComputePartitionDPX {
		t.Errorf("expected GPU 1 mode=%q, got %q", consts.ComputePartitionDPX, modes[1])
	}

	// Mutating the returned map should not affect the internal state
	modes[0] = "modified"
	if ps.gpuComputeModes[0] != consts.ComputePartitionCPX {
		t.Error("GetGPUComputeModes returned a reference instead of a copy")
	}
}

// TestPartitionState_BuildDevices_SortedByName verifies that the published
// synthetic-partition device list is sorted by name, so the ResourceSlice has a
// stable order across restarts/republishes (matching the standard path's
// resourceSliceDevices; see the sort added for GPUOP #74).
func TestPartitionState_BuildDevices_SortedByName(t *testing.T) {
	// Build an allocatable map whose Go iteration order is non-deterministic;
	// several synthetic devices across two GPUs and multiple partition configs.
	allocatable := make(AllocatableDevices)
	for _, gpuIndex := range []int{1, 0} {
		for _, cfg := range consts.ValidPartitionConfigs {
			dev := &SyntheticPartitionDevice{
				GPUIndex:         gpuIndex,
				ComputePartition: cfg.Compute,
				MemoryPartition:  cfg.Memory,
				PartitionCount:   cfg.PartitionCount,
			}
			ad := &AllocatableDevice{SyntheticPartition: dev}
			allocatable[ad.CanonicalName()] = ad
		}
	}

	ps := newTestPartitionState(map[int]string{0: "0000:19:00.0", 1: "0000:20:00.0"}, []int{0, 1}, allocatable)

	devices := ps.BuildDevices(allocatable)
	if len(devices) != len(allocatable) {
		t.Fatalf("expected %d devices, got %d", len(allocatable), len(devices))
	}

	for i := 1; i < len(devices); i++ {
		if devices[i-1].Name > devices[i].Name {
			t.Errorf("devices not sorted by name: %q came before %q", devices[i-1].Name, devices[i].Name)
		}
	}
}

// ---- Tests for per-share partition slot assignment ----

// TestPartitionState_MultipleSharesOfSameDeviceGetDistinctSlots is the regression
// test for partition-slot aliasing. A claim may hold several shares of one
// synthetic device (AllowMultipleAllocations), and each must land on a different
// physical partition. The slot used to be derived from the GPU's allocation
// count, so every share of a device read the same value and the containers were
// handed identical device nodes.
func TestPartitionState_MultipleSharesOfSameDeviceGetDistinctSlots(t *testing.T) {
	ps := newTestPartitionState(map[int]string{0: "0000:19:00.0"}, []int{0}, make(AllocatableDevices))

	shares := testShares("claim-1", "gpu-0-cpx-nps4", "gpu-0-cpx-nps4")
	if _, err := ps.ReserveClaim("claim-1", shares); err != nil {
		t.Fatalf("ReserveClaim: %v", err)
	}

	slot0, ok := ps.GetAssignedSlot(0, shares[0].ShareKey)
	if !ok {
		t.Fatal("no slot assigned for first share")
	}
	slot1, ok := ps.GetAssignedSlot(0, shares[1].ShareKey)
	if !ok {
		t.Fatal("no slot assigned for second share")
	}
	if slot0 == slot1 {
		t.Errorf("both shares of the same device got slot %d; expected distinct slots", slot0)
	}
	if ps.gpuAllocCounts[0] != 2 {
		t.Errorf("expected gpuAllocCounts[0]=2, got %d", ps.gpuAllocCounts[0])
	}
}

// TestPartitionState_ReleasedSlotIsReused verifies that releasing a share frees
// its slot for a later share, and that a non-LIFO release does not disturb the
// slot held by a share that is still active.
func TestPartitionState_ReleasedSlotIsReused(t *testing.T) {
	ps := newTestPartitionState(map[int]string{0: "0000:19:00.0"}, []int{0}, make(AllocatableDevices))

	a := testShares("claim-a", "gpu-0-cpx-nps4")
	b := testShares("claim-b", "gpu-0-cpx-nps4")
	if _, err := ps.ReserveClaim("claim-a", a); err != nil {
		t.Fatalf("ReserveClaim(a): %v", err)
	}
	if _, err := ps.ReserveClaim("claim-b", b); err != nil {
		t.Fatalf("ReserveClaim(b): %v", err)
	}

	slotA, _ := ps.GetAssignedSlot(0, a[0].ShareKey)
	slotB, _ := ps.GetAssignedSlot(0, b[0].ShareKey)
	if slotA == slotB {
		t.Fatalf("concurrent claims shared slot %d", slotA)
	}

	// Release the FIRST claim while the second is still active (non-LIFO).
	if _, err := ps.ReleaseClaim("claim-a", a); err != nil {
		t.Fatalf("ReleaseClaim(a): %v", err)
	}

	// The still-active claim must keep its slot.
	if got, ok := ps.GetAssignedSlot(0, b[0].ShareKey); !ok || got != slotB {
		t.Errorf("active claim's slot changed after unrelated release: got %d, ok=%v, want %d", got, ok, slotB)
	}

	// A new claim should take the freed slot, not collide with the active one.
	c := testShares("claim-c", "gpu-0-cpx-nps4")
	if _, err := ps.ReserveClaim("claim-c", c); err != nil {
		t.Fatalf("ReserveClaim(c): %v", err)
	}
	slotC, _ := ps.GetAssignedSlot(0, c[0].ShareKey)
	if slotC == slotB {
		t.Errorf("new claim collided with active claim on slot %d", slotC)
	}
	if slotC != slotA {
		t.Errorf("expected new claim to reuse freed slot %d, got %d", slotA, slotC)
	}
}

// TestPartitionState_ReserveClaimRetryKeepsSameSlot verifies that re-driving
// Prepare for an already-reserved claim (as kubelet does while an async memory
// reload converges) neither inflates the counts nor moves the assigned slot.
func TestPartitionState_ReserveClaimRetryKeepsSameSlot(t *testing.T) {
	ps := newTestPartitionState(map[int]string{0: "0000:19:00.0"}, []int{0}, make(AllocatableDevices))

	shares := testShares("claim-1", "gpu-0-cpx-nps4")
	if _, err := ps.ReserveClaim("claim-1", shares); err != nil {
		t.Fatalf("ReserveClaim: %v", err)
	}
	first, _ := ps.GetAssignedSlot(0, shares[0].ShareKey)

	for i := 0; i < 3; i++ {
		if _, err := ps.ReserveClaim("claim-1", shares); err != nil {
			t.Fatalf("ReserveClaim retry %d: %v", i, err)
		}
	}

	again, ok := ps.GetAssignedSlot(0, shares[0].ShareKey)
	if !ok || again != first {
		t.Errorf("slot changed across retries: got %d (ok=%v), want %d", again, ok, first)
	}
	if ps.totalAllocCount != 1 {
		t.Errorf("retries inflated totalAllocCount to %d, want 1", ps.totalAllocCount)
	}
}

// TestPartitionState_RecoverFromCheckpoint_RestoresSlotsWithoutPreparedClaims is
// the regression test for the reload-window checkpoint gap. A claim reserved but
// not yet prepared (ApplyPartition returned errReloadInProgress) is absent from
// preparedClaims. Rebuilding counts from preparedClaims alone left the node with
// an active memory mode and taints but a zero allocation count, which no release
// could ever clear. Slots are persisted at reservation time, so they carry it.
func TestPartitionState_RecoverFromCheckpoint_RestoresSlotsWithoutPreparedClaims(t *testing.T) {
	allocatable := make(AllocatableDevices)
	ps := newTestPartitionState(map[int]string{0: "0000:19:00.0"}, []int{0}, allocatable)

	shareKey := ShareKey("claim-inflight", "req0", "gpu-0-cpx-nps4", nil)
	assigned := map[int]map[string]int{0: {shareKey: 0}}

	// preparedClaims is empty: Prepare had not completed when the driver stopped.
	ps.RecoverFromCheckpoint(consts.MemoryPartitionNPS4,
		map[int]string{0: consts.ComputePartitionCPX},
		nil,
		PreparedClaims{},
		assigned,
	)

	if ps.totalAllocCount != 1 {
		t.Errorf("expected totalAllocCount=1 recovered from slots, got %d", ps.totalAllocCount)
	}
	if !ps.reservedClaims["claim-inflight"] {
		t.Error("expected in-flight claim to be marked reserved after recovery")
	}
	if got, ok := ps.GetAssignedSlot(0, shareKey); !ok || got != 0 {
		t.Errorf("expected recovered slot 0, got %d (ok=%v)", got, ok)
	}

	// The recovered reservation must be releasable, clearing the memory mode.
	taintsChanged, err := ps.ReleaseClaim("claim-inflight", []PartitionShare{
		{DeviceName: "gpu-0-cpx-nps4", ShareKey: shareKey},
	})
	if err != nil {
		t.Fatalf("ReleaseClaim: %v", err)
	}
	if !taintsChanged {
		t.Error("expected taintsChanged=true when last allocation released")
	}
	if ps.activeMemoryMode != "" {
		t.Errorf("memory mode not cleared after release: %q", ps.activeMemoryMode)
	}
}

// TestPartitionState_RecoverFromCheckpoint_FallsBackToPreparedClaims verifies
// that a checkpoint written before slots were persisted still recovers its
// allocation counts, and that recovered devices are given distinct slots.
func TestPartitionState_RecoverFromCheckpoint_FallsBackToPreparedClaims(t *testing.T) {
	ps := newTestPartitionState(map[int]string{0: "0000:19:00.0"}, []int{0}, make(AllocatableDevices))

	preparedClaims := PreparedClaims{
		"claim-old": {
			{Device: drapbv1.Device{DeviceName: "gpu-0-cpx-nps4"}},
			{Device: drapbv1.Device{DeviceName: "gpu-0-cpx-nps4"}},
		},
	}

	ps.RecoverFromCheckpoint(consts.MemoryPartitionNPS4,
		map[int]string{0: consts.ComputePartitionCPX},
		nil,
		preparedClaims,
		nil, // no persisted slots: pre-upgrade checkpoint
	)

	if ps.totalAllocCount != 2 {
		t.Errorf("expected totalAllocCount=2 from prepared claims, got %d", ps.totalAllocCount)
	}
	if !ps.reservedClaims["claim-old"] {
		t.Error("expected recovered claim to be marked reserved")
	}

	// Both recovered devices must hold distinct slots.
	seen := make(map[int]bool)
	ps.mu.Lock()
	for _, slot := range ps.assignedSlots[0] {
		if seen[slot] {
			t.Errorf("duplicate slot %d assigned during fallback recovery", slot)
		}
		seen[slot] = true
	}
	ps.mu.Unlock()
	if len(seen) != 2 {
		t.Errorf("expected 2 distinct recovered slots, got %d", len(seen))
	}
}

// TestPartitionState_ReleaseClaim_ErrorKeepsClaimReserved verifies that a failed
// release does not drop the claim's reservation marker. Dropping it would strand
// the remaining counts, so totalAllocCount could never reach zero and the node
// memory mode and its taints would never clear.
func TestPartitionState_ReleaseClaim_ErrorKeepsClaimReserved(t *testing.T) {
	ps := newTestPartitionState(map[int]string{0: "0000:19:00.0"}, []int{0}, make(AllocatableDevices))

	good := PartitionShare{DeviceName: "gpu-0-cpx-nps4", ShareKey: testShareKey("claim-1", "gpu-0-cpx-nps4")}
	if _, err := ps.ReserveClaim("claim-1", []PartitionShare{good}); err != nil {
		t.Fatalf("ReserveClaim: %v", err)
	}

	// A device name that cannot be parsed makes ReleasePartition fail.
	bad := PartitionShare{DeviceName: "not-a-valid-name", ShareKey: "k"}
	if _, err := ps.ReleaseClaim("claim-1", []PartitionShare{bad, good}); err == nil {
		t.Fatal("expected error from ReleaseClaim when a device fails to release")
	}

	ps.mu.Lock()
	stillReserved := ps.reservedClaims["claim-1"]
	ps.mu.Unlock()
	if !stillReserved {
		t.Error("claim marker was dropped despite a failed release; remaining counts would be stranded")
	}

	// Retrying with only the good share must now balance the counts.
	if _, err := ps.ReleaseClaim("claim-1", []PartitionShare{good}); err != nil {
		t.Fatalf("retry ReleaseClaim: %v", err)
	}
	if ps.totalAllocCount != 0 {
		t.Errorf("expected totalAllocCount=0 after successful retry, got %d", ps.totalAllocCount)
	}
}

// TestPartitionState_HasReservation guards the primitive added for the
// Unprepare cleanup-gap fix: a claim that reserved a partition but never
// finished Prepare (errReloadInProgress persists AssignedSlots without ever
// recording the claim as prepared) must still report as reserved, and must
// stop reporting so once released.
func TestPartitionState_HasReservation(t *testing.T) {
	ps := newTestPartitionState(map[int]string{0: "0000:19:00.0"}, []int{0}, make(AllocatableDevices))

	if ps.HasReservation("claim-1") {
		t.Error("expected no reservation before ReserveClaim")
	}

	shares := testShares("claim-1", "gpu-0-cpx-nps4")
	if _, err := ps.ReserveClaim("claim-1", shares); err != nil {
		t.Fatalf("ReserveClaim: %v", err)
	}
	if !ps.HasReservation("claim-1") {
		t.Error("expected a reservation to be held after ReserveClaim, even though nothing was ever \"prepared\"")
	}
	if ps.HasReservation("claim-2") {
		t.Error("HasReservation must be per-claim, not global")
	}

	if _, err := ps.ReleaseClaim("claim-1", shares); err != nil {
		t.Fatalf("ReleaseClaim: %v", err)
	}
	if ps.HasReservation("claim-1") {
		t.Error("expected no reservation after ReleaseClaim")
	}
}

// TestPartitionState_PartitionSharesFromAssignedSlots guards the reconstruction
// path Unprepare uses when preparedClaims has no entry for a claim that still
// holds a reservation (see HasReservation): the shares must be recoverable from
// assignedSlots alone, and must be scoped to the requested claim only.
func TestPartitionState_PartitionSharesFromAssignedSlots(t *testing.T) {
	ps := newTestPartitionState(map[int]string{0: "0000:19:00.0"}, []int{0}, make(AllocatableDevices))

	claim1Shares := testShares("claim-1", "gpu-0-cpx-nps4")
	if _, err := ps.ReserveClaim("claim-1", claim1Shares); err != nil {
		t.Fatalf("ReserveClaim claim-1: %v", err)
	}
	claim2Shares := []PartitionShare{{
		DeviceName: "gpu-0-cpx-nps4",
		ShareKey:   ShareKey("claim-2", "req0", "gpu-0-cpx-nps4", nil),
	}}
	if _, err := ps.ReserveClaim("claim-2", claim2Shares); err != nil {
		t.Fatalf("ReserveClaim claim-2: %v", err)
	}

	got := ps.PartitionSharesFromAssignedSlots("claim-1")
	if len(got) != 1 {
		t.Fatalf("expected 1 share for claim-1, got %d: %v", len(got), got)
	}
	if got[0].DeviceName != "gpu-0-cpx-nps4" {
		t.Errorf("expected DeviceName gpu-0-cpx-nps4, got %q", got[0].DeviceName)
	}
	if got[0].ShareKey != claim1Shares[0].ShareKey {
		t.Errorf("expected ShareKey %q, got %q", claim1Shares[0].ShareKey, got[0].ShareKey)
	}

	// Reconstructed shares must actually work as ReleaseClaim input, restoring
	// the exact release path Unprepare takes for a never-fully-prepared claim.
	if _, err := ps.ReleaseClaim("claim-1", got); err != nil {
		t.Fatalf("ReleaseClaim with reconstructed shares: %v", err)
	}
	if ps.HasReservation("claim-1") {
		t.Error("expected claim-1 released")
	}
	if !ps.HasReservation("claim-2") {
		t.Error("claim-2 must be unaffected by releasing claim-1")
	}

	if got := ps.PartitionSharesFromAssignedSlots("no-such-claim"); got != nil {
		t.Errorf("expected nil shares for a claim with no assigned slots, got %v", got)
	}
}
