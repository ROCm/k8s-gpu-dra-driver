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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"

	configapi "github.com/ROCm/k8s-gpu-dra-driver/api/amd.com/resource/gpu/v1alpha1"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/amdgpu"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/consts"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/featuregates"
	"golang.org/x/sys/unix"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/runtime"
	klog "k8s.io/klog/v2"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/checksum"

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
	pluginPath           string
	bootID               string
}

// bootEpochFile records the boot the checkpoint's claims were prepared under. It
// lives next to the checkpoint but outside it, so it does not change the checkpoint
// checksum and an older binary that does not know about it stays rollback-safe.
const bootEpochFile = "boot-epoch"

// readBootID returns the current boot identifier from procfs. It changes on every node
// reboot. An unreadable value is returned empty, which cannot confirm a reboot, so the
// checkpoint is kept and verified against the host rather than discarded.
func readBootID() string {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		klog.Warningf("unable to read boot id (%v); a reboot cannot be confirmed, so the checkpoint will be verified instead of discarded", err)
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (s *DeviceState) epochPath() string {
	return filepath.Join(s.pluginPath, bootEpochFile)
}

// epochRecord ties a boot id to the checkpoint it was written for. The checksum is what
// separates "we rebooted and nothing has written the checkpoint since" from "some other
// binary wrote it after this was recorded", and only the first is safe to discard.
type epochRecord struct {
	BootID     string            `json:"bootID"`
	Checkpoint checksum.Checksum `json:"checkpoint"`
}

// readStoredEpoch returns the recorded epoch and whether it can be trusted. Absent,
// unreadable, and unparsable are all untrusted, which keeps the checkpoint rather than
// discarding it. The pre-record format was a bare boot id, so it also reads as untrusted.
func (s *DeviceState) readStoredEpoch() (epochRecord, bool) {
	data, err := os.ReadFile(s.epochPath())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			klog.Warningf("unable to read the boot epoch (%v); treating it as unknown", err)
		}
		return epochRecord{}, false
	}
	var rec epochRecord
	if err := json.Unmarshal(data, &rec); err != nil || rec.BootID == "" {
		klog.Warningf("boot epoch at %s is not a usable record; treating it as unknown", s.epochPath())
		return epochRecord{}, false
	}
	return rec, true
}

// writeEpoch records the boot and checkpoint the plugin is running against. It replaces
// the file atomically, since a half-written record would read as a different boot and
// authorize discarding claims.
func (s *DeviceState) writeEpoch(ck checksum.Checksum) error {
	if s.bootID == "" {
		// Recording an empty boot id would lose the only value a later reboot can be
		// detected against, so keep whatever is already stored.
		klog.Warning("boot id unavailable; leaving the recorded boot epoch unchanged")
		return nil
	}
	data, err := json.Marshal(epochRecord{BootID: s.bootID, Checkpoint: ck})
	if err != nil {
		return fmt.Errorf("unable to encode boot epoch: %w", err)
	}
	tmp := s.epochPath() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("unable to record boot epoch: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("unable to record boot epoch: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("unable to record boot epoch: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("unable to record boot epoch: %w", err)
	}
	return os.Rename(tmp, s.epochPath())
}

// saveCheckpoint writes the checkpoint and the epoch describing it together, so the two
// never drift apart. CreateCheckpoint fills in the checksum recorded here.
func (s *DeviceState) saveCheckpoint(cp *Checkpoint) error {
	if err := s.checkpointManager.CreateCheckpoint(DriverPluginCheckpointFile, cp); err != nil {
		return err
	}
	return s.writeEpoch(cp.Checksum)
}

// discardCheckpoint removes any CDI spec files for the given claims and then replaces
// the on-disk checkpoint with an empty one. Used when the recorded boot epoch no longer
// matches: the checkpointed device nodes may now point at different hardware, so the
// specs must not survive on a persistent spec directory where the container runtime
// could still resolve a claim against them. RemoveSpec tolerates an already-absent
// file, which is the common tmpfs case.
func (s *DeviceState) discardCheckpoint(claims PreparedClaims) error {
	for claimUID := range claims {
		if err := s.cdi.DeleteClaimSpecFile(claimUID); err != nil {
			return fmt.Errorf("unable to remove stale CDI spec for claim %s: %w", claimUID, err)
		}
	}
	if err := s.saveCheckpoint(newCheckpoint()); err != nil {
		return fmt.Errorf("unable to discard stale checkpoint: %w", err)
	}
	return nil
}

func NewDeviceState(config *Config) (*DeviceState, error) {
	allocatable, err := enumerateAllPossibleDevices()
	if err != nil {
		return nil, fmt.Errorf("error enumerating all possible devices: %v", err)
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
		cdi:               cdi,
		allocatable:       allocatable,
		vfioManager:       vfioMgr,
		checkpointManager: checkpointManager,
		pluginPath:        config.DriverPluginPath(),
		bootID:            readBootID(),
	}

	checkpoints, err := state.checkpointManager.ListCheckpoints()
	if err != nil {
		return nil, fmt.Errorf("unable to list checkpoints: %v", err)
	}

	for _, c := range checkpoints {
		if c == DriverPluginCheckpointFile {
			if err := state.reconcileCDISpecs(); err != nil {
				return nil, fmt.Errorf("unable to reconcile CDI spec files from checkpoint: %v", err)
			}
			// Rebind the epoch to whatever the checkpoint is now, after reconcile has
			// read the previous one and possibly discarded.
			current := newCheckpoint()
			if err := state.checkpointManager.GetCheckpoint(DriverPluginCheckpointFile, current); err != nil {
				return nil, fmt.Errorf("unable to sync from checkpoint: %v", err)
			}
			if err := state.writeEpoch(current.Checksum); err != nil {
				return nil, err
			}
			return state, nil
		}
	}

	if err := state.saveCheckpoint(newCheckpoint()); err != nil {
		return nil, fmt.Errorf("unable to sync to checkpoint: %v", err)
	}

	return state, nil
}

// validatePreparedDevices rejects a checkpoint entry that cannot be turned into a
// CDI spec, so a corrupt or partial checkpoint fails the claim loudly rather than
// panicking at startup or reporting a claim as prepared when it is not.
func validatePreparedDevices(claimUID string, preparedDevices PreparedDevices) error {
	if len(preparedDevices) == 0 {
		return fmt.Errorf("checkpoint entry for claim %s has no prepared devices", claimUID)
	}
	for _, pd := range preparedDevices {
		if pd == nil {
			return fmt.Errorf("checkpoint entry for claim %s has a nil prepared device", claimUID)
		}
		if pd.DeviceName == "" {
			return fmt.Errorf("checkpoint entry for claim %s has a device with no name", claimUID)
		}
		if pd.ContainerEdits == nil || pd.ContainerEdits.ContainerEdits == nil {
			return fmt.Errorf("checkpoint entry for claim %s device %s has no container edits", claimUID, pd.DeviceName)
		}
		// Edits that grant no device node would rebuild into a spec that resolves but
		// hands the container no GPU.
		if len(pd.ContainerEdits.ContainerEdits.DeviceNodes) == 0 {
			return fmt.Errorf("checkpoint entry for claim %s device %s grants no device nodes", claimUID, pd.DeviceName)
		}
		// A non-nil wrapper can still carry nil entries in its pointer slices; CDI
		// dereferences these while determining the spec version, so a nil entry would
		// panic before CreateClaimSpecFile could return a normal error.
		edits := pd.ContainerEdits.ContainerEdits
		for i, n := range edits.DeviceNodes {
			if n == nil {
				return fmt.Errorf("checkpoint entry for claim %s device %s has a nil deviceNodes[%d]", claimUID, pd.DeviceName, i)
			}
		}
		for i, h := range edits.Hooks {
			if h == nil {
				return fmt.Errorf("checkpoint entry for claim %s device %s has a nil hooks[%d]", claimUID, pd.DeviceName, i)
			}
		}
		for i, m := range edits.Mounts {
			if m == nil {
				return fmt.Errorf("checkpoint entry for claim %s device %s has a nil mounts[%d]", claimUID, pd.DeviceName, i)
			}
		}
		for i, nd := range edits.NetDevices {
			if nd == nil {
				return fmt.Errorf("checkpoint entry for claim %s device %s has a nil netDevices[%d]", claimUID, pd.DeviceName, i)
			}
		}
	}
	return nil
}

// validateCheckpointedClaim rejects a checkpoint entry that cannot be safely turned
// into a CDI spec: one that is structurally corrupt, or that names a device not in
// the current allocatable inventory (removed, repartitioned, or skipped by discovery
// this boot). The name match is necessary but not sufficient; proving it is the same
// physical GPU needs the stable identity tracked in #83.
func (s *DeviceState) validateCheckpointedClaim(claimUID string, preparedDevices PreparedDevices) error {
	if err := validatePreparedDevices(claimUID, preparedDevices); err != nil {
		return err
	}
	for _, pd := range preparedDevices {
		if _, ok := s.allocatable[pd.DeviceName]; !ok {
			return fmt.Errorf("checkpointed device %s for claim %s is not currently allocatable", pd.DeviceName, claimUID)
		}
	}
	return nil
}

// ensureClaimSpec makes sure the CDI spec for a checkpointed claim exists, so a
// claim reported as prepared from the checkpoint is actually usable. It is safe to
// call repeatedly: CreateClaimSpecFile overwrites the deterministic spec path.
func (s *DeviceState) ensureClaimSpec(claimUID string, preparedDevices PreparedDevices) error {
	if err := s.validateCheckpointedClaim(claimUID, preparedDevices); err != nil {
		return err
	}
	// The response always names the common device, so that spec has to exist as well.
	if err := s.cdi.CreateCommonSpecFile(); err != nil {
		return fmt.Errorf("ensure common CDI spec for claim %s: %w", claimUID, err)
	}
	if err := s.cdi.CreateClaimSpecFile(claimUID, preparedDevices); err != nil {
		return fmt.Errorf("ensure CDI spec for claim %s: %w", claimUID, err)
	}
	return nil
}

// deviceNodesCurrent reports whether every checkpointed device node still resolves to
// the major/minor it was prepared with. The boot epoch catches a reboot; this catches a
// same-boot change that moves a node's numbers without changing the boot id, for example
// /dev/kfd's dynamically allocated major after a KFD reload, or a node whose path no
// longer exists. It does NOT catch a node that keeps the same numbers but now backs a
// different physical GPU (the DRM card/render minors are stable per path), which needs
// the stable identity tracked in #83. Malformed entries (nil device, nil wrapper, nil
// node) are left to validateCheckpointedClaim; this skips them.
func (s *DeviceState) deviceNodesCurrent(claims PreparedClaims) bool {
	for _, preparedDevices := range claims {
		for _, pd := range preparedDevices {
			if pd == nil || pd.ContainerEdits == nil || pd.ContainerEdits.ContainerEdits == nil {
				continue
			}
			for _, n := range pd.ContainerEdits.ContainerEdits.DeviceNodes {
				if n == nil || n.HostPath == "" || (n.Major == 0 && n.Minor == 0) {
					continue
				}
				major, minor, _, _, err := getDeviceAttrs(n.HostPath)
				if err != nil || major != n.Major || minor != n.Minor {
					klog.Warningf("checkpointed device node %s resolves to %d:%d now, recorded %d:%d (err=%v); the checkpoint is stale", n.HostPath, major, minor, n.Major, n.Minor, err)
					return false
				}
			}
		}
	}
	return true
}

// reconcileCDISpecs rebuilds the per-claim CDI spec files from the checkpoint on a
// plugin restart, which can clear the (often tmpfs) spec directory while the
// checkpoint survives. A checkpoint from a confirmed different boot is discarded rather
// than replayed, since a reboot also clears kubelet's prepared state. Otherwise the
// specs are rebuilt when the device nodes still match, and startup fails when they do
// not, because deleting them while kubelet still holds the claim would strand it. A
// corrupt or no-longer-allocatable entry is skipped, leaving it as unusable as it
// already was; a CDI write error fails startup, because the kubelet may not re-Prepare
// an already-prepared claim.
func (s *DeviceState) reconcileCDISpecs() error {
	checkpoint := newCheckpoint()
	if err := s.checkpointManager.GetCheckpoint(DriverPluginCheckpointFile, checkpoint); err != nil {
		return fmt.Errorf("unable to sync from checkpoint: %v", err)
	}
	if checkpoint.V1 == nil {
		return fmt.Errorf("checkpoint has no v1 payload")
	}

	// Discarding the checkpoint deletes the CDI spec files that kubelet's cached CDI IDs
	// resolve against. That is only safe when kubelet has also dropped its prepared-claim
	// state, which happens across a real reboot. A reboot changes the boot id, so only a
	// recorded epoch that is present and differs from the current boot proves one. A
	// missing or unreadable epoch does not: the first restart after this field was added
	// has none, and a plugin-only upgrade keeps the same kubelet running. In those cases
	// kubelet still considers the claims prepared and will not call Prepare again, so
	// discarding here would strand them with no spec to resolve. Cross-boot and
	// cross-reload device identity still need the stable name tracked in #83.
	stored, trusted := s.readStoredEpoch()
	switch {
	case !trusted || s.bootID == "":
		// Nothing to compare against, so the checkpoint is kept and verified below.
	case stored.Checkpoint != checkpoint.Checksum:
		// The checkpoint was written after this epoch was recorded, so it describes
		// claims some other binary prepared. A reboot cannot be attributed to them, and
		// discarding would drop claims kubelet may hold from that later Prepare.
		klog.Warningf("boot epoch describes checkpoint %d but the checkpoint is now %d; keeping it and verifying instead of discarding", stored.Checkpoint, checkpoint.Checksum)
	case stored.BootID != s.bootID:
		klog.Warningf("checkpoint boot epoch %q differs from current boot %q; discarding %d checkpointed claim(s) without replay", stored.BootID, s.bootID, len(checkpoint.V1.PreparedClaims))
		return s.discardCheckpoint(checkpoint.V1.PreparedClaims)
	}

	// Same boot, or a boot that cannot be confirmed. kubelet has not necessarily
	// restarted, so its prepared claims must be preserved. A same-boot driver reload can
	// still renumber a device node (for example /dev/kfd's major) without changing the
	// boot id: rebuild the specs when every checkpointed node still resolves to its
	// recorded numbers, and fail startup when one does not, rather than discarding and
	// registering healthy while kubelet keeps the claim. Rebuilding a spec that points at
	// the wrong numbers, or silently dropping a claim kubelet still holds, are both worse
	// than refusing to start and letting an operator drain or restart kubelet.
	if !s.deviceNodesCurrent(checkpoint.V1.PreparedClaims) {
		return fmt.Errorf("checkpointed device nodes no longer match the host and the boot epoch (stored %q, current %q) does not confirm a reboot; refusing to discard %d claim(s) kubelet may still consider prepared", stored.BootID, s.bootID, len(checkpoint.V1.PreparedClaims))
	}

	for claimUID, preparedDevices := range checkpoint.V1.PreparedClaims {
		if err := s.validateCheckpointedClaim(claimUID, preparedDevices); err != nil {
			// Skipped, not fatal. Failing startup here would take the whole node's GPUs
			// down for one bad entry, and an on-demand VFIO conversion reaches this
			// legitimately: the device comes back under its VFIO name, so the
			// checkpointed name is no longer allocatable. Recovering that claim needs
			// the type-aware, stable identity tracked in #83 and #86; until then the
			// claim stays unusable and this only avoids making it worse.
			klog.Warningf("skipping unrecoverable checkpoint entry for claim %s: %v", claimUID, err)
			// Leaving the old spec behind on a persistent spec directory would let the
			// runtime keep resolving the claim to device nodes that may now belong to
			// another GPU, so remove it rather than only declining to rebuild it.
			if rmErr := s.cdi.DeleteClaimSpecFile(claimUID); rmErr != nil {
				return fmt.Errorf("unable to remove the spec for unrecoverable claim %s: %w", claimUID, rmErr)
			}
			continue
		}
		if err := s.cdi.CreateClaimSpecFile(claimUID, preparedDevices); err != nil {
			// A CDI write or filesystem error is not self-healing: the kubelet may keep
			// the claim marked prepared and never call Prepare again, so fail startup
			// loudly instead of registering with a missing spec.
			return fmt.Errorf("rebuild CDI spec for claim %s on startup: %w", claimUID, err)
		}
	}
	return nil
}

func (s *DeviceState) Prepare(claim *resourceapi.ResourceClaim) ([]*drapbv1.Device, error) {
	s.Lock()
	defer s.Unlock()

	claimUID := string(claim.UID)

	checkpoint := newCheckpoint()
	if err := s.checkpointManager.GetCheckpoint(DriverPluginCheckpointFile, checkpoint); err != nil {
		return nil, fmt.Errorf("unable to sync from checkpoint: %v", err)
	}
	if checkpoint.V1 == nil {
		return nil, fmt.Errorf("checkpoint has no v1 payload")
	}
	if checkpoint.V1.PreparedClaims == nil {
		// An explicit "preparedClaims": null decodes to a nil map; initialize it so the
		// assignment below does not panic.
		checkpoint.V1.PreparedClaims = make(PreparedClaims)
	}
	preparedClaims := checkpoint.V1.PreparedClaims

	if preparedDevices := preparedClaims[claimUID]; preparedDevices != nil {
		// The spec directory can be cleared (tmpfs) while the checkpoint survives, so
		// make sure the spec exists before reporting the claim as already prepared.
		if err := s.ensureClaimSpec(claimUID, preparedDevices); err != nil {
			return nil, err
		}
		return preparedDevices.GetDevices(), nil
	}

	preparedDevices, err := s.prepareDevices(claim)
	if err != nil {
		return nil, fmt.Errorf("prepare failed: %v", err)
	}

	if err = s.cdi.CreateClaimSpecFile(claimUID, preparedDevices); err != nil {
		return nil, fmt.Errorf("unable to create CDI spec file for claim: %v", err)
	}

	preparedClaims[claimUID] = preparedDevices
	if err := s.saveCheckpoint(checkpoint); err != nil {
		return nil, fmt.Errorf("unable to sync to checkpoint: %v", err)
	}

	return preparedClaims[claimUID].GetDevices(), nil
}

func (s *DeviceState) Unprepare(claimUID string) error {
	s.Lock()
	defer s.Unlock()

	checkpoint := newCheckpoint()
	if err := s.checkpointManager.GetCheckpoint(DriverPluginCheckpointFile, checkpoint); err != nil {
		return fmt.Errorf("unable to sync from checkpoint: %v", err)
	}
	if checkpoint.V1 == nil {
		return fmt.Errorf("checkpoint has no v1 payload")
	}
	preparedClaims := checkpoint.V1.PreparedClaims

	if preparedClaims[claimUID] == nil {
		return nil
	}

	// Startup validates the checkpoint, but Unprepare is reached again on every claim
	// teardown, so a malformed entry would otherwise be dereferenced here instead.
	if err := validatePreparedDevices(claimUID, preparedClaims[claimUID]); err != nil {
		return fmt.Errorf("cannot unprepare claim %s: %w", claimUID, err)
	}

	if err := s.unprepareDevices(claimUID, preparedClaims[claimUID]); err != nil {
		return fmt.Errorf("unprepare failed: %v", err)
	}

	err := s.cdi.DeleteClaimSpecFile(claimUID)
	if err != nil {
		return fmt.Errorf("unable to delete CDI spec file for claim: %v", err)
	}

	delete(preparedClaims, claimUID)
	if err := s.saveCheckpoint(checkpoint); err != nil {
		return fmt.Errorf("unable to sync to checkpoint: %v", err)
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
			containerEdits, err := s.applyConfig(castConfig, results)
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
				edits, err := s.applyVFIOConfig(result)
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
				perDeviceCDIContainerEdits[result.Device] = edits
			}
		default:
			return nil, fmt.Errorf("runtime object is not a recognized configuration")
		}
	}

	// Walk through each config and its associated device allocation results
	// and construct the list of prepared devices to return.
	var preparedDevices PreparedDevices
	for _, results := range configResultsMap {
		for _, result := range results {
			cdiDeviceIDs := s.cdi.GetClaimDevices(string(claim.UID), []string{result.Device})
			device := &PreparedDevice{
				Device: drapbv1.Device{
					RequestNames: []string{result.Request},
					PoolName:     result.Pool,
					DeviceName:   result.Device,
					CdiDeviceIds: cdiDeviceIDs,
				},
				ContainerEdits: perDeviceCDIContainerEdits[result.Device],
			}
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
			// Reporting success here would tell kubelet the device was cleaned up while
			// its host state, including a driver binding left by a VFIO conversion, is
			// whatever the last Prepare made it. Surface it instead of releasing the
			// claim on an unverifiable cleanup.
			errs = append(errs, fmt.Errorf("device %s is no longer in the inventory, so its host state cannot be restored", device.DeviceName))
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
func (s *DeviceState) applyConfig(config *configapi.GpuConfig, results []*resourceapi.DeviceRequestAllocationResult) (PerDeviceCDIContainerEdits, error) {
	perDeviceEdits := make(PerDeviceCDIContainerEdits)

	for _, result := range results {
		klog.Infof("received allocation result: %+v", result)
		card, renderD, err := parseDeviceName(result.Device)
		if err != nil {
			return nil, fmt.Errorf("error parsing device name %s: %w", result.Device, err)
		}
		// TODO implement GPU sharing config when it is available
		//switch {
		//case config.Sharing.IsTimeSlicing():
		//	// TODO implement time slicing config when it is available
		//case config.Sharing.IsSpacePartitioning():
		//	// TODO implement space partitioning config when it is available
		//}

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

		edits := &cdispec.ContainerEdits{
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
		}

		perDeviceEdits[result.Device] = &cdiapi.ContainerEdits{ContainerEdits: edits}
	}

	return perDeviceEdits, nil
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
func (s *DeviceState) applyVFIOConfig(result *resourceapi.DeviceRequestAllocationResult) (*cdiapi.ContainerEdits, error) {
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

	deviceEdits, err := GetVfioCDIContainerEdits(device.Vfio)
	if err != nil {
		return nil, fmt.Errorf("error building CDI edits for %s: %w", result.Device, err)
	}

	commonEdits, err := GetVfioCommonCDIContainerEdits()
	if err != nil {
		return nil, fmt.Errorf("error building common VFIO CDI edits: %w", err)
	}
	deviceEdits.ContainerEdits.DeviceNodes = append(deviceEdits.ContainerEdits.DeviceNodes, commonEdits.ContainerEdits.DeviceNodes...)

	klog.Infof("Applied VFIO config for %s: iommuGroup=%s", device.Vfio.PCIAddress, device.Vfio.IOMMUGroup)
	return deviceEdits, nil
}
