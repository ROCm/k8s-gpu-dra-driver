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
	"errors"
	"fmt"
	"sort"

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/amdgpu"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/consts"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	klog "k8s.io/klog/v2"
)

// GPU->VFIO conversion bookkeeping.
//
// A claim with a VfioDeviceConfig may convert a regular amdgpu GPU to a VFIO
// device: its allocatable entry is swapped to Vfio and the hardware is rebound
// to vfio-pci. DeviceState.vfioConversions records, per claim UID, every
// device converted on that claim's behalf together with its original GPU
// info. A record is removed only once the device has actually been rebound to
// its original driver, so a failed rebind keeps both the VFIO entry and the
// record, and a later Prepare retry or Unprepare can finish the job. Records
// are persisted in the checkpoint so a plugin restart does not lose them.
// All access happens under the DeviceState lock.

// recordVfioConversion notes that deviceName was converted from original to
// VFIO for claimUID.
func (s *DeviceState) recordVfioConversion(claimUID, deviceName string, original *AmdGpuInfo) {
	if s.vfioConversions == nil {
		s.vfioConversions = make(map[string]map[string]*AmdGpuInfo)
	}
	if s.vfioConversions[claimUID] == nil {
		s.vfioConversions[claimUID] = make(map[string]*AmdGpuInfo)
	}
	s.vfioConversions[claimUID][deviceName] = original
}

// restoreFromVfio swaps a converted device's allocatable entry back to its
// original GPU and drops the record. Callers must only call it once the
// hardware is back on its original driver (or was never rebound).
func (s *DeviceState) restoreFromVfio(claimUID, deviceName string) {
	original, ok := s.vfioConversions[claimUID][deviceName]
	if !ok {
		return
	}
	if allocDev, exists := s.allocatable[deviceName]; exists {
		allocDev.AmdGpu = original
		allocDev.Vfio = nil
		klog.Infof("Restored %s from VFIO back to AmdGpu type", deviceName)
	}
	delete(s.vfioConversions[claimUID], deviceName)
	if len(s.vfioConversions[claimUID]) == 0 {
		delete(s.vfioConversions, claimUID)
	}
}

// returnToOriginalDriver rebinds a VFIO device to its pre-configure driver.
// Without a VFIO manager (initialization failed or the feature is disabled
// after a restart) nothing can be rebound, so it succeeds only when sysfs
// shows the device already on that driver, e.g. a GPU converted but never
// bound. Otherwise it fails, so callers keep the device and its record until
// a rebind is possible.
func (s *DeviceState) returnToOriginalDriver(info *AmdGpuVFIOInfo) error {
	if s.vfioManager != nil {
		return s.vfioManager.Unconfigure(info)
	}
	current, err := amdgpu.GetPCIDriver(info.PCIAddress)
	if err != nil {
		return fmt.Errorf("VFIO manager unavailable and current driver of %s unknown: %w", info.PCIAddress, err)
	}
	if current != info.preConfigureDriver {
		return fmt.Errorf("VFIO manager unavailable; cannot rebind %s from %q to %q", info.PCIAddress, current, info.preConfigureDriver)
	}
	return nil
}

// releaseClaimVfio unconfigures the named devices that are currently VFIO and
// then restores every conversion recorded for claimUID whose device was
// rebound successfully (or never bound). Devices whose rebind fails stay VFIO
// with their record intact, and the failures are returned.
func (s *DeviceState) releaseClaimVfio(claimUID string, devices []string) error {
	var errs []error
	failed := make(map[string]bool)
	for _, name := range devices {
		dev := s.allocatable[name]
		if dev == nil || dev.Vfio == nil {
			continue
		}
		if err := s.returnToOriginalDriver(dev.Vfio); err != nil {
			failed[name] = true
			errs = append(errs, fmt.Errorf("failed to return %s (%s) to its original driver: %w", name, dev.Vfio.PCIAddress, err))
		}
	}
	for name := range s.vfioConversions[claimUID] {
		if !failed[name] {
			s.restoreFromVfio(claimUID, name)
		}
	}
	return errors.Join(errs...)
}

// releaseStrandedVfio finishes returning devices that an earlier, failed
// Prepare of claimUID converted but could not rebind. It is a no-op when the
// claim has no conversions left.
func (s *DeviceState) releaseStrandedVfio(claimUID string) error {
	names := make([]string, 0, len(s.vfioConversions[claimUID]))
	for name := range s.vfioConversions[claimUID] {
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	klog.Infof("Returning devices %v left converted by a failed prepare of claim %s", names, claimUID)
	return s.releaseClaimVfio(claimUID, names)
}

// saveVfioConversions writes the conversion records into the checkpoint.
func (s *DeviceState) saveVfioConversions(checkpoint *Checkpoint) {
	if len(s.vfioConversions) == 0 {
		checkpoint.V1.VfioConversions = nil
		return
	}
	records := make(map[string]map[string]*VfioConversionRecord, len(s.vfioConversions))
	for claimUID, devices := range s.vfioConversions {
		records[claimUID] = make(map[string]*VfioConversionRecord, len(devices))
		for name, original := range devices {
			rec := &VfioConversionRecord{
				PCIAddress:       original.PCIAddress,
				CardIndex:        original.cardIndex,
				RenderIndex:      original.renderIndex,
				KFDID:            original.KFDID,
				DeviceID:         original.DeviceID,
				DriverVersion:    original.DriverVersion,
				PartitionProfile: original.PartitionProfile,
				ProductName:      original.ProductName,
				MemoryBytes:      original.MemoryBytes,
				ComputeUnits:     original.ComputeUnits,
				SimdUnits:        original.SimdUnits,
				NumaNode:         original.NumaNode,
			}
			if dev := s.allocatable[name]; dev != nil && dev.Vfio != nil {
				rec.IsVF = dev.Vfio.IsVF
				rec.IOMMUGroup = dev.Vfio.IOMMUGroup
			}
			records[claimUID][name] = rec
		}
	}
	checkpoint.V1.VfioConversions = records
}

// recoverVfioConversions rebuilds converted devices after a restart. A
// converted GPU is still bound to vfio-pci, so discovery cannot see it as a
// GPU and instead lists it as a pre-bound VFIO device under another name.
// Recovery drops that duplicate entry and restores the converted entry under
// the name the claim was allocated, so Unprepare can rebind it to amdgpu.
func (s *DeviceState) recoverVfioConversions(records map[string]map[string]*VfioConversionRecord) {
	for claimUID, devices := range records {
		for name, rec := range devices {
			for other, dev := range s.allocatable {
				if other != name && dev.Vfio != nil && dev.Vfio.PCIAddress == rec.PCIAddress {
					klog.Infof("Dropping discovered VFIO device %s: %s is converted for claim %s as %s", other, rec.PCIAddress, claimUID, name)
					delete(s.allocatable, other)
				}
			}

			pciBusIDAttr, _ := deviceattribute.GetPCIBusIDAttribute(rec.PCIAddress)
			pcieRootAttr, err := deviceattribute.GetPCIeRootAttributeByPCIBusID(rec.PCIAddress)
			if err != nil {
				klog.Warningf("Failed to get PCIe root for recovered VFIO conversion %s: %v", rec.PCIAddress, err)
			}
			original := &AmdGpuInfo{
				PCIAddress:       rec.PCIAddress,
				cardIndex:        rec.CardIndex,
				renderIndex:      rec.RenderIndex,
				KFDID:            rec.KFDID,
				DeviceID:         rec.DeviceID,
				DriverVersion:    rec.DriverVersion,
				PartitionProfile: rec.PartitionProfile,
				ProductName:      rec.ProductName,
				MemoryBytes:      rec.MemoryBytes,
				ComputeUnits:     rec.ComputeUnits,
				SimdUnits:        rec.SimdUnits,
				NumaNode:         rec.NumaNode,
				pciBusIDAttr:     pciBusIDAttr,
				pcieRootAttr:     pcieRootAttr,
			}
			s.allocatable[name] = &AllocatableDevice{Vfio: &AmdGpuVFIOInfo{
				PCIAddress:         rec.PCIAddress,
				DeviceID:           rec.DeviceID,
				VendorID:           consts.AMDVendorID,
				ProductName:        rec.ProductName,
				NumaNode:           rec.NumaNode,
				IsVF:               rec.IsVF,
				IOMMUGroup:         rec.IOMMUGroup,
				pciBusIDAttr:       pciBusIDAttr,
				pcieRootAttr:       pcieRootAttr,
				preConfigureDriver: "amdgpu",
				convertedFrom:      original,
			}}
			s.recordVfioConversion(claimUID, name, original)
			klog.Infof("Recovered VFIO conversion of %s (%s) for claim %s", name, rec.PCIAddress, claimUID)
		}
	}
}
