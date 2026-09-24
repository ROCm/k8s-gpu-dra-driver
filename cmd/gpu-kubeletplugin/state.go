/*
 * Copyright 2023 The Kubernetes Authors.
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
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"sync"
	"syscall"

	configapi "github.com/ROCm/k8s-gpu-dra-driver/api/amd.com/resource/gpu/v1alpha1"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/amdgpu"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/consts"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/featuregates"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/kmm"
	"golang.org/x/sys/unix"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/runtime"
	klog "k8s.io/klog/v2"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	cdispec "tags.cncf.io/container-device-interface/specs-go"
)

type PreparedDevices []*PreparedDevice
type PreparedClaims map[string]PreparedDevices
type PerDeviceCDIContainerEdits map[string]*cdiapi.ContainerEdits

type OpaqueDeviceConfig struct {
	Requests []string
	Config   runtime.Object
}

type PreparedDevice struct {
	drapbv1.Device
	ContainerEdits *cdiapi.ContainerEdits

	// PartitionSlot is the partition slot this share was assigned on its GPU, for
	// synthetic-partition devices. nil for regular GPU and VFIO devices. It is
	// persisted so Unprepare rebuilds the same CDI device name that Prepare wrote.
	PartitionSlot *int `json:"partitionSlot,omitempty"`

	// ShareKey identifies the allocation share this device represents (see
	// ShareKey). Persisted so Unprepare releases the exact slot that was reserved.
	ShareKey string `json:"shareKey,omitempty"`
}

func (pds PreparedDevices) GetDevices() []*drapbv1.Device {
	var devices []*drapbv1.Device
	for _, pd := range pds {
		devices = append(devices, &pd.Device)
	}
	return devices
}

type DeviceState struct {
	sync.Mutex
	cdi                  *CDIHandler
	allocatable          AllocatableDevices
	checkpointManager    checkpointmanager.CheckpointManager
	vfioManager          *VfioPciManager
	claimVfioConversions map[string]*AmdGpuInfo
	partitionState       *PartitionState
	driver               *driver // back-reference for re-publishing resources
	syntheticPartition   bool    // whether synthetic-partition mode is enabled
	nodeName             string  // this node's name, matched against allocation results' Pool
}

func NewDeviceState(config *Config) (*DeviceState, error) {
	autoPartition := featuregates.Enabled(featuregates.AutoPartition)
	allocatable, gpuPCIAddresses, partitionableGPUs, err := enumerateAllPossibleDevices(autoPartition)
	if err != nil {
		return nil, fmt.Errorf("error enumerating devices: %v", err)
	}

	cdi, err := NewCDIHandler(config)
	if err != nil {
		return nil, fmt.Errorf("unable to create CDI handler: %v", err)
	}

	err = cdi.CreateCommonSpecFile()
	if err != nil {
		return nil, fmt.Errorf("unable to create CDI spec file for common edits: %v", err)
	}

	checkpointManager, err := checkpointmanager.NewCheckpointManager(config.DriverPluginPath())
	if err != nil {
		return nil, fmt.Errorf("unable to create checkpoint manager: %v", err)
	}

	var vfioMgr *VfioPciManager
	if featuregates.Enabled(featuregates.VFIOPassthrough) {
		var err2 error
		vfioMgr, err2 = NewVfioPciManager()
		if err2 != nil {
			klog.Warningf("VFIO manager initialization failed (VFIO passthrough unavailable): %v", err2)
			vfioMgr = nil
			for name, dev := range allocatable {
				if dev.Type() == consts.VfioDeviceType {
					delete(allocatable, name)
				}
			}
		}
	}

	state := &DeviceState{
		cdi:                cdi,
		allocatable:        allocatable,
		vfioManager:        vfioMgr,
		checkpointManager:  checkpointManager,
		syntheticPartition: autoPartition,
		nodeName:           config.flags.nodeName,
	}

	// Set up partition state for synthetic-partition mode
	if autoPartition {
		kmmEnabled := kmm.IsDriverEnabled()
		var recoverer *kmm.Recoverer
		if kmmEnabled {
			if config.dynamicClient != nil {
				recoverer = kmm.NewRecoverer(config.dynamicClient, config.flags.nodeName)
			} else {
				klog.Warningf("KMM driver enabled but no dynamic client available; KMM reload recovery will fail")
			}
		}
		state.partitionState = NewPartitionState(gpuPCIAddresses, partitionableGPUs, allocatable, kmmEnabled, recoverer)
	}

	checkpoints, err := state.checkpointManager.ListCheckpoints()
	if err != nil {
		return nil, fmt.Errorf("unable to list checkpoints: %v", err)
	}

	checkpointExists := false
	for _, c := range checkpoints {
		if c == DriverPluginCheckpointFile {
			checkpointExists = true
			break
		}
	}

	if checkpointExists {
		// Recover partition state from checkpoint if synthetic-partition is enabled
		if autoPartition && state.partitionState != nil {
			checkpoint := newCheckpoint()
			if err := state.checkpointManager.GetCheckpoint(DriverPluginCheckpointFile, checkpoint); err != nil {
				klog.Warningf("Failed to read checkpoint for partition state recovery: %v", err)
			} else {
				state.partitionState.RecoverFromCheckpoint(
					checkpoint.V1.ActiveMemoryMode,
					checkpoint.V1.GPUComputeModes,
					checkpoint.V1.MemoryReload,
					checkpoint.V1.PreparedClaims,
					checkpoint.V1.AssignedSlots,
				)
			}
		}
		return state, nil
	}

	checkpoint := newCheckpoint()
	if err := state.checkpointManager.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint); err != nil {
		return nil, fmt.Errorf("unable to sync to checkpoint: %v", err)
	}

	return state, nil
}

func (s *DeviceState) Prepare(claim *resourceapi.ResourceClaim) ([]*drapbv1.Device, error) {
	s.Lock()
	defer s.Unlock()

	claimUID := string(claim.UID)

	checkpoint := newCheckpoint()
	if err := s.checkpointManager.GetCheckpoint(DriverPluginCheckpointFile, checkpoint); err != nil {
		return nil, fmt.Errorf("unable to sync from checkpoint: %v", err)
	}
	preparedClaims := checkpoint.V1.PreparedClaims

	if preparedClaims[claimUID] != nil {
		return preparedClaims[claimUID].GetDevices(), nil
	}

	// If synthetic-partition mode is enabled, set up partitions in two phases so
	// that memory-mode conflict taints are published to the API server BEFORE the
	// slow hardware reconfiguration (driver reload) runs. This lets the scheduler
	// and taint-eviction controller react during the reload window instead of
	// after it. On any failure we roll back the in-memory reservation (and taints)
	// and fail the Prepare; the kubelet re-drives it and ApplyPartition reconciles
	// against sysfs idempotently.
	// Populated inside the synthetic-partition branch below and consulted by every
	// error path from here to the end of Prepare, so that a failure after a
	// successful reservation always rolls the reservation back rather than
	// leaking it (see partitionShares' use after prepareDevices/CreateClaimSpecFile/
	// CreateCheckpoint below).
	var partitionShares []PartitionShare
	if s.syntheticPartition && s.partitionState != nil && claim.Status.Allocation != nil {
		// The partition shares for this claim are derivable from the allocation
		// results on every call, so we rebuild the list each time Prepare is
		// re-driven (kubelet retries it while an async memory reload converges).
		// One entry per result, not per device: a claim may hold several shares of
		// the same device, each of which needs its own partition slot.
		partitionShares = partitionSharesForClaim(claim, s.allocatable, s.nodeName)

		// Phase 1: reserve modes and stamp taints (fast, in-memory only). This is
		// idempotent per claim: on a retry the claim is already reserved, so counts
		// are not re-incremented and taintsChanged is false.
		taintsChanged, err := s.partitionState.ReserveClaim(claimUID, partitionShares)
		if err != nil {
			return nil, fmt.Errorf("error reserving partition for claim %s: %v", claimUID, err)
		}

		// Phase 1.5: publish taints before the slow apply. If this fails we have no
		// scheduler-visible protection for the reload window, so roll back and fail.
		if taintsChanged && s.driver != nil {
			if err := s.driver.republishResources(context.TODO()); err != nil {
				s.rollbackPartitions(claimUID, partitionShares)
				return nil, fmt.Errorf("failed to publish partition taints before apply: %v", err)
			}
		}

		// Phase 2: apply the hardware partition (slow: compute set + memory reload).
		// Applying is per physical device, so shares of the same device collapse to
		// a single apply; ApplyPartition is idempotent against sysfs regardless.
		for _, deviceName := range uniqueShareDevices(partitionShares) {
			if err := s.partitionState.ApplyPartition(deviceName); err != nil {
				// On a KMM node the reload is asynchronous: ApplyPartition reports
				// errReloadInProgress until sysfs converges. This is NOT a failure —
				// keep the reservation and taints, persist the in-flight reload marker
				// so a restart resumes polling, and return a retryable error so the
				// kubelet re-drives Prepare (which re-enters ApplyPartition to poll).
				if IsReloadInProgress(err) {
					s.savePartitionCheckpoint(checkpoint)
					if cperr := s.checkpointManager.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint); cperr != nil {
						klog.Warningf("failed to checkpoint in-flight KMM reload marker: %v", cperr)
					}
					return nil, fmt.Errorf("partition apply for device %s pending: %w", deviceName, err)
				}
				s.rollbackPartitions(claimUID, partitionShares)
				return nil, fmt.Errorf("error applying partition for device %s: %v", deviceName, err)
			}
		}
	}

	preparedDevices, err := s.prepareDevices(claim)
	if err != nil {
		if s.syntheticPartition && s.partitionState != nil {
			s.rollbackPartitions(claimUID, partitionShares)
		}
		return nil, fmt.Errorf("prepare failed: %v", err)
	}

	if err = s.cdi.CreateClaimSpecFile(claimUID, preparedDevices); err != nil {
		if s.syntheticPartition && s.partitionState != nil {
			s.rollbackPartitions(claimUID, partitionShares)
		}
		return nil, fmt.Errorf("unable to create CDI spec file for claim: %v", err)
	}

	preparedClaims[claimUID] = preparedDevices

	// Save partition state to checkpoint if synthetic-partition mode is enabled
	s.savePartitionCheckpoint(checkpoint)

	if err := s.checkpointManager.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint); err != nil {
		// The CDI spec file was already written for this claim; without deleting it
		// here, a claim we're about to roll back (and which kubelet will not learn
		// succeeded) would leave an orphaned spec no Unprepare call is coming to clean up.
		if delErr := s.cdi.DeleteClaimSpecFile(claimUID); delErr != nil {
			klog.Warningf("failed to delete orphaned CDI spec file for claim %s after checkpoint failure: %v", claimUID, delErr)
		}
		if s.syntheticPartition && s.partitionState != nil {
			s.rollbackPartitions(claimUID, partitionShares)
		}
		return nil, fmt.Errorf("unable to sync to checkpoint: %v", err)
	}

	// Note: memory-mode conflict taints were already published in Phase 1.5 above,
	// before the hardware apply, so no re-publish is needed here.

	return preparedClaims[claimUID].GetDevices(), nil
}

// partitionSharesForClaim returns one PartitionShare per allocation result that
// refers to a synthetic-partition device, in allocation order.
//
// There is one entry per result rather than per device name: DRA allows a claim
// to hold several concurrent shares of the same device (AllowMultipleAllocations),
// distinguished only by ShareID, and each share needs its own partition slot.
func partitionSharesForClaim(claim *resourceapi.ResourceClaim, allocatable AllocatableDevices, nodeName string) []PartitionShare {
	if claim.Status.Allocation == nil {
		return nil
	}
	claimUID := string(claim.UID)
	var shares []PartitionShare
	for _, result := range claim.Status.Allocation.Devices.Results {
		// A DRA device is identified by (driver, pool, device), not device name
		// alone. Without this check, a same-named device published by another
		// driver instance or another pool (e.g. a different gpu.amd.com pool on
		// a different node reusing the checkpoint, or another driver entirely)
		// would be treated as a local partition device and reserved/repartitioned
		// against this node's hardware.
		if result.Driver != consts.DriverName || result.Pool != nodeName {
			continue
		}
		device, exists := allocatable[result.Device]
		if !exists || device.SyntheticPartition == nil {
			continue
		}
		shares = append(shares, PartitionShare{
			DeviceName: result.Device,
			ShareKey:   allocationResultKey(claimUID, &result),
		})
	}
	return shares
}

// allocationResultKey returns the share key for one allocation result. Both the
// reservation path (partitionSharesForClaim) and the device-node resolution path
// (applyConfig) go through here, so the slot reserved for a share is the one
// looked up for it.
func allocationResultKey(claimUID string, result *resourceapi.DeviceRequestAllocationResult) string {
	var shareID *string
	if result.ShareID != nil {
		s := string(*result.ShareID)
		shareID = &s
	}
	return ShareKey(claimUID, result.Request, result.Device, shareID)
}

// uniqueShareDevices returns the distinct device names across the given shares,
// preserving first-seen order.
func uniqueShareDevices(shares []PartitionShare) []string {
	seen := make(map[string]bool, len(shares))
	var names []string
	for _, share := range shares {
		if seen[share.DeviceName] {
			continue
		}
		seen[share.DeviceName] = true
		names = append(names, share.DeviceName)
	}
	return names
}

// savePartitionCheckpoint writes the current partition state (active memory mode,
// per-GPU compute modes, any in-flight KMM reload marker, and the per-share slot
// assignments) into the checkpoint. No-op when synthetic-partition mode is disabled.
func (s *DeviceState) savePartitionCheckpoint(checkpoint *Checkpoint) {
	if !s.syntheticPartition || s.partitionState == nil {
		return
	}
	checkpoint.V1.ActiveMemoryMode = s.partitionState.GetActiveMemoryMode()
	checkpoint.V1.GPUComputeModes = s.partitionState.GetGPUComputeModes()
	checkpoint.V1.MemoryReload = s.partitionState.GetMemoryReloadMarker()
	checkpoint.V1.AssignedSlots = s.partitionState.GetAssignedSlots()
}

// rollbackPartitions releases the reservation for the given claim (in-memory
// only) and, if that cleared the node memory mode, re-publishes resources to
// drop the now-stale taints. Used to undo a two-phase Prepare when a later phase
// fails. It is idempotent per claim (see ReleaseClaim). The hardware partition
// state is intentionally not reverted; see ReleasePartition.
func (s *DeviceState) rollbackPartitions(claimUID string, shares []PartitionShare) {
	if s.partitionState == nil {
		return
	}
	taintsChanged, err := s.partitionState.ReleaseClaim(claimUID, shares)
	if err != nil {
		klog.Warningf("Error rolling back partition reservation for claim %s: %v", claimUID, err)
	}
	if taintsChanged && s.driver != nil {
		if err := s.driver.republishResources(context.TODO()); err != nil {
			klog.Warningf("Failed to re-publish resources after partition rollback: %v", err)
		}
	}
}

func (s *DeviceState) Unprepare(claimUID string) error {
	s.Lock()
	defer s.Unlock()

	checkpoint := newCheckpoint()
	if err := s.checkpointManager.GetCheckpoint(DriverPluginCheckpointFile, checkpoint); err != nil {
		return fmt.Errorf("unable to sync from checkpoint: %v", err)
	}
	preparedClaims := checkpoint.V1.PreparedClaims

	if preparedClaims[claimUID] == nil {
		// A claim can hold a live partition reservation without ever having been
		// recorded as prepared: the errReloadInProgress path in Prepare persists
		// AssignedSlots and returns before preparedClaims[claimUID] is set. Without
		// this check, kubelet's Unprepare call for such a claim (e.g. the pod was
		// deleted while the reload was still converging) would return here and
		// strand the reservation, taints, and mutex counters permanently.
		if !s.syntheticPartition || s.partitionState == nil || !s.partitionState.HasReservation(claimUID) {
			return nil
		}
		shares := s.partitionState.PartitionSharesFromAssignedSlots(claimUID)
		changed, err := s.partitionState.ReleaseClaim(claimUID, shares)
		if err != nil {
			klog.Warningf("Error releasing partition for never-fully-prepared claim %s: %v", claimUID, err)
		}
		if changed && s.driver != nil {
			if err := s.driver.republishResources(context.TODO()); err != nil {
				klog.Warningf("Failed to re-publish resources after unprepare of never-fully-prepared claim %s: %v", claimUID, err)
			}
		}
		s.savePartitionCheckpoint(checkpoint)
		if err := s.checkpointManager.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint); err != nil {
			return fmt.Errorf("unable to sync to checkpoint: %v", err)
		}
		return nil
	}

	// Track whether taints changed for re-publishing
	taintsChanged := false

	// If synthetic-partition mode, handle partition cleanup via the idempotent
	// claim-scoped release so the per-GPU/total allocation counts balance the
	// reservation exactly once, regardless of retries.
	if s.syntheticPartition && s.partitionState != nil {
		// Release the exact shares that were reserved, using the share keys stored
		// at Prepare time. Rebuilding them from device names would collapse several
		// shares of one device into a single release and strand their slots.
		var partitionShares []PartitionShare
		for i, device := range preparedClaims[claimUID] {
			allocDevice, exists := s.allocatable[device.DeviceName]
			if !exists || allocDevice.SyntheticPartition == nil {
				continue
			}
			shareKey := device.ShareKey
			if shareKey == "" {
				// Checkpoint written before share keys were persisted: fall back to
				// the positional key used by RecoverFromCheckpoint for such entries.
				shareKey = ShareKey(claimUID, strconv.Itoa(i), device.DeviceName, nil)
			}
			partitionShares = append(partitionShares, PartitionShare{
				DeviceName: device.DeviceName,
				ShareKey:   shareKey,
			})
		}
		changed, err := s.partitionState.ReleaseClaim(claimUID, partitionShares)
		if err != nil {
			klog.Warningf("Error releasing partition for claim %s: %v", claimUID, err)
		}
		taintsChanged = changed
	}

	if err := s.unprepareDevices(claimUID, preparedClaims[claimUID]); err != nil {
		return fmt.Errorf("unprepare failed: %v", err)
	}

	err := s.cdi.DeleteClaimSpecFile(claimUID)
	if err != nil {
		return fmt.Errorf("unable to delete CDI spec file for claim: %v", err)
	}

	delete(preparedClaims, claimUID)

	// Save partition state to checkpoint if synthetic-partition mode is enabled
	s.savePartitionCheckpoint(checkpoint)

	if err := s.checkpointManager.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint); err != nil {
		return fmt.Errorf("unable to sync to checkpoint: %v", err)
	}

	// Re-publish resources if taints changed (all allocations released)
	if taintsChanged && s.driver != nil {
		if err := s.driver.republishResources(context.TODO()); err != nil {
			klog.Warningf("Failed to re-publish resources after partition unprepare: %v", err)
		}
	}

	return nil
}

func (s *DeviceState) prepareDevices(claim *resourceapi.ResourceClaim) (PreparedDevices, error) {
	if claim.Status.Allocation == nil {
		return nil, fmt.Errorf("claim not yet allocated")
	}

	// Retrieve the full set of device configs for the driver.
	configs, err := GetOpaqueDeviceConfigs(
		configapi.Decoder,
		consts.DriverName,
		claim.Status.Allocation.Devices.Config,
	)
	if err != nil {
		return nil, fmt.Errorf("error getting opaque device configs: %v", err)
	}
	klog.V(2).Infof("Decoded %d opaque configs for driver %s", len(configs), consts.DriverName)

	// Add a default GPU config at the front with lowest precedence. No
	// default VfioDeviceConfig — VFIO conversion requires an explicit config
	// in the claim to avoid accidentally routing regular GPUs into vfio-pci.
	configs = slices.Insert(configs, 0,
		&OpaqueDeviceConfig{Requests: []string{}, Config: configapi.DefaultGpuConfig()},
	)

	// Track per-claim VFIO conversions so the long-lived allocatable entry
	// stays immutable. Restored on unprepare or rollback.
	s.claimVfioConversions = make(map[string]*AmdGpuInfo)

	// Look through the configs and figure out which one will be applied to
	// each device allocation result based on their order of precedence.
	configResultsMap := make(map[runtime.Object][]*resourceapi.DeviceRequestAllocationResult)
	for _, result := range claim.Status.Allocation.Devices.Results {
		if result.Driver != consts.DriverName {
			continue
		}
		allocDev, exists := s.allocatable[result.Device]
		if !exists {
			return nil, fmt.Errorf("requested GPU is not allocatable: %v", result.Device)
		}
		isVFIO := allocDev.Type() == consts.VfioDeviceType
		for _, c := range slices.Backward(configs) {
			switch c.Config.(type) {
			case *configapi.VfioDeviceConfig:
				if !featuregates.Enabled(featuregates.VFIOPassthrough) {
					continue
				}
				if !isVFIO {
					if allocDev.AmdGpu != nil {
						physfn := filepath.Join(amdgpu.PCIDevicePath, allocDev.AmdGpu.PCIAddress, "physfn")
						_, physfnErr := os.Lstat(physfn)
						isVF := physfnErr == nil

						klog.Infof("Converting GPU %s to VFIO device (isVF=%v, VfioDeviceConfig present)", allocDev.AmdGpu.PCIAddress, isVF)
						vfioInfo := &AmdGpuVFIOInfo{
							PCIAddress:         allocDev.AmdGpu.PCIAddress,
							DeviceID:           allocDev.AmdGpu.DeviceID,
							VendorID:           consts.AMDVendorID,
							ProductName:        allocDev.AmdGpu.ProductName,
							NumaNode:           allocDev.AmdGpu.NumaNode,
							IsVF:               isVF,
							pciBusIDAttr:       allocDev.AmdGpu.pciBusIDAttr,
							pcieRootAttr:       allocDev.AmdGpu.pcieRootAttr,
							preConfigureDriver: "amdgpu",
						}
						iommuGroup, _ := amdgpu.GetIOMMUGroup(allocDev.AmdGpu.PCIAddress)
						vfioInfo.IOMMUGroup = iommuGroup
						s.claimVfioConversions[result.Device] = allocDev.AmdGpu
						allocDev.Vfio = vfioInfo
						allocDev.AmdGpu = nil
						isVFIO = true
					}
					if !isVFIO {
						continue
					}
				}
			case *configapi.GpuConfig:
				if isVFIO {
					continue
				}
			}
			if len(c.Requests) == 0 || slices.Contains(c.Requests, result.Request) {
				configResultsMap[c.Config] = append(configResultsMap[c.Config], &result)
				break
			}
		}
	}

	// Normalize, validate, and apply all configs associated with devices that
	// need to be prepared. Track container edits generated from applying the
	// config to the set of device allocation results.
	perDeviceCDIContainerEdits := make(PerDeviceCDIContainerEdits)
	for c, results := range configResultsMap {
		switch castConfig := c.(type) {
		case *configapi.GpuConfig:
			if err := castConfig.Normalize(); err != nil {
				return nil, fmt.Errorf("error normalizing GPU config: %w", err)
			}
			if err := castConfig.Validate(); err != nil {
				return nil, fmt.Errorf("error validating GPU config: %w", err)
			}
			containerEdits, err := s.applyConfig(string(claim.UID), castConfig, results)
			if err != nil {
				return nil, fmt.Errorf("error applying GPU config: %w", err)
			}
			for k, v := range containerEdits {
				perDeviceCDIContainerEdits[k] = v
			}
		case *configapi.VfioDeviceConfig:
			if err := castConfig.Normalize(); err != nil {
				return nil, fmt.Errorf("error normalizing VFIO config: %w", err)
			}
			if err := castConfig.Validate(); err != nil {
				return nil, fmt.Errorf("error validating VFIO config: %w", err)
			}
			var configuredNames []string
			for _, result := range results {
				edits, err := s.applyVFIOConfig(result, castConfig)
				if err != nil {
					if dev := s.allocatable[result.Device]; dev != nil && dev.Vfio != nil {
						configuredNames = append(configuredNames, result.Device)
					}
					for _, name := range configuredNames {
						if dev := s.allocatable[name]; dev != nil && dev.Vfio != nil {
							if unconfigErr := s.vfioManager.Unconfigure(dev.Vfio); unconfigErr != nil {
								klog.Warningf("Rollback: failed to unconfigure %s: %v", dev.Vfio.PCIAddress, unconfigErr)
							}
						}
						s.restoreFromVfio(name)
					}
					return nil, fmt.Errorf("error applying VFIO config for %s: %w", result.Device, err)
				}
				configuredNames = append(configuredNames, result.Device)
				perDeviceCDIContainerEdits[allocationResultKey(string(claim.UID), result)] = edits
			}
		default:
			return nil, fmt.Errorf("runtime object is not a recognized configuration")
		}
	}

	// Walk through each config and its associated device allocation results
	// and construct the list of prepared devices to return.
	claimUID := string(claim.UID)
	var preparedDevices PreparedDevices
	for _, results := range configResultsMap {
		for _, result := range results {
			key := allocationResultKey(claimUID, result)
			device := &PreparedDevice{
				Device: drapbv1.Device{
					RequestNames: []string{result.Request},
					PoolName:     result.Pool,
					DeviceName:   result.Device,
				},
				ContainerEdits: perDeviceCDIContainerEdits[key],
				ShareKey:       key,
			}

			// Record the partition slot so the CDI device name is unique per share
			// and can be rebuilt identically on unprepare.
			if allocDev := s.allocatable[result.Device]; allocDev != nil && allocDev.SyntheticPartition != nil && s.partitionState != nil {
				gpuIndex := allocDev.SyntheticPartition.GPUIndex
				if slot, ok := s.partitionState.GetAssignedSlot(gpuIndex, key); ok {
					device.PartitionSlot = &slot
				}
			}

			// Built after PartitionSlot is set: the CDI device name embeds the slot.
			device.CdiDeviceIds = s.cdi.GetClaimDevices(claimUID, []*PreparedDevice{device})
			preparedDevices = append(preparedDevices, device)
		}
	}

	return preparedDevices, nil
}

func (s *DeviceState) unprepareDevices(claimUID string, devices PreparedDevices) error {
	var errs []error
	for _, device := range devices {
		allocDev, exists := s.allocatable[device.DeviceName]
		if !exists {
			continue
		}
		if allocDev.Type() == consts.VfioDeviceType && allocDev.Vfio != nil && s.vfioManager != nil {
			if err := s.vfioManager.Unconfigure(allocDev.Vfio); err != nil {
				errs = append(errs, fmt.Errorf("failed to unconfigure VFIO device %s: %w", device.DeviceName, err))
			} else {
				s.restoreFromVfio(device.DeviceName)
			}
		}
	}
	return errors.Join(errs...)
}

func (s *DeviceState) restoreFromVfio(deviceName string) {
	if s.claimVfioConversions == nil {
		return
	}
	original, ok := s.claimVfioConversions[deviceName]
	if !ok {
		return
	}
	if allocDev, exists := s.allocatable[deviceName]; exists {
		allocDev.AmdGpu = original
		allocDev.Vfio = nil
		klog.Infof("Restored %s from VFIO back to AmdGpu type", deviceName)
	}
	delete(s.claimVfioConversions, deviceName)
}

// getDeviceAttrs gets the major, minor, type, and permissions for a given device path.
func getDeviceAttrs(path string) (major, minor int64, devType, permissions string, err error) {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return 0, 0, "", "", fmt.Errorf("failed to stat device %s: %w", path, err)
	}

	stat, ok := fileInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, "", "", fmt.Errorf("failed to get syscall.Stat_t for %s", path)
	}

	major = int64(unix.Major(uint64(stat.Rdev)))
	minor = int64(unix.Minor(uint64(stat.Rdev)))

	if (fileInfo.Mode() & os.ModeCharDevice) != 0 {
		devType = "c"
	} else if (fileInfo.Mode() & os.ModeDevice) != 0 {
		devType = "b"
	} else {
		return 0, 0, "", "", fmt.Errorf("unsupported file type for device %s: %v", path, fileInfo.Mode())
	}

	permissions = "rwm"

	return major, minor, devType, permissions, nil
}

// applyConfig applies a configuration to a set of device allocation results.
//
// Results are keyed by share key rather than device name: a claim may hold
// several shares of one synthetic-partition device, and each resolves to a
// different physical partition, so keying by name would collapse them onto one
// set of container edits.
func (s *DeviceState) applyConfig(claimUID string, config *configapi.GpuConfig, results []*resourceapi.DeviceRequestAllocationResult) (PerDeviceCDIContainerEdits, error) {
	perDeviceEdits := make(PerDeviceCDIContainerEdits)

	for _, result := range results {
		klog.Infof("received allocation result: %+v", result)

		var card, renderD int
		var err error

		device, exists := s.allocatable[result.Device]
		if !exists {
			return nil, fmt.Errorf("requested device is not allocatable: %v", result.Device)
		}

		key := allocationResultKey(claimUID, result)

		if device.SyntheticPartition != nil {
			// Synthetic-partition device: discover card/renderD by re-reading sysfs
			// after GPU has been partitioned by ApplyPartition
			card, renderD, err = s.discoverPartitionDeviceNodes(result.Device, key)
			if err != nil {
				return nil, fmt.Errorf("error discovering device nodes for synthetic-partition device %s: %w", result.Device, err)
			}
		} else {
			card, renderD, err = parseDeviceName(result.Device)
			if err != nil {
				return nil, fmt.Errorf("error parsing device name %s: %w", result.Device, err)
			}
		}

		// TODO implement GPU sharing config when it is available
		//switch {
		//case config.Sharing.IsTimeSlicing():
		//	// TODO implement time slicing config when it is available
		//case config.Sharing.IsSpacePartitioning():
		//	// TODO implement space partitioning config when it is available
		//}

		edits, err := s.buildDeviceCDIEdits(card, renderD)
		if err != nil {
			return nil, fmt.Errorf("error building CDI edits for device %s (card=%d, renderD=%d): %w",
				result.Device, card, renderD, err)
		}

		perDeviceEdits[key] = &cdiapi.ContainerEdits{ContainerEdits: edits}
	}

	return perDeviceEdits, nil
}

// buildDeviceCDIEdits creates CDI container edits for a specific card/renderD pair.
func (s *DeviceState) buildDeviceCDIEdits(card, renderD int) (*cdispec.ContainerEdits, error) {
	cardPath := fmt.Sprintf("/dev/dri/card%d", card)
	renderDPath := fmt.Sprintf("/dev/dri/renderD%d", renderD)
	kfdPath := "/dev/kfd"

	cardMajor, cardMinor, cardDevType, cardPermission, err := getDeviceAttrs(cardPath)
	if err != nil {
		return nil, fmt.Errorf("error getting device attrs for %s: %w", cardPath, err)
	}
	renderDMajor, renderDMinor, renderDDevType, renderDPermission, err := getDeviceAttrs(renderDPath)
	if err != nil {
		return nil, fmt.Errorf("error getting device attrs for %s: %w", renderDPath, err)
	}
	kfdMajor, kfdMinor, kfdDevType, kfdPermission, err := getDeviceAttrs(kfdPath)
	if err != nil {
		return nil, fmt.Errorf("error getting device attrs for %s: %w", kfdPath, err)
	}

	return &cdispec.ContainerEdits{
		DeviceNodes: []*cdispec.DeviceNode{
			{
				Path:        "/dev/kfd",
				HostPath:    "/dev/kfd",
				Type:        kfdDevType,
				Major:       kfdMajor,
				Minor:       kfdMinor,
				Permissions: kfdPermission,
			},
			{
				Path:        fmt.Sprintf("/dev/dri/card%d", card),
				HostPath:    fmt.Sprintf("/dev/dri/card%d", card),
				Type:        cardDevType,
				Major:       cardMajor,
				Minor:       cardMinor,
				Permissions: cardPermission,
			},
			{
				Path:        fmt.Sprintf("/dev/dri/renderD%d", renderD),
				HostPath:    fmt.Sprintf("/dev/dri/renderD%d", renderD),
				Type:        renderDDevType,
				Major:       renderDMajor,
				Minor:       renderDMinor,
				Permissions: renderDPermission,
			},
		},
	}, nil
}

// discoverPartitionDeviceNodes re-reads sysfs to find the card/renderD indices
// for a synthetic-partition device after GPU partitioning has been applied.
// For SPX (full GPU), it uses the parent GPU's card/renderD.
// For partitioned modes, it reads XCP platform devices from sysfs and selects the
// one matching the partition slot reserved for shareKey.
func (s *DeviceState) discoverPartitionDeviceNodes(deviceName, shareKey string) (card, renderD int, err error) {
	gpuIndex, computeMode, _, err := parseSyntheticPartitionDeviceName(deviceName)
	if err != nil {
		return 0, 0, err
	}

	// Re-enumerate GPUs to find current device nodes after partitioning
	allGPUs := amdgpu.GetAMDGPUs()

	// Look up the PCI address for this GPU index
	var gpuPCIAddr string
	if s.partitionState != nil {
		var ok bool
		gpuPCIAddr, ok = s.partitionState.gpuPCIAddresses[gpuIndex]
		if !ok {
			return 0, 0, fmt.Errorf("PCI address for GPU index %d not found for partition device discovery", gpuIndex)
		}
	} else {
		return 0, 0, fmt.Errorf("partition state not available for partition device discovery")
	}

	if computeMode == consts.ComputePartitionSPX {
		// SPX mode - use the physical GPU's card/renderD directly
		for _, gpuInfoMap := range allGPUs {
			pciAddr := gpuInfoMap["pciAddr"].(string)
			cpt := gpuInfoMap["computePartitionType"].(string)
			if pciAddr == gpuPCIAddr && (cpt == consts.ComputePartitionSPX || cpt == "") {
				card = gpuInfoMap["card"].(int)
				renderD = gpuInfoMap["renderD"].(int)
				klog.Infof("Auto-partition SPX device %s mapped to card=%d renderD=%d", deviceName, card, renderD)
				return card, renderD, nil
			}
		}
		return 0, 0, fmt.Errorf("could not find SPX device nodes for GPU %d (PCI: %s)", gpuIndex, gpuPCIAddr)
	}

	// Collect partitions belonging to this GPU
	type partitionDev struct {
		card    int
		renderD int
	}
	var partitions []partitionDev

	for _, gpuInfoMap := range allGPUs {
		pciAddr := gpuInfoMap["pciAddr"].(string)
		cpt := gpuInfoMap["computePartitionType"].(string)
		if pciAddr == gpuPCIAddr && cpt != "" && cpt != consts.ComputePartitionSPX {
			partitions = append(partitions, partitionDev{
				card:    gpuInfoMap["card"].(int),
				renderD: gpuInfoMap["renderD"].(int),
			})
		}
	}

	if len(partitions) == 0 {
		return 0, 0, fmt.Errorf("no partition devices found for GPU %d (PCI: %s) after partitioning",
			gpuIndex, gpuPCIAddr)
	}

	// Sort partitions by card index for deterministic assignment
	sort.Slice(partitions, func(i, j int) bool {
		return partitions[i].card < partitions[j].card
	})

	// Use the slot assigned to this share at reservation time. It is looked up,
	// not derived from the allocation count: several shares of one device see the
	// same count, and releases need not be LIFO, so a count cannot say which
	// partition this particular share owns.
	slot, ok := s.partitionState.GetAssignedSlot(gpuIndex, shareKey)
	if !ok {
		return 0, 0, fmt.Errorf("no partition slot reserved for share %q on GPU %d", shareKey, gpuIndex)
	}

	if slot >= len(partitions) {
		return 0, 0, fmt.Errorf("partition slot %d exceeds available partitions %d for GPU %d",
			slot, len(partitions), gpuIndex)
	}

	card = partitions[slot].card
	renderD = partitions[slot].renderD
	klog.Infof("Auto-partition device %s (share %s) mapped to partition slot %d: card=%d renderD=%d",
		deviceName, shareKey, slot, card, renderD)

	return card, renderD, nil
}

// GetOpaqueDeviceConfigs returns an ordered list of the configs contained in possibleConfigs for this driver.
//
// Configs can either come from the resource claim itself or from the device
// class associated with the request. Configs coming directly from the resource
// claim take precedence over configs coming from the device class. Moreover,
// configs found later in the list of configs attached to its source take
// precedence over configs found earlier in the list for that source.
//
// All of the configs relevant to the driver from the list of possibleConfigs
// will be returned in order of precedence (from lowest to highest). If no
// configs are found, nil is returned.
func GetOpaqueDeviceConfigs(
	decoder runtime.Decoder,
	driverName string,
	possibleConfigs []resourceapi.DeviceAllocationConfiguration,
) ([]*OpaqueDeviceConfig, error) {
	// Collect all configs in order of reverse precedence.
	var classConfigs []resourceapi.DeviceAllocationConfiguration
	var claimConfigs []resourceapi.DeviceAllocationConfiguration
	var candidateConfigs []resourceapi.DeviceAllocationConfiguration
	for _, config := range possibleConfigs {
		switch config.Source {
		case resourceapi.AllocationConfigSourceClass:
			classConfigs = append(classConfigs, config)
		case resourceapi.AllocationConfigSourceClaim:
			claimConfigs = append(claimConfigs, config)
		default:
			return nil, fmt.Errorf("invalid config source: %v", config.Source)
		}
	}
	candidateConfigs = append(candidateConfigs, classConfigs...)
	candidateConfigs = append(candidateConfigs, claimConfigs...)

	// Decode all configs that are relevant for the driver.
	var resultConfigs []*OpaqueDeviceConfig
	for _, config := range candidateConfigs {
		// If this is nil, the driver doesn't support some future API extension
		// and needs to be updated.
		if config.DeviceConfiguration.Opaque == nil {
			return nil, fmt.Errorf("only opaque parameters are supported by this driver")
		}

		// Configs for different drivers may have been specified because a
		// single request can be satisfied by different drivers. This is not
		// an error -- drivers must skip over other driver's configs in order
		// to support this.
		if config.DeviceConfiguration.Opaque.Driver != driverName {
			continue
		}

		decodedConfig, err := runtime.Decode(decoder, config.DeviceConfiguration.Opaque.Parameters.Raw)
		if err != nil {
			return nil, fmt.Errorf("error decoding config parameters: %w", err)
		}

		resultConfig := &OpaqueDeviceConfig{
			Requests: config.Requests,
			Config:   decodedConfig,
		}

		resultConfigs = append(resultConfigs, resultConfig)
	}

	return resultConfigs, nil
}

// applyVFIOConfig configures a VFIO passthrough device and returns CDI edits.
func (s *DeviceState) applyVFIOConfig(result *resourceapi.DeviceRequestAllocationResult, config *configapi.VfioDeviceConfig) (*cdiapi.ContainerEdits, error) {
	device, exists := s.allocatable[result.Device]
	if !exists || device.Vfio == nil {
		return nil, fmt.Errorf("device %s is not a VFIO device", result.Device)
	}

	if s.vfioManager == nil {
		return nil, fmt.Errorf("VFIO manager not available for device %s", result.Device)
	}
	if err := s.vfioManager.Configure(device.Vfio); err != nil {
		return nil, fmt.Errorf("error configuring VFIO device %s: %w", result.Device, err)
	}

	preferIommuFD := config.Iommu != nil && config.Iommu.ShouldPreferIommuFD()

	useIommuFD := UseIommuFD(device.Vfio, preferIommuFD, s.vfioManager.iommuFDEnabled)

	deviceEdits, err := GetVfioDeviceCDIEdits(device.Vfio, useIommuFD)
	if err != nil {
		return nil, fmt.Errorf("error building CDI edits for %s: %w", result.Device, err)
	}
	commonEdits := GetVfioCommonCDIEdits(useIommuFD)
	deviceEdits.ContainerEdits.DeviceNodes = append(deviceEdits.ContainerEdits.DeviceNodes, commonEdits.ContainerEdits.DeviceNodes...)

	backend := "legacy"
	if useIommuFD {
		backend = "iommufd"
	}
	klog.Infof("Applied VFIO config for %s: iommuGroup=%s, backend=%s", device.Vfio.PCIAddress, device.Vfio.IOMMUGroup, backend)
	return deviceEdits, nil
}
