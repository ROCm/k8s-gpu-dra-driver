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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"

	configapi "github.com/ROCm/k8s-gpu-dra-driver/api/amd.com/resource/gpu/v1alpha1"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/amdgpu"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/consts"
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

	// Always initialize VFIO manager if IOMMU is available. GIM VFs on
	// amdgpu are VFIO-capable and can be dynamically bound to vfio-pci
	// during Prepare when a VfioDeviceConfig is present in the claim.
	var vfioMgr *VfioPciManager
	vfioMgr, err2 := NewVfioPciManager()
	if err2 != nil {
		klog.Warningf("VFIO manager initialization failed (VFIO passthrough unavailable): %v", err2)
		vfioMgr = nil
		for name, dev := range allocatable {
			if dev.Type() == VfioDeviceType {
				delete(allocatable, name)
			}
		}
	}

	state := &DeviceState{
		cdi:               cdi,
		allocatable:       allocatable,
		vfioManager:       vfioMgr,
		checkpointManager: checkpointManager,
	}

	checkpoints, err := state.checkpointManager.ListCheckpoints()
	if err != nil {
		return nil, fmt.Errorf("unable to list checkpoints: %v", err)
	}

	for _, c := range checkpoints {
		if c == DriverPluginCheckpointFile {
			return state, nil
		}
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

	preparedDevices, err := s.prepareDevices(claim)
	if err != nil {
		return nil, fmt.Errorf("prepare failed: %v", err)
	}

	if err = s.cdi.CreateClaimSpecFile(claimUID, preparedDevices); err != nil {
		return nil, fmt.Errorf("unable to create CDI spec file for claim: %v", err)
	}

	preparedClaims[claimUID] = preparedDevices
	if err := s.checkpointManager.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint); err != nil {
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
	preparedClaims := checkpoint.V1.PreparedClaims

	if preparedClaims[claimUID] == nil {
		return nil
	}

	if err := s.unprepareDevices(claimUID, preparedClaims[claimUID]); err != nil {
		return fmt.Errorf("unprepare failed: %v", err)
	}

	err := s.cdi.DeleteClaimSpecFile(claimUID)
	if err != nil {
		return fmt.Errorf("unable to delete CDI spec file for claim: %v", err)
	}

	delete(preparedClaims, claimUID)
	if err := s.checkpointManager.CreateCheckpoint(DriverPluginCheckpointFile, checkpoint); err != nil {
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
		isVFIO := allocDev.Type() == VfioDeviceType
		for _, c := range slices.Backward(configs) {
			switch c.Config.(type) {
			case *configapi.VfioDeviceConfig:
				if !isVFIO {
					if allocDev.AmdGpu != nil {
						physfn := filepath.Join(amdgpu.PCIDevicePath, allocDev.AmdGpu.PCIAddress, "physfn")
						_, physfnErr := os.Lstat(physfn)
						isVF := physfnErr == nil

						klog.Infof("Converting GPU %s to VFIO device (isVF=%v, VfioDeviceConfig present)", allocDev.AmdGpu.PCIAddress, isVF)
						vfioInfo := &AmdGpuVFIOInfo{
							PCIAddress:         allocDev.AmdGpu.PCIAddress,
							DeviceID:           allocDev.AmdGpu.DeviceID,
							VendorID:           amdgpu.AMDVendorID,
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
			continue
		}
		if allocDev.Type() == VfioDeviceType && allocDev.Vfio != nil && s.vfioManager != nil {
			if err := s.vfioManager.Unconfigure(allocDev.Vfio); err != nil {
				errs = append(errs, fmt.Errorf("failed to unconfigure VFIO device %s: %w", device.DeviceName, err))
			}
			s.restoreFromVfio(device.DeviceName)
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
