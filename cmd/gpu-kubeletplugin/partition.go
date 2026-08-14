/*
Copyright (c) Advanced Micro Devices, Inc. All rights reserved.

Licensed under the Apache License, Version 2.0 (the \"License\");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an \"AS IS\" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/amdsmi"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/consts"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/kmm"
	resourceapi "k8s.io/api/resource/v1"
	klog "k8s.io/klog/v2"
)

// errReloadInProgress is returned by ApplyPartition when a KMM-managed driver
// reload has been triggered for a memory partition change but the hardware has
// not yet converged to the requested mode. It is a retryable signal: the caller
// must NOT roll back the reservation; kubelet re-drives Prepare and ApplyPartition
// polls sysfs until convergence.
var errReloadInProgress = errors.New("kmm driver reload in progress, memory partition not yet converged")

// IsReloadInProgress reports whether err indicates a KMM reload is still
// converging (a retryable, no-rollback condition).
func IsReloadInProgress(err error) bool {
	return errors.Is(err, errReloadInProgress)
}

// kmmReloadDeadline bounds how long we wait for a KMM-managed reload to converge
// before giving up and returning a hard error (which triggers rollback).
const kmmReloadDeadline = 10 * time.Minute

// PartitionState tracks the active partition state for all GPUs on the node.
// It manages compute mode per GPU and memory mode per node, and coordinates
// libamd_smi calls for GPU reconfiguration.
type PartitionState struct {
	mu sync.Mutex

	// activeMemoryMode is the currently locked memory partition mode for the node.
	// Empty string means unlocked (any mode can be requested).
	activeMemoryMode string

	// gpuComputeModes tracks the active compute partition mode per physical GPU.
	// Key is gpuIndex, value is the compute mode string (e.g., "cpx").
	gpuComputeModes map[int]string

	// gpuAllocCounts tracks the number of active allocations per physical GPU.
	gpuAllocCounts map[int]int

	// totalAllocCount is the total number of active partition allocations across all GPUs.
	totalAllocCount int

	// gpuPCIAddresses maps GPU index to PCI address (used by discoverPartitionDeviceNodes).
	gpuPCIAddresses map[int]string

	// partitionableGPUs is the list of GPU indices that support compute partitioning.
	// Used for building shared counter sets in the ResourceSlice.
	partitionableGPUs []int

	// allocatable is a reference to the allocatable devices map for taint updates.
	allocatable AllocatableDevices

	// kmmEnabled indicates the amdgpu driver on this node is KMM-managed. When
	// true, the amd-smi driver reload is skipped after a memory partition change
	// (a manual reload would restore the inbox driver instead of the KMM one) and
	// the KMM recovery path (recoverer) is used instead.
	kmmEnabled bool

	// recoverer triggers a KMM-managed driver reload (modprobe + NodeModulesConfig
	// delete). Non-nil only when kmmEnabled and a dynamic client was provided.
	recoverer *kmm.Recoverer

	// memoryReload records an in-flight KMM reload for a memory-mode change. It is
	// checkpointed so a restart mid-reload resumes polling instead of re-triggering.
	// nil when no reload is in flight.
	memoryReload *MemoryReloadMarker

	// reservedClaims tracks which claim UIDs currently hold a reservation, so that
	// re-driving Prepare for the same claim (as kubelet does on every retry while
	// an async memory reload converges) does not double-count allocations. It is
	// rebuilt from the checkpoint's prepared claims on restart (see
	// RecoverFromCheckpoint), so a recovered claim releases exactly once.
	reservedClaims map[string]bool
}

// NewPartitionState creates a new PartitionState with the given GPU PCI addresses
// and the list of GPU indices that support partitioning. kmmEnabled selects the
// KMM-managed driver reload path; recoverer (may be nil) performs it.
func NewPartitionState(gpuPCIAddresses map[int]string, partitionableGPUs []int, allocatable AllocatableDevices, kmmEnabled bool, recoverer *kmm.Recoverer) *PartitionState {
	return &PartitionState{
		gpuComputeModes:   make(map[int]string),
		gpuAllocCounts:    make(map[int]int),
		gpuPCIAddresses:   gpuPCIAddresses,
		partitionableGPUs: partitionableGPUs,
		allocatable:       allocatable,
		kmmEnabled:        kmmEnabled,
		recoverer:         recoverer,
		reservedClaims:    make(map[string]bool),
	}
}

// ReserveClaim is the claim-scoped, idempotent entry point for phase 1 of the
// two-phase partition setup. It reserves every partition device in the claim
// (via ReservePartition) exactly once per claim UID: a repeat call for a claim
// that is already reserved is a no-op returning taintsChanged=false. This is
// what makes kubelet retries safe — Prepare is re-driven on every retry while an
// async memory reload converges, and without this guard each retry would
// re-increment the per-GPU/total allocation counts, corrupting the partition
// index math and preventing taints/memory mode from ever clearing on release.
//
// Reservation is all-or-nothing: if any device conflicts, devices already
// reserved for THIS call are rolled back and the claim is left unreserved so a
// later retry re-evaluates cleanly.
func (ps *PartitionState) ReserveClaim(claimUID string, deviceNames []string) (taintsChanged bool, err error) {
	ps.mu.Lock()
	alreadyReserved := ps.reservedClaims[claimUID]
	ps.mu.Unlock()
	if alreadyReserved {
		return false, nil
	}

	var reserved []string
	for _, deviceName := range deviceNames {
		changed, rerr := ps.ReservePartition(deviceName)
		if rerr != nil {
			for _, d := range reserved {
				if _, relErr := ps.ReleasePartition(d); relErr != nil {
					klog.Warningf("error rolling back device %s during failed claim %s reservation: %v", d, claimUID, relErr)
				}
			}
			return false, rerr
		}
		taintsChanged = taintsChanged || changed
		reserved = append(reserved, deviceName)
	}

	ps.mu.Lock()
	ps.reservedClaims[claimUID] = true
	ps.mu.Unlock()
	return taintsChanged, nil
}

// ReleaseClaim is the claim-scoped, idempotent counterpart to ReserveClaim. It
// releases every partition device in the claim (via ReleasePartition) exactly
// once per claim UID; a call for a claim that is not currently reserved is a
// no-op returning taintsChanged=false. This mirrors ReserveClaim so that the
// per-GPU/total allocation counts balance out regardless of how many times
// Prepare/Unprepare are re-driven.
func (ps *PartitionState) ReleaseClaim(claimUID string, deviceNames []string) (taintsChanged bool, err error) {
	ps.mu.Lock()
	reserved := ps.reservedClaims[claimUID]
	ps.mu.Unlock()
	if !reserved {
		return false, nil
	}

	for _, deviceName := range deviceNames {
		changed, rerr := ps.ReleasePartition(deviceName)
		if rerr != nil {
			klog.Warningf("error releasing device %s for claim %s: %v", deviceName, claimUID, rerr)
			continue
		}
		taintsChanged = taintsChanged || changed
	}

	ps.mu.Lock()
	delete(ps.reservedClaims, claimUID)
	ps.mu.Unlock()
	return taintsChanged, nil
}

// ReservePartition is phase 1 of a two-phase partition setup. It performs only
// fast, in-memory bookkeeping: it validates the requested compute/memory modes
// against any already-active modes, records the reservation, stamps memory-mode
// conflict taints in-memory, and increments allocation counts. It performs NO
// hardware work, so the caller can publish the resulting taints to the API
// server before the slow ApplyPartition step runs. Returns whether taints
// changed (so the caller knows to re-publish) and an error on mode conflict.
func (ps *PartitionState) ReservePartition(deviceName string) (taintsChanged bool, err error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	gpuIndex, computeMode, memoryMode, err := parseSyntheticPartitionDeviceName(deviceName)
	if err != nil {
		return false, fmt.Errorf("error parsing device name for partition reserve: %v", err)
	}

	// Validate compute partition mode on this GPU against the tracked mode.
	if existingMode, ok := ps.gpuComputeModes[gpuIndex]; ok {
		if existingMode != computeMode {
			return false, fmt.Errorf("GPU %d is already in compute mode %q, cannot switch to %q while allocations exist",
				gpuIndex, existingMode, computeMode)
		}
	}

	// Validate memory partition mode on the node against the tracked mode.
	if ps.activeMemoryMode != "" && ps.activeMemoryMode != memoryMode {
		return false, fmt.Errorf("node is already in memory mode %q, cannot switch to %q while allocations exist",
			ps.activeMemoryMode, memoryMode)
	}

	// Reservation is valid: record modes.
	ps.gpuComputeModes[gpuIndex] = computeMode
	if ps.activeMemoryMode == "" {
		ps.activeMemoryMode = memoryMode
		// Stamp taints on devices with incompatible memory modes (in-memory only;
		// the caller publishes them before ApplyPartition runs).
		ps.applyMemoryTaints(memoryMode)
		taintsChanged = true
	}

	// Increment allocation counts.
	ps.gpuAllocCounts[gpuIndex]++
	ps.totalAllocCount++

	klog.Infof("Partition reserved: GPU %d, compute=%s, memory=%s, gpuAllocs=%d, totalAllocs=%d, taintsChanged=%t",
		gpuIndex, computeMode, memoryMode, ps.gpuAllocCounts[gpuIndex], ps.totalAllocCount, taintsChanged)

	return taintsChanged, nil
}

// ApplyPartition is phase 2 of a two-phase partition setup. It performs the
// slow hardware reconfiguration via libamd_smi (with a sysfs fallback), setting
// the compute mode on the target GPU and the memory mode on all partitionable
// GPUs. The memory-mode change triggers a driver reload, which is the expensive
// part of this call.
//
// Idempotency is keyed entirely on sysfs readback, NOT on the in-memory tracked
// state: a GPU already in the requested mode is skipped. This is what makes
// kubelet-driven retries (and recovery after a mid-reload crash) converge
// without re-triggering a reload. It deliberately does not consult
// activeMemoryMode/gpuComputeModes, since those reflect the reservation rather
// than the actual hardware state.
func (ps *PartitionState) ApplyPartition(deviceName string) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	gpuIndex, computeMode, memoryMode, err := parseSyntheticPartitionDeviceName(deviceName)
	if err != nil {
		return fmt.Errorf("error parsing device name for partition apply: %v", err)
	}

	// Phase A — memory partition (node-wide, requires a driver reload). This MUST
	// run BEFORE the compute partition: the reload has to happen over the clean
	// top-level GPU topology. If compute is set first, the target GPU splits into
	// CPX/DPX sub-partition devices and the extra open handles make the reload fail
	// with AMDSMI_STATUS_AMDGPU_RESTART_ERR (54). This mirrors device-config-manager,
	// which reloads before touching compute (validated: DCM reloads fine on the same
	// node/driver where our old compute-first order got status 54).
	//
	// The reload is asynchronous (KMM operator out-of-band, or an in-process
	// goroutine for the amd-smi reload). A marker means one is in flight: poll
	// sysfs for convergence instead of re-issuing set calls that would collide
	// (AMDSMI_STATUS_BUSY). On convergence we fall through to the compute partition.
	if ps.memoryReload != nil {
		if err := ps.pollMemoryReload(memoryMode); err != nil {
			return err
		}
		// Converged (marker cleared): proceed to the compute partition below.
	} else if !ps.memoryConvergedLocked(memoryMode) {
		// Stage the memory mode on every partitionable GPU that is not already
		// there; it only takes effect after the driver reload.
		klog.Infof("Setting memory partition mode %q on all partitionable GPUs via libamd_smi", memoryMode)
		for _, gpuIdx := range ps.partitionableGPUs {
			if strings.EqualFold(ps.readCurrentMemoryPartition(gpuIdx), memoryMode) {
				continue
			}
			if err := amdsmi.SetMemoryPartition(gpuIdx, memoryMode); err != nil {
				klog.Warningf("AMD SMI failed to set memory partition on GPU %d: %v, trying sysfs fallback", gpuIdx, err)
				if sysfsErr := ps.writeMemoryPartitionViaSysfs(gpuIdx, memoryMode); sysfsErr != nil {
					return fmt.Errorf("failed to set memory partition on GPU %d (amdsmi: %v, sysfs: %v)", gpuIdx, err, sysfsErr)
				}
			}
		}
		// Trigger the reload asynchronously and return errReloadInProgress. Kubelet
		// re-drives Prepare, which re-enters here and polls sysfs (via the marker
		// branch above) until the mode converges. This keeps ApplyPartition from
		// blocking under the DeviceState lock through a multi-minute reload.
		return ps.triggerMemoryReload(memoryMode)
	}

	// Phase B — compute partition (per-GPU, no reload). Reached only once the memory
	// mode has converged, so the reload above ran over the pre-split topology. Skip
	// if sysfs already reports the requested mode (idempotent retry / recovery).
	if currentMode := ps.readCurrentComputePartition(gpuIndex); strings.EqualFold(currentMode, computeMode) {
		klog.Infof("GPU %d already in compute mode %q (via sysfs), skipping partition set", gpuIndex, computeMode)
	} else {
		klog.Infof("Setting compute partition mode %q on GPU %d (current: %q)", computeMode, gpuIndex, currentMode)
		if err := amdsmi.SetComputePartition(gpuIndex, computeMode); err != nil {
			klog.Warningf("AMD SMI failed to set compute partition on GPU %d: %v, trying sysfs fallback", gpuIndex, err)
			if sysfsErr := ps.writeComputePartitionViaSysfs(gpuIndex, computeMode); sysfsErr != nil {
				return fmt.Errorf("failed to set compute partition on GPU %d (amdsmi: %v, sysfs: %v)", gpuIndex, err, sysfsErr)
			}
		}
	}

	klog.Infof("Partition apply complete: GPU %d, compute=%s, memory=%s", gpuIndex, computeMode, memoryMode)
	return nil
}

// triggerMemoryReload starts the driver reload for a staged memory-mode change
// and records a checkpointed marker, returning errReloadInProgress. Callers must
// hold ps.mu. On a KMM node the reload is delegated to the KMM operator (modprobe
// + NodeModulesConfig delete). On a non-KMM node the blocking amd-smi reload is
// run in a background goroutine so this call returns immediately.
func (ps *PartitionState) triggerMemoryReload(memoryMode string) error {
	if ps.kmmEnabled {
		if ps.recoverer == nil {
			return fmt.Errorf("KMM enabled but no recoverer configured; cannot reload driver for memory mode %q", memoryMode)
		}
		klog.Infof("Triggering KMM-managed driver reload for memory mode %q", memoryMode)
		if err := ps.recoverer.TriggerReload(context.TODO()); err != nil {
			return fmt.Errorf("failed to trigger KMM driver reload for memory mode %q: %v", memoryMode, err)
		}
	} else {
		klog.Infof("Triggering async amd-smi driver reload for memory mode %q", memoryMode)
		go func() {
			if err := amdsmi.ReloadDriver(); err != nil {
				klog.Warningf("async amd-smi driver reload for memory mode %q failed: %v (will be retried/observed via sysfs)", memoryMode, err)
			} else {
				klog.Infof("async amd-smi driver reload for memory mode %q completed", memoryMode)
			}
		}()
	}
	ps.memoryReload = &MemoryReloadMarker{Mode: memoryMode, TriggeredAtUnix: time.Now().Unix()}
	return errReloadInProgress
}

// pollMemoryReload is entered on a retry while a reload marker exists. It reports
// convergence via sysfs, enforces the deadline, and never re-issues hardware
// writes. Callers must hold ps.mu.
func (ps *PartitionState) pollMemoryReload(memoryMode string) error {
	// A marker for a different mode should never happen (node memory mode is
	// single-writer under the mutex), but fail loudly rather than silently wait.
	if !strings.EqualFold(ps.memoryReload.Mode, memoryMode) {
		return fmt.Errorf("a driver reload for memory mode %q is already in progress; cannot apply %q", ps.memoryReload.Mode, memoryMode)
	}
	if ps.memoryConvergedLocked(memoryMode) {
		klog.Infof("Driver reload converged: all GPUs now in memory mode %q", memoryMode)
		ps.memoryReload = nil
		return nil
	}
	elapsed := time.Since(time.Unix(ps.memoryReload.TriggeredAtUnix, 0))
	if elapsed > kmmReloadDeadline {
		ps.memoryReload = nil
		return fmt.Errorf("driver reload for memory mode %q did not converge within %v", memoryMode, kmmReloadDeadline)
	}
	klog.Infof("Driver reload for memory mode %q still converging (%v elapsed), waiting for kubelet retry", memoryMode, elapsed.Round(time.Second))
	return errReloadInProgress
}

// memoryConvergedLocked reports whether every partitionable GPU reads back the
// given memory mode from sysfs. Callers must hold ps.mu.
func (ps *PartitionState) memoryConvergedLocked(memoryMode string) bool {
	for _, gpuIdx := range ps.partitionableGPUs {
		if !strings.EqualFold(ps.readCurrentMemoryPartition(gpuIdx), memoryMode) {
			return false
		}
	}
	return true
}

// GetMemoryReloadMarker returns a copy of the in-flight KMM reload marker (nil if
// none), for persisting to the checkpoint.
func (ps *PartitionState) GetMemoryReloadMarker() *MemoryReloadMarker {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.memoryReload == nil {
		return nil
	}
	m := *ps.memoryReload
	return &m
}

// ReleasePartition handles cleanup when a partition allocation is released. It
// is used both for normal unprepare and to roll back a reservation when a later
// phase of Prepare fails. It decrements allocation counts and returns whether
// taints changed (indicating that ResourceSlice needs to be re-published).
//
// Rollback is in-memory only: it never reverts the hardware partition mode. The
// hardware may be left repartitioned; the next kubelet-driven Prepare reconciles
// against sysfs via ApplyPartition's idempotent skip.
func (ps *PartitionState) ReleasePartition(deviceName string) (taintsChanged bool, err error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	gpuIndex, _, _, err := parseSyntheticPartitionDeviceName(deviceName)
	if err != nil {
		return false, fmt.Errorf("error parsing device name for partition release: %v", err)
	}

	// Decrement allocation counts
	if ps.gpuAllocCounts[gpuIndex] > 0 {
		ps.gpuAllocCounts[gpuIndex]--
	}
	if ps.totalAllocCount > 0 {
		ps.totalAllocCount--
	}

	// If no more allocations on this GPU, clear its compute mode
	if ps.gpuAllocCounts[gpuIndex] == 0 {
		delete(ps.gpuComputeModes, gpuIndex)
		delete(ps.gpuAllocCounts, gpuIndex)
		klog.Infof("GPU %d has no more allocations, compute mode cleared", gpuIndex)
	}

	// If no more allocations on the entire node, clear memory mode and remove taints
	if ps.totalAllocCount == 0 {
		klog.Infof("No more partition allocations on node, clearing memory mode %q and removing taints",
			ps.activeMemoryMode)
		ps.activeMemoryMode = ""
		ps.removeMemoryTaints()
		taintsChanged = true
	}

	return taintsChanged, nil
}

// GetActiveMemoryMode returns the currently active memory partition mode.
func (ps *PartitionState) GetActiveMemoryMode() string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.activeMemoryMode
}

// GetGPUComputeModes returns a copy of the GPU compute modes map.
func (ps *PartitionState) GetGPUComputeModes() map[int]string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	result := make(map[int]string, len(ps.gpuComputeModes))
	for k, v := range ps.gpuComputeModes {
		result[k] = v
	}
	return result
}

// RecoverFromCheckpoint reconstructs partition state from checkpoint data.
// This is called on driver restart to recover the active memory mode, per-GPU
// compute modes, and any in-flight KMM memory-reload marker from the persisted
// checkpoint. Recovering the marker ensures a restart mid-reload resumes polling
// for convergence instead of re-triggering the reload.
func (ps *PartitionState) RecoverFromCheckpoint(activeMemoryMode string, gpuComputeModes map[int]string, memoryReload *MemoryReloadMarker, preparedClaims PreparedClaims) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	ps.activeMemoryMode = activeMemoryMode

	if memoryReload != nil {
		m := *memoryReload
		ps.memoryReload = &m
	}

	if gpuComputeModes != nil {
		ps.gpuComputeModes = make(map[int]string, len(gpuComputeModes))
		for k, v := range gpuComputeModes {
			ps.gpuComputeModes[k] = v
		}
	}

	// Reconstruct allocation counts and the reserved-claim set from prepared
	// claims. Marking each recovered claim as reserved keeps the idempotency
	// guard consistent so a post-restart Unprepare (ReleaseClaim) decrements the
	// counts instead of no-oping.
	ps.gpuAllocCounts = make(map[int]int)
	ps.totalAllocCount = 0
	ps.reservedClaims = make(map[string]bool)

	for claimUID, devices := range preparedClaims {
		claimHasPartitionDevice := false
		for _, device := range devices {
			gpuIndex, _, _, err := parseSyntheticPartitionDeviceName(device.DeviceName)
			if err != nil {
				// Not a synthetic-partition device, skip
				continue
			}
			ps.gpuAllocCounts[gpuIndex]++
			ps.totalAllocCount++
			claimHasPartitionDevice = true
		}
		if claimHasPartitionDevice {
			ps.reservedClaims[claimUID] = true
		}
	}

	// Re-apply memory taints if a memory mode is active
	if ps.activeMemoryMode != "" {
		ps.applyMemoryTaints(ps.activeMemoryMode)
	}

	klog.Infof("Partition state recovered: memoryMode=%q, computeModes=%v, totalAllocs=%d",
		ps.activeMemoryMode, ps.gpuComputeModes, ps.totalAllocCount)
}

// applyMemoryTaints adds NoExecute taints to all synthetic-partition devices
// whose memory mode is incompatible with the given active mode.
func (ps *PartitionState) applyMemoryTaints(activeMemoryMode string) {
	for _, device := range ps.allocatable {
		if device.SyntheticPartition == nil {
			continue
		}
		ap := device.SyntheticPartition
		if ap.MemoryPartition != activeMemoryMode {
			ap.Taints = []resourceapi.DeviceTaint{
				{
					Key:    consts.MemoryPartitionTaintKey,
					Value:  activeMemoryMode,
					Effect: resourceapi.DeviceTaintEffectNoExecute,
				},
			}
		} else {
			// Clear any taints from compatible devices
			ap.Taints = nil
		}
	}
}

// removeMemoryTaints removes all memory partition taints from synthetic-partition devices.
func (ps *PartitionState) removeMemoryTaints() {
	for _, device := range ps.allocatable {
		if device.SyntheticPartition == nil {
			continue
		}
		device.SyntheticPartition.Taints = nil
	}
}

// HasTaints returns true if any synthetic-partition devices currently have taints.
func (ps *PartitionState) HasTaints() bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.activeMemoryMode != ""
}

// BuildDevices returns the DRA Device representation for every allocatable
// device, snapshotting under ps.mu so that reads of the dynamically-updated
// SyntheticPartitionDevice.Taints field are synchronized with the taint writes
// in applyMemoryTaints/removeMemoryTaints.
//
// The devices are sorted by name so the published ResourceSlice has a stable,
// deterministic order across restarts (and republishes), matching the standard
// path's resourceSliceDevices. The order is also the first-fit allocation
// priority the scheduler applies.
func (ps *PartitionState) BuildDevices(allocatable AllocatableDevices) []resourceapi.Device {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	devices := make([]resourceapi.Device, 0, len(allocatable))
	for _, device := range allocatable {
		devices = append(devices, device.GetDevice())
	}
	slices.SortFunc(devices, func(a, b resourceapi.Device) int {
		return cmp.Compare(a.Name, b.Name)
	})
	return devices
}

// writeComputePartitionViaSysfs sets the compute partition mode via sysfs as a fallback.
func (ps *PartitionState) writeComputePartitionViaSysfs(gpuIndex int, mode string) error {
	pciAddr, ok := ps.gpuPCIAddresses[gpuIndex]
	if !ok {
		return fmt.Errorf("no PCI address for GPU %d", gpuIndex)
	}
	path := filepath.Join("/sys/bus/pci/devices", pciAddr, "current_compute_partition")
	if err := os.WriteFile(path, []byte(strings.ToUpper(mode)), 0644); err != nil {
		return fmt.Errorf("sysfs write to %s failed: %v", path, err)
	}
	klog.Infof("Successfully set compute partition mode %q on GPU %d via sysfs", strings.ToUpper(mode), gpuIndex)
	return nil
}

// writeMemoryPartitionViaSysfs sets the memory partition mode via sysfs as a fallback.
func (ps *PartitionState) writeMemoryPartitionViaSysfs(gpuIndex int, mode string) error {
	pciAddr, ok := ps.gpuPCIAddresses[gpuIndex]
	if !ok {
		return fmt.Errorf("no PCI address for GPU %d", gpuIndex)
	}
	path := filepath.Join("/sys/bus/pci/devices", pciAddr, "current_memory_partition")
	if err := os.WriteFile(path, []byte(strings.ToUpper(mode)), 0644); err != nil {
		return fmt.Errorf("sysfs write to %s failed: %v", path, err)
	}
	klog.Infof("Successfully set memory partition mode %q on GPU %d via sysfs", strings.ToUpper(mode), gpuIndex)
	return nil
}

// readCurrentComputePartition reads the current compute partition mode from sysfs.
func (ps *PartitionState) readCurrentComputePartition(gpuIndex int) string {
	pciAddr, ok := ps.gpuPCIAddresses[gpuIndex]
	if !ok {
		return ""
	}
	path := filepath.Join("/sys/bus/pci/devices", pciAddr, "current_compute_partition")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// readCurrentMemoryPartition reads the current memory partition mode from sysfs.
func (ps *PartitionState) readCurrentMemoryPartition(gpuIndex int) string {
	pciAddr, ok := ps.gpuPCIAddresses[gpuIndex]
	if !ok {
		return ""
	}
	path := filepath.Join("/sys/bus/pci/devices", pciAddr, "current_memory_partition")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
