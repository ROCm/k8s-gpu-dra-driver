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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/amdgpu"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/consts"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/featuregates"
	resourceapi "k8s.io/api/resource/v1"
	klog "k8s.io/klog/v2"
)

var pciBDFPattern = regexp.MustCompile(`^[[:xdigit:]]{4}:[[:xdigit:]]{2}:[[:xdigit:]]{2}\.[0-7]$`)

// checkpointReservedBDFs preserves AutoPartition's logical GPU indices while
// GPUs bound to vfio-pci are absent from amdgpu discovery.
func checkpointReservedBDFs(v1 *CheckpointV1) []string {
	var addrs []string
	for _, claim := range v1.DirectVfioBindings {
		if claim == nil {
			continue
		}
		for _, binding := range claim.Devices {
			if binding != nil {
				addrs = append(addrs, binding.PCIAddress)
			}
		}
	}
	for _, devices := range v1.VfioConversions {
		for _, conversion := range devices {
			if conversion != nil {
				addrs = append(addrs, conversion.PCIAddress)
			}
		}
	}
	return addrs
}

func (s *DeviceState) reservedVfioBDFs() []string {
	var addrs []string
	for _, claim := range s.directVfioBindings {
		if claim == nil {
			continue
		}
		for _, binding := range claim.Devices {
			if binding != nil {
				addrs = append(addrs, binding.PCIAddress)
			}
		}
	}
	for _, devices := range s.vfioConversions {
		for _, original := range devices {
			if original != nil {
				addrs = append(addrs, original.PCIAddress)
			}
		}
	}
	return addrs
}

func (s *DeviceState) recordDirectVfioBinding(claimUID, name string, info *AmdGpuVFIOInfo) error {
	if info == nil || info.IsVF || info.preConfigureDriver != consts.AMDGPUDriverName || !info.siblingExclusive {
		return nil
	}
	current, err := amdgpu.GetPCIDriver(info.PCIAddress)
	if err != nil {
		return err
	}
	if current != consts.AMDGPUDriverName {
		return fmt.Errorf("direct VFIO sibling %s changed driver from amdgpu to %q before Prepare", name, current)
	}
	if s.directVfioBindings == nil {
		s.directVfioBindings = make(map[string]*DirectVfioClaim)
	}
	claim := s.directVfioBindings[claimUID]
	if claim == nil {
		claim = &DirectVfioClaim{Devices: make(map[string]*DirectVfioBinding)}
		s.directVfioBindings[claimUID] = claim
	}
	claim.Devices[name] = &DirectVfioBinding{PCIAddress: info.PCIAddress, OriginalDriver: consts.AMDGPUDriverName}
	return nil
}

func (s *DeviceState) saveDirectVfioBindings(checkpoint *Checkpoint) {
	if len(s.directVfioBindings) == 0 {
		checkpoint.V1.DirectVfioBindings = nil
		return
	}
	checkpoint.V1.DirectVfioBindings = s.directVfioBindings
}

func (s *DeviceState) saveVfioIntents(checkpoint *Checkpoint) {
	s.saveVfioConversions(checkpoint)
	s.saveDirectVfioBindings(checkpoint)
}

func (s *DeviceState) directBinding(claimUID, name string) *DirectVfioBinding {
	if claim := s.directVfioBindings[claimUID]; claim != nil {
		return claim.Devices[name]
	}
	return nil
}

func (s *DeviceState) hasComputePF(pciAddr string) bool {
	for _, dev := range s.allocatable {
		if dev.AmdGpu != nil && dev.AmdGpu.PCIAddress == pciAddr {
			return true
		}
	}
	return false
}

func (s *DeviceState) validatePreparedDirectVfio(claimUID string) error {
	claim := s.directVfioBindings[claimUID]
	if claim == nil {
		return nil
	}
	if claim.Releasing {
		return fmt.Errorf("direct VFIO claim %s is being released; retry Unprepare", claimUID)
	}
	if s.vfioManager == nil {
		return fmt.Errorf("VFIO manager unavailable for prepared direct VFIO claim %s", claimUID)
	}
	for _, binding := range claim.Devices {
		current, err := amdgpu.GetPCIDriver(binding.PCIAddress)
		if err != nil {
			return fmt.Errorf("cannot verify prepared direct VFIO PF %s: %w", binding.PCIAddress, err)
		}
		if current != consts.VFIODriverName {
			return fmt.Errorf("prepared direct VFIO PF %s is bound to %q, expected vfio-pci", binding.PCIAddress, current)
		}
	}
	return nil
}

// The scheduler's shared counter normally excludes sibling claims. Check the
// BDF locally as well when it has released a force-deleted claim too early.
func (s *DeviceState) validateNoOverlappingPFs(claim *resourceapi.ResourceClaim, prepared PreparedClaims) error {
	if claim.Status.Allocation == nil {
		return nil
	}
	claimUID := string(claim.UID)
	requested := make(map[string]string)
	for _, result := range claim.Status.Allocation.Devices.Results {
		if result.Driver != consts.DriverName || result.ShareID != nil {
			continue
		}
		dev := s.allocatable[result.Device]
		if dev == nil {
			continue
		}
		pciAddr := dev.GetPCIAddress()
		paired := dev.AmdGpu != nil && dev.AmdGpu.siblingExclusive ||
			dev.Vfio != nil && dev.Vfio.siblingExclusive
		if !paired {
			continue
		}
		if previous, exists := requested[pciAddr]; exists {
			return fmt.Errorf("claim %s requests PF %s through both %s and %s", claimUID, pciAddr, previous, result.Device)
		}
		requested[pciAddr] = result.Device
	}
	for pciAddr := range requested {
		for otherUID, devices := range prepared {
			if otherUID == claimUID {
				continue
			}
			for _, preparedDev := range devices {
				if other := s.allocatable[preparedDev.DeviceName]; other != nil && other.GetPCIAddress() == pciAddr {
					return fmt.Errorf("PF %s is already prepared for claim %s", pciAddr, otherUID)
				}
			}
		}
		for otherUID, direct := range s.directVfioBindings {
			if otherUID == claimUID {
				continue
			}
			for _, binding := range direct.Devices {
				if binding.PCIAddress == pciAddr {
					return fmt.Errorf("PF %s is still held by direct VFIO claim %s", pciAddr, otherUID)
				}
			}
		}
	}
	return nil
}

func directVFIOName(pciAddr string) string { return "gpu-vfio-" + pciAddrToDNSLabel(pciAddr) }

func validateDirectBinding(name string, binding *DirectVfioBinding) error {
	if binding == nil || !pciBDFPattern.MatchString(binding.PCIAddress) ||
		name != directVFIOName(binding.PCIAddress) || binding.OriginalDriver != consts.AMDGPUDriverName {
		return fmt.Errorf("invalid direct VFIO binding %q: %+v", name, binding)
	}
	return nil
}

// withholdDirectBDF removes both representations of an unsafe PF. Its durable
// binding record remains available for retry even when discovery did not find it.
func (s *DeviceState) withholdDirectBDF(pciAddr string) error {
	for name, dev := range s.allocatable {
		if dev.GetPCIAddress() == pciAddr ||
			(dev.SyntheticPartition != nil && dev.SyntheticPartition.PCIAddress == pciAddr) {
			delete(s.allocatable, name)
		}
	}
	markSiblingPairs(s.allocatable)
	if s.driver != nil {
		return s.driver.republishResourcesLocked(context.TODO())
	}
	return nil
}

// recoverDirectVfioBindings distinguishes a claimed sibling from an
// operator-prebound PF. A release interrupted by restart is resumed before
// the initial ResourceSlice publication.
func (s *DeviceState) recoverDirectVfioBindings(checkpoint *Checkpoint) error {
	owners := make(map[string]string)
	pendingClaims := make(map[string]bool)
	for claimUID, claim := range s.directVfioBindings {
		if claim == nil || len(claim.Devices) == 0 {
			return fmt.Errorf("empty direct VFIO binding for claim %s", claimUID)
		}
		for name, binding := range claim.Devices {
			if err := validateDirectBinding(name, binding); err != nil {
				return err
			}
			if owner, exists := owners[binding.PCIAddress]; exists && owner != claimUID {
				return fmt.Errorf("direct VFIO PF %s recorded for both %s and %s", binding.PCIAddress, owner, claimUID)
			}
			owners[binding.PCIAddress] = claimUID
			current, err := amdgpu.GetPCIDriver(binding.PCIAddress)
			if err != nil {
				klog.Warningf("Cannot read driver for direct VFIO PF %s: %v", binding.PCIAddress, err)
			}
			if claim.Releasing || checkpoint.V1.PreparedClaims[claimUID] == nil {
				if err := s.withholdDirectBDF(binding.PCIAddress); err != nil {
					klog.Warningf("Cannot withhold direct VFIO PF %s: %v", binding.PCIAddress, err)
				}
				pendingClaims[claimUID] = true
				continue
			}
			preparedName := false
			for _, prepared := range checkpoint.V1.PreparedClaims[claimUID] {
				if prepared != nil && prepared.DeviceName == name {
					preparedName = true
					break
				}
			}
			if !preparedName {
				return fmt.Errorf("direct VFIO binding %s is absent from prepared claim %s", name, claimUID)
			}
			if err == nil && current == consts.VFIODriverName {
				if dev := s.allocatable[name]; dev != nil && dev.Vfio != nil && dev.Vfio.PCIAddress == binding.PCIAddress && !dev.Vfio.IsVF {
					dev.Vfio.preConfigureDriver = binding.OriginalDriver
					for otherName, other := range s.allocatable {
						if otherName != name && other.GetPCIAddress() == binding.PCIAddress {
							delete(s.allocatable, otherName)
						}
					}
					markSiblingPairs(s.allocatable)
					continue
				}
			}
			klog.Warningf("Withholding direct VFIO PF %s: prepared claim %s is not discoverable on vfio-pci", binding.PCIAddress, claimUID)
			if err := s.withholdDirectBDF(binding.PCIAddress); err != nil {
				klog.Warningf("Cannot withhold direct VFIO PF %s: %v", binding.PCIAddress, err)
			}
		}
	}
	pending := make([]string, 0, len(pendingClaims))
	for claimUID := range pendingClaims {
		pending = append(pending, claimUID)
	}
	sort.Strings(pending)
	for _, claimUID := range pending {
		if err := s.Unprepare(claimUID); err != nil {
			klog.Warningf("Direct VFIO cleanup of claim %s will retry: %v", claimUID, err)
		}
	}
	return nil
}

// refreshDirectPF rebuilds the device from live amdgpu discovery after a
// successful rebind; cached card/render indices may no longer be valid.
func (s *DeviceState) refreshDirectPF(pciAddr string) error {
	discovered, _, _, err := enumerateAllPossibleDevices(s.syntheticPartition, s.reservedVfioBDFs()...)
	if err != nil {
		return err
	}
	current, err := amdgpu.GetPCIDriver(pciAddr)
	if err != nil || current != consts.AMDGPUDriverName {
		return fmt.Errorf("PF %s is not bound to amdgpu after release (driver %q, error %v)", pciAddr, current, err)
	}
	var gpu *AllocatableDevice
	var vfio *AllocatableDevice
	for _, dev := range discovered {
		if dev.GetPCIAddress() != pciAddr {
			continue
		}
		if dev.AmdGpu != nil {
			gpu = dev
		} else if dev.Vfio != nil && !dev.Vfio.IsVF {
			vfio = dev
		}
	}
	if gpu == nil {
		return fmt.Errorf("amdgpu PF %s not yet visible after rebind", pciAddr)
	}
	if s.vfioManager == nil || !featuregates.Enabled(featuregates.VFIOPassthrough) {
		vfio = nil
	} else if vfio != nil && vfio.Vfio.preConfigureDriver != consts.AMDGPUDriverName {
		return fmt.Errorf("rediscovered VFIO sibling %s has unexpected original driver %q", pciAddr, vfio.Vfio.preConfigureDriver)
	}
	if existing := s.allocatable[gpu.CanonicalName()]; existing != nil && existing.GetPCIAddress() != pciAddr {
		return fmt.Errorf("rediscovered GPU %s (%s) collides with existing device %s", gpu.CanonicalName(), pciAddr, existing.GetPCIAddress())
	}
	for name, dev := range s.allocatable {
		if dev.GetPCIAddress() == pciAddr {
			delete(s.allocatable, name)
		}
	}
	s.allocatable[gpu.CanonicalName()] = gpu
	if vfio != nil {
		s.allocatable[vfio.CanonicalName()] = vfio
	}
	markSiblingPairs(s.allocatable)
	if s.driver != nil {
		return s.driver.republishResourcesLocked(context.TODO())
	}
	return nil
}

func (s *DeviceState) releaseDirectBinding(name string, binding *DirectVfioBinding) error {
	physfn := filepath.Join(amdgpu.PCIDevicePath, binding.PCIAddress, "physfn")
	if _, err := os.Lstat(physfn); err == nil {
		return fmt.Errorf("recorded direct VFIO PF %s is now an SR-IOV VF", binding.PCIAddress)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("cannot verify PF identity of %s: %w", binding.PCIAddress, err)
	}
	current, err := amdgpu.GetPCIDriver(binding.PCIAddress)
	if err != nil {
		return err
	}
	if current != consts.VFIODriverName && current != consts.AMDGPUDriverName && current != "" {
		return fmt.Errorf("direct VFIO PF %s is bound to unexpected driver %q", binding.PCIAddress, current)
	}
	info := &AmdGpuVFIOInfo{PCIAddress: binding.PCIAddress, preConfigureDriver: binding.OriginalDriver}
	if dev := s.allocatable[name]; dev != nil && dev.Vfio != nil && dev.Vfio.PCIAddress == binding.PCIAddress {
		info = dev.Vfio
		info.preConfigureDriver = binding.OriginalDriver
	}
	if err := s.returnToOriginalDriver(info); err != nil {
		return err
	}
	if err := s.refreshDirectPF(binding.PCIAddress); err != nil {
		return err
	}
	return nil
}
