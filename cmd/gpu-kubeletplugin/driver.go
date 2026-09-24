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
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	coreclientset "k8s.io/client-go/kubernetes"
	drametadatav1alpha1 "k8s.io/dynamic-resource-allocation/api/metadata/v1alpha1"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	klog "k8s.io/klog/v2"

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/amdsmi"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/consts"
	"github.com/ROCm/k8s-gpu-dra-driver/pkg/featuregates"
)

type driver struct {
	client                   coreclientset.Interface
	helper                   *kubeletplugin.Helper
	state                    *DeviceState
	healthcheck              *healthcheck
	cancelCtx                func(error)
	enableSyntheticPartition bool
	nodeName                 string
	partitionableGPUs        []int

	// publish hands ResourceSlices to the kubelet plugin helper; a field so
	// tests can observe publishes without a running helper.
	publish func(context.Context, resourceslice.DriverResources) error
}

func NewDriver(ctx context.Context, config *Config) (*driver, error) {
	d := &driver{
		client:                   config.coreclient,
		cancelCtx:                config.cancelMainCtx,
		enableSyntheticPartition: featuregates.Enabled(featuregates.AutoPartition),
		nodeName:                 config.flags.nodeName,
	}

	state, err := NewDeviceState(config)
	if err != nil {
		return nil, err
	}
	d.state = state

	// Copy partitionable GPU indices from partition state for counter set building
	if state.partitionState != nil {
		d.partitionableGPUs = state.partitionState.partitionableGPUs
	}

	// Initialize AMD SMI library for GPU partition operations. The discovered PCI
	// addresses bind each GPU index to its processor handle, so partition calls
	// target the same physical GPU that discovery and the sysfs fallbacks do.
	if d.enableSyntheticPartition {
		var gpuPCIAddresses map[int]string
		if state.partitionState != nil {
			gpuPCIAddresses = state.partitionState.gpuPCIAddresses
		}
		if err := amdsmi.Init(gpuPCIAddresses); err != nil {
			return nil, fmt.Errorf("failed to initialize AMD SMI: %v", err)
		}
	}

	opts := []kubeletplugin.Option{
		kubeletplugin.KubeClient(config.coreclient),
		kubeletplugin.NodeName(config.flags.nodeName),
		kubeletplugin.DriverName(consts.DriverName),
		kubeletplugin.RegistrarDirectoryPath(config.flags.kubeletRegistrarDirectoryPath),
		kubeletplugin.PluginDataDirectoryPath(config.DriverPluginPath()),
	}
	if featuregates.Enabled(featuregates.DeviceMetadata) {
		opts = append(opts,
			kubeletplugin.EnableDeviceMetadata(true),
			kubeletplugin.MetadataVersions(drametadatav1alpha1.SchemeGroupVersion),
		)
		klog.Infof("DeviceMetadata feature gate enabled: KEP-5304 device metadata will be published")
	}
	helper, err := kubeletplugin.Start(ctx, d, opts...)
	if err != nil {
		return nil, err
	}
	d.helper = helper
	d.publish = helper.PublishResources
	// Store helper reference in state for re-publishing from Prepare/Unprepare
	d.state.driver = d

	d.healthcheck, err = startHealthcheck(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("start healthcheck: %w", err)
	}

	// The plugin is already registered, so Prepare/Unprepare may be running.
	// Build and publish under the state lock like every republish, so this
	// initial publish cannot overwrite a newer one.
	d.state.Lock()
	defer d.state.Unlock()
	resources := d.buildResources()
	if resourcesJSON, err := json.MarshalIndent(resources, "", "  "); err != nil {
		klog.Warningf("Failed to marshal ResourceSlice to JSON: %v", err)
	} else {
		klog.Infof("Publishing ResourceSlice:\n%s", string(resourcesJSON))
	}
	if err := d.publish(ctx, resources); err != nil {
		return nil, err
	}

	return d, nil
}

// buildSyntheticPartitionResources builds DriverResources for synthetic-partition mode.
// Counter sets and devices are placed in separate slices within the same pool.
// The API requires that a ResourceSlice contains either sharedCounters or devices, not both.
//
// Both collections are chunked to the API's per-slice limits
// (resourceapi.ResourceSliceMaxDevicesWithAdvancedFeatures devices,
// resourceapi.ResourceSliceMaxCounterSets counter sets): synthetic-partition devices
// consume shared counters, which counts as an "advanced feature," so the lower
// 64-device limit applies, not the general 128. A node with more than 8
// partitionable GPUs (more than 64 synthetic devices) would otherwise publish an
// invalid ResourceSlice and fail to register any of them.
func (d *driver) buildSyntheticPartitionResources() resourceslice.DriverResources {
	// Build counter sets for partitionable GPUs
	counterSets := make([]resourceapi.CounterSet, 0, len(d.partitionableGPUs))
	for _, gpuIndex := range d.partitionableGPUs {
		counterSets = append(counterSets, buildMutexCounterSet(gpuIndex))
	}
	// Non-partitionable GPUs and VFIO devices consume their family's counter
	// sets (vf-slots, sibling exclusion) in this mode too, so publish them.
	counterSets = append(counterSets, d.collectCounterSets()...)

	// Build device list. Snapshot under the partition-state lock so reads of the
	// dynamically-updated per-device Taints field are synchronized with writes.
	devices := d.state.partitionState.BuildDevices(d.state.allocatable)

	// Use separate slices: one (or more) for shared counters, one (or more) for
	// devices — the API forbids mixing sharedCounters and devices in one slice.
	var slices []resourceslice.Slice
	for _, part := range chunk(devices, resourceapi.ResourceSliceMaxDevicesWithAdvancedFeatures) {
		slices = append(slices, resourceslice.Slice{Devices: part})
	}
	for _, part := range chunk(counterSets, resourceapi.ResourceSliceMaxCounterSets) {
		slices = append(slices, resourceslice.Slice{SharedCounters: part})
	}

	return resourceslice.DriverResources{
		Pools: map[string]resourceslice.Pool{
			d.nodeName: {
				Slices: slices,
			},
		},
	}
}

// chunk splits items into groups of at most size, preserving order. It is
// used for both devices and counter sets, which the API caps per ResourceSlice
// at different limits. A nil/empty input yields no chunks.
func chunk[T any](items []T, size int) [][]T {
	var chunks [][]T
	for len(items) > 0 {
		n := min(size, len(items))
		chunks = append(chunks, items[:n])
		items = items[n:]
	}
	return chunks
}

// republishResourcesLocked re-publishes ResourceSlices from the current device
// state, e.g. when partition taints change. The caller must hold the
// DeviceState lock: building and publishing under it keeps the reads of
// allocatable consistent with Prepare/Unprepare, which modify its devices,
// and orders publishes so an older snapshot never replaces a newer one.
// PublishResources only hands the desired state to the ResourceSlice
// controller, so holding the lock across it is cheap.
func (d *driver) republishResourcesLocked(ctx context.Context) error {
	if err := d.publish(ctx, d.buildResources()); err != nil {
		return fmt.Errorf("error re-publishing resources: %v", err)
	}
	klog.Infof("Re-published ResourceSlices")
	return nil
}

// buildResources builds the DriverResources for the current mode. The caller
// must hold the DeviceState lock.
func (d *driver) buildResources() resourceslice.DriverResources {
	if d.enableSyntheticPartition && d.state.partitionState != nil {
		return d.buildSyntheticPartitionResources()
	}
	return d.buildDriverResources(d.nodeName)
}

// buildDriverResources builds normal-mode DriverResources. Like the synthetic
// path, it splits counter sets and devices into separate slices within the
// pool and chunks both to the API's per-slice limits: at most
// ResourceSliceMaxCounterSets counter sets per slice, and at most
// ResourceSliceMaxDevicesWithAdvancedFeatures devices per slice once any
// device consumes counters (ResourceSliceMaxDevices otherwise). A node with
// more than 8 SR-IOV PFs or more than 64 dual/VF entries would otherwise
// publish an invalid ResourceSlice.
func (d *driver) buildDriverResources(nodeName string) resourceslice.DriverResources {
	devices := resourceSliceDevices(d.state.allocatable)
	counterSets := d.collectCounterSets()

	var slicesOut []resourceslice.Slice
	for _, part := range chunk(counterSets, resourceapi.ResourceSliceMaxCounterSets) {
		slicesOut = append(slicesOut, resourceslice.Slice{SharedCounters: part})
	}
	for _, part := range chunk(devices, maxDevicesPerSlice(devices)) {
		slicesOut = append(slicesOut, resourceslice.Slice{Devices: part})
	}
	if len(devices) == 0 {
		// Publish an empty device slice rather than no slice at all, as before.
		slicesOut = append(slicesOut, resourceslice.Slice{Devices: devices})
	}
	return resourceslice.DriverResources{Pools: map[string]resourceslice.Pool{
		nodeName: {Slices: slicesOut},
	}}
}

// maxDevicesPerSlice returns the API's per-slice device limit for devices:
// the lower limit applies as soon as any device consumes counters or carries
// taints.
func maxDevicesPerSlice(devices []resourceapi.Device) int {
	for _, dev := range devices {
		if len(dev.ConsumesCounters) > 0 || len(dev.Taints) > 0 {
			return resourceapi.ResourceSliceMaxDevicesWithAdvancedFeatures
		}
	}
	return resourceapi.ResourceSliceMaxDevices
}

// collectCounterSets returns the KEP-4815 counter sets consumed by the
// compute and VFIO devices, one per device family, sorted by name. Each set
// merges the counters its devices consume (see deviceCounters), so it always
// declares every counter a device references.
func (d *driver) collectCounterSets() []resourceapi.CounterSet {
	sets := make(map[string]map[string]int64)
	for _, device := range d.state.allocatable {
		var parentPF, pciAddr string
		var totalVFs int
		var isVF, siblingExclusive bool
		switch device.Type() {
		case consts.VfioDeviceType:
			v := device.Vfio
			parentPF, pciAddr, totalVFs, isVF, siblingExclusive = v.ParentPFAddress, v.PCIAddress, v.TotalVFs, v.IsVF, v.siblingExclusive
		case consts.AmdGpuDeviceType:
			g := device.AmdGpu
			parentPF, pciAddr, totalVFs, isVF, siblingExclusive = g.ParentPFAddress, g.PCIAddress, g.TotalVFs, g.IsVF, g.siblingExclusive
		default:
			continue
		}
		_, capacity := deviceCounters(parentPF, pciAddr, totalVFs, isVF, siblingExclusive)
		if len(capacity) == 0 {
			continue
		}
		name := counterSetName(counterFamily(parentPF, pciAddr))
		if sets[name] == nil {
			sets[name] = make(map[string]int64)
		}
		for counter, value := range capacity {
			sets[name][counter] = value
		}
	}
	names := make([]string, 0, len(sets))
	for name := range sets {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]resourceapi.CounterSet, 0, len(names))
	for _, name := range names {
		counters := make(map[string]resourceapi.Counter, len(sets[name]))
		for counter, value := range sets[name] {
			counters[counter] = resourceapi.Counter{Value: *resource.NewQuantity(value, resource.BinarySI)}
		}
		result = append(result, resourceapi.CounterSet{Name: name, Counters: counters})
	}
	return result
}

// resourceSliceDevices returns the allocatable devices sorted by name. Go map
// iteration order is not specified, so without sorting the published
// ResourceSlice would change on every restart. The order is also the first-fit
// allocation priority the scheduler applies, so keeping it deterministic
// matters beyond avoiding churn.
func resourceSliceDevices(allocatable AllocatableDevices) []resourceapi.Device {
	devices := make([]resourceapi.Device, 0, len(allocatable))
	for device := range maps.Values(allocatable) {
		devices = append(devices, device.GetDevice())
	}
	slices.SortFunc(devices, func(a, b resourceapi.Device) int {
		return cmp.Compare(a.Name, b.Name)
	})
	return devices
}

func (d *driver) Shutdown(logger klog.Logger) error {
	if d.healthcheck != nil {
		d.healthcheck.Stop(logger)
	}
	if d.enableSyntheticPartition {
		amdsmi.Shutdown()
	}
	d.helper.Stop()
	return nil
}

func (d *driver) PrepareResourceClaims(ctx context.Context, claims []*resourceapi.ResourceClaim) (map[types.UID]kubeletplugin.PrepareResult, error) {
	klog.Infof("PrepareResourceClaims is called: number of claims: %d", len(claims))
	result := make(map[types.UID]kubeletplugin.PrepareResult)

	for _, claim := range claims {
		result[claim.UID] = d.prepareResourceClaim(ctx, claim)
	}

	return result, nil
}

func (d *driver) prepareResourceClaim(ctx context.Context, claim *resourceapi.ResourceClaim) kubeletplugin.PrepareResult {
	preparedPBs, err := d.state.Prepare(claim)
	if err != nil {
		return kubeletplugin.PrepareResult{
			Err: fmt.Errorf("error preparing devices for claim %v: %w", claim.UID, err),
		}
	}
	var prepared []kubeletplugin.Device
	for _, preparedPB := range preparedPBs {
		dev := kubeletplugin.Device{
			Requests:     preparedPB.GetRequestNames(),
			PoolName:     preparedPB.GetPoolName(),
			DeviceName:   preparedPB.GetDeviceName(),
			CDIDeviceIDs: preparedPB.GetCdiDeviceIds(),
		}

		if featuregates.Enabled(featuregates.DeviceMetadata) {
			// allocatable is mutated by concurrent Prepare/Unprepare calls.
			d.state.Lock()
			allocDev, exists := d.state.allocatable[preparedPB.GetDeviceName()]
			var device resourceapi.Device
			if exists {
				device = allocDev.GetDevice()
			}
			d.state.Unlock()
			if exists {
				if len(device.Attributes) > 0 {
					attrs := make(map[string]resourceapi.DeviceAttribute, len(device.Attributes))
					for k, v := range device.Attributes {
						attrs[string(k)] = v
					}
					dev.Metadata = &kubeletplugin.DeviceMetadata{
						Attributes: attrs,
					}
				}
			}
		}

		prepared = append(prepared, dev)
	}

	klog.Infof("Returning newly prepared devices for claim '%v': %v", claim.UID, prepared)
	return kubeletplugin.PrepareResult{Devices: prepared}
}

func (d *driver) UnprepareResourceClaims(ctx context.Context, claims []kubeletplugin.NamespacedObject) (map[types.UID]error, error) {
	klog.Infof("UnprepareResourceClaims is called: number of claims: %d", len(claims))
	result := make(map[types.UID]error)

	for _, claim := range claims {
		result[claim.UID] = d.unprepareResourceClaim(ctx, claim)
	}

	return result, nil
}

func (d *driver) unprepareResourceClaim(ctx context.Context, claim kubeletplugin.NamespacedObject) error {
	if err := d.state.Unprepare(string(claim.UID)); err != nil {
		return fmt.Errorf("error unpreparing devices for claim %v: %w", claim.UID, err)
	}
	return nil
}

func (d *driver) HandleError(ctx context.Context, err error, msg string) {
	utilruntime.HandleErrorWithContext(ctx, err, msg)
	if !errors.Is(err, kubeletplugin.ErrRecoverable) && d.cancelCtx != nil {
		d.cancelCtx(fmt.Errorf("fatal background error: %w", err))
	}
}
