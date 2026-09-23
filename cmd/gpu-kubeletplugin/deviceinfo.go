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
	"fmt"
	"strings"

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/consts"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/utils/ptr"

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/amdgpu"
)

// AmdGpuInfo represents a full AMD GPU device
type AmdGpuInfo struct {
	UUID             string
	ProductName      string
	KFDID            string // KFD-derived PCI address for internal parent-child tracking
	DeviceID         string // sysfs PCI device ID (e.g., "0x740f")
	DriverVersion    string
	PCIAddress       string
	PartitionProfile string
	MemoryBytes      uint64
	ComputeUnits     int
	SimdUnits        int
	NumaNode         int
	ParentPFAddress  string
	TotalVFs         int
	IsVF             bool
	cardIndex        int // unexported: for CanonicalName and CDI path derivation
	renderIndex      int // unexported: for CanonicalName and CDI path derivation
	pcieRootAttr     deviceattribute.DeviceAttribute
	pciBusIDAttr     deviceattribute.DeviceAttribute
}

// AmdPartitionInfo represents a partition of an AMD GPU
type AmdPartitionInfo struct {
	Parent           *AmdGpuInfo
	UUID             string
	PartitionProfile string
	MemoryBytes      uint64
	ComputeUnits     int
	SimdUnits        int
	NumaNode         int
	cardIndex        int // unexported: for CanonicalName and CDI path derivation
	renderIndex      int // unexported: for CanonicalName and CDI path derivation
}

// CanonicalName returns the canonical name for this GPU
func (d *AmdGpuInfo) CanonicalName() string {
	return fmt.Sprintf("gpu-%v-%v", d.cardIndex, d.renderIndex)
}

// GetDevice returns the DRA Device representation for a full AMD GPU
func (d *AmdGpuInfo) GetDevice() resourceapi.Device {
	attributes := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
		"type":        {StringValue: ptr.To(consts.AmdGpuDeviceType)},
		"productName": {StringValue: ptr.To(d.ProductName)},
		"numaNode":    {IntValue: ptr.To(int64(d.NumaNode))},
	}
	if d.DriverVersion != "" {
		attributes["driverVersion"] = resourceapi.DeviceAttribute{VersionValue: ptr.To(amdgpu.SemverDriverVersion(d.DriverVersion))}
		attributes["driverVersionFull"] = resourceapi.DeviceAttribute{StringValue: ptr.To(d.DriverVersion)}
	}
	if d.DeviceID != "" {
		attributes["deviceID"] = resourceapi.DeviceAttribute{StringValue: ptr.To(d.DeviceID)}
	}
	if d.PartitionProfile != "" {
		attributes["partitionProfile"] = resourceapi.DeviceAttribute{StringValue: ptr.To(d.PartitionProfile)}
	}
	if d.pciBusIDAttr.Name != "" {
		attributes[d.pciBusIDAttr.Name] = d.pciBusIDAttr.Value
	}
	if d.pcieRootAttr.Name != "" {
		attributes[d.pcieRootAttr.Name] = d.pcieRootAttr.Value
	}
	return resourceapi.Device{
		Name:             d.CanonicalName(),
		Attributes:       attributes,
		ConsumesCounters: getConsumesCounters(d.ParentPFAddress, d.TotalVFs, d.IsVF),
		Capacity: map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
			"memory":       {Value: *resource.NewQuantity(int64(d.MemoryBytes), resource.BinarySI)},
			"computeUnits": {Value: *resource.NewQuantity(int64(d.ComputeUnits), resource.BinarySI)},
			"simdUnits":    {Value: *resource.NewQuantity(int64(d.SimdUnits), resource.BinarySI)},
		},
	}
}

// AmdGpuVFIOInfo represents a GIM SR-IOV VF for VFIO passthrough
const VFSlotCounterName = "vf-slots"

func getSharedCounterSetName(parentPFAddress string, totalVFs int) string {
	if totalVFs == 0 || parentPFAddress == "" {
		return ""
	}
	return fmt.Sprintf("pf-%s-counter-set", pciAddrToDNSLabel(parentPFAddress))
}

func getSharedCounterSet(parentPFAddress string, totalVFs int) *resourceapi.CounterSet {
	name := getSharedCounterSetName(parentPFAddress, totalVFs)
	if name == "" {
		return nil
	}
	return &resourceapi.CounterSet{
		Name: name,
		Counters: map[string]resourceapi.Counter{
			VFSlotCounterName: {Value: *resource.NewQuantity(int64(totalVFs), resource.BinarySI)},
		},
	}
}

func getConsumesCounters(parentPFAddress string, totalVFs int, isVF bool) []resourceapi.DeviceCounterConsumption {
	name := getSharedCounterSetName(parentPFAddress, totalVFs)
	if name == "" {
		return nil
	}
	consumed := int64(totalVFs)
	if isVF {
		consumed = 1
	}
	return []resourceapi.DeviceCounterConsumption{{
		CounterSet: name,
		Counters: map[string]resourceapi.Counter{
			VFSlotCounterName: {Value: *resource.NewQuantity(consumed, resource.BinarySI)},
		},
	}}
}

type AmdGpuVFIOInfo struct {
	PCIAddress         string
	DeviceID           string
	VendorID           string
	IOMMUGroup         string
	Index              int
	ProductName        string
	NumaNode           int
	IsVF               bool
	pciBusIDAttr       deviceattribute.DeviceAttribute
	pcieRootAttr       deviceattribute.DeviceAttribute
	preConfigureDriver string
	ParentPFAddress    string
	TotalVFs           int
	NumVFs             int
	MemoryBytes        uint64
	ComputeUnits       int
	SimdUnits          int
}

func (d *AmdGpuVFIOInfo) partitionMode() string {
	switch d.NumVFs {
	case 1:
		return "spx"
	case 2:
		return "dpx"
	case 3:
		return "tpx"
	case 4:
		return "qpx"
	case 8:
		return "cpx"
	default:
		return ""
	}
}

// CanonicalName returns the canonical name for this VFIO device
func (d *AmdGpuVFIOInfo) CanonicalName() string {
	return fmt.Sprintf("gpu-vfio-%d", d.Index)
}

func pciAddrToDNSLabel(addr string) string {
	return strings.NewReplacer(":", "-", ".", "-").Replace(addr)
}

// GetSharedCounterSetName returns the KEP-4815 counter set name for the parent
// PF of this VFIO device. Returns "" if this device has no SR-IOV capability.
func (d *AmdGpuVFIOInfo) GetSharedCounterSetName() string {
	return getSharedCounterSetName(d.ParentPFAddress, d.TotalVFs)
}

// GetSharedCounterSet returns the KEP-4815 CounterSet for the parent PF.
func (d *AmdGpuVFIOInfo) GetSharedCounterSet() *resourceapi.CounterSet {
	return getSharedCounterSet(d.ParentPFAddress, d.TotalVFs)
}

// GetConsumesCounters returns the KEP-4815 counter consumption for this device.
// VFs consume 1 vf-slot; PFs consume all vf-slots (mutually exclusive with VFs).
func (d *AmdGpuVFIOInfo) GetConsumesCounters() []resourceapi.DeviceCounterConsumption {
	return getConsumesCounters(d.ParentPFAddress, d.TotalVFs, d.IsVF)
}

// GetDevice returns the DRA Device representation for a VFIO passthrough GPU
func (d *AmdGpuVFIOInfo) GetDevice() resourceapi.Device {
	attributes := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
		"type":       {StringValue: ptr.To(consts.VfioDeviceType)},
		"numaNode":   {IntValue: ptr.To(int64(d.NumaNode))},
		"iommuGroup": {StringValue: ptr.To(d.IOMMUGroup)},
		"pciAddr":    {StringValue: ptr.To(d.PCIAddress)},
		"isVF":       {BoolValue: ptr.To(d.IsVF)},
	}
	if d.ProductName != "" {
		attributes["productName"] = resourceapi.DeviceAttribute{StringValue: ptr.To(d.ProductName)}
	}
	if d.DeviceID != "" {
		attributes["deviceID"] = resourceapi.DeviceAttribute{StringValue: ptr.To(d.DeviceID)}
	}
	if d.VendorID != "" {
		attributes["vendorID"] = resourceapi.DeviceAttribute{StringValue: ptr.To(d.VendorID)}
	}
	if d.pciBusIDAttr.Name != "" {
		attributes[d.pciBusIDAttr.Name] = d.pciBusIDAttr.Value
	}
	if d.pcieRootAttr.Name != "" {
		attributes[d.pcieRootAttr.Name] = d.pcieRootAttr.Value
	}
	if mode := d.partitionMode(); mode != "" {
		attributes["partitionProfile"] = resourceapi.DeviceAttribute{StringValue: ptr.To(mode)}
	}
	dev := resourceapi.Device{
		Name:             d.CanonicalName(),
		Attributes:       attributes,
		ConsumesCounters: d.GetConsumesCounters(),
	}
	if d.MemoryBytes > 0 || d.ComputeUnits > 0 || d.SimdUnits > 0 {
		dev.Capacity = map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{}
		if d.MemoryBytes > 0 {
			dev.Capacity["memory"] = resourceapi.DeviceCapacity{Value: *resource.NewQuantity(int64(d.MemoryBytes), resource.BinarySI)}
		}
		if d.ComputeUnits > 0 {
			dev.Capacity["computeUnits"] = resourceapi.DeviceCapacity{Value: *resource.NewQuantity(int64(d.ComputeUnits), resource.BinarySI)}
		}
		if d.SimdUnits > 0 {
			dev.Capacity["simdUnits"] = resourceapi.DeviceCapacity{Value: *resource.NewQuantity(int64(d.SimdUnits), resource.BinarySI)}
		}
	}
	return dev
}

// CanonicalName returns the canonical name for this partition
func (d *AmdPartitionInfo) CanonicalName() string {
	return fmt.Sprintf("gpu-%v-%v", d.cardIndex, d.renderIndex)
}

// GetDevice returns the DRA Device representation for an AMD GPU partition
func (d *AmdPartitionInfo) GetDevice() resourceapi.Device {
	attributes := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
		"type":             {StringValue: ptr.To(consts.AmdPartitionDeviceType)},
		"productName":      {StringValue: ptr.To(d.Parent.ProductName)},
		"partitionProfile": {StringValue: ptr.To(d.PartitionProfile)},
		"numaNode":         {IntValue: ptr.To(int64(d.NumaNode))},
	}
	if d.Parent.DriverVersion != "" {
		attributes["driverVersion"] = resourceapi.DeviceAttribute{VersionValue: ptr.To(amdgpu.SemverDriverVersion(d.Parent.DriverVersion))}
		attributes["driverVersionFull"] = resourceapi.DeviceAttribute{StringValue: ptr.To(d.Parent.DriverVersion)}
	}
	if d.Parent.DeviceID != "" {
		attributes["deviceID"] = resourceapi.DeviceAttribute{StringValue: ptr.To(d.Parent.DeviceID)}
	}
	if d.Parent.pciBusIDAttr.Name != "" {
		attributes[d.Parent.pciBusIDAttr.Name] = d.Parent.pciBusIDAttr.Value
	}
	if d.Parent.pcieRootAttr.Name != "" {
		attributes[d.Parent.pcieRootAttr.Name] = d.Parent.pcieRootAttr.Value
	}
	return resourceapi.Device{
		Name:       d.CanonicalName(),
		Attributes: attributes,
		Capacity: map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
			"memory":       {Value: *resource.NewQuantity(int64(d.MemoryBytes), resource.BinarySI)},
			"computeUnits": {Value: *resource.NewQuantity(int64(d.ComputeUnits), resource.BinarySI)},
			"simdUnits":    {Value: *resource.NewQuantity(int64(d.SimdUnits), resource.BinarySI)},
		},
	}
}

// SyntheticPartitionDevice represents a virtual partition device for synthetic-partition mode.
// Each physical GPU generates one of these for each valid compute+memory combination.
type SyntheticPartitionDevice struct {
	GPUIndex         int
	ComputePartition string
	MemoryPartition  string
	PartitionCount   int
	PCIAddress       string
	ProductName      string
	DeviceID         string
	DriverVersion    string
	MemoryBytes      uint64 // per-partition memory (total / count)
	ComputeUnits     int    // per-partition CUs
	SimdUnits        int    // per-partition SIMDs
	NumaNode         int
	pcieRootAttr     deviceattribute.DeviceAttribute
	pciBusIDAttr     deviceattribute.DeviceAttribute
	// Taints holds any taints applied to this device (e.g. memory partition conflicts).
	// This field is set dynamically and may be updated during runtime.
	Taints []resourceapi.DeviceTaint
}

// CanonicalName returns the canonical name for this synthetic-partition device
func (d *SyntheticPartitionDevice) CanonicalName() string {
	return fmt.Sprintf("gpu-%d-%s-%s", d.GPUIndex, d.ComputePartition, d.MemoryPartition)
}

// GetDevice returns the DRA Device representation for a synthetic-partition device
func (d *SyntheticPartitionDevice) GetDevice() resourceapi.Device {
	// Use the same user-visible type attribute values as real GPU/partition devices:
	// SPX (full GPU, 1 partition) -> "amdgpu", DPX/CPX (partitioned) -> "amdgpu-partition"
	deviceType := consts.AmdPartitionDeviceType
	if d.ComputePartition == consts.ComputePartitionSPX {
		deviceType = consts.AmdGpuDeviceType
	}

	attributes := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
		"type": {
			StringValue: ptr.To(deviceType),
		},
		"computePartition": {
			StringValue: ptr.To(d.ComputePartition),
		},
		"memoryPartition": {
			StringValue: ptr.To(d.MemoryPartition),
		},
		"gpuIndex": {
			IntValue: ptr.To(int64(d.GPUIndex)),
		},
		"productName": {
			StringValue: ptr.To(d.ProductName),
		},
		"pciAddr": {
			StringValue: ptr.To(d.PCIAddress),
		},
		"numaNode": {
			IntValue: ptr.To(int64(d.NumaNode)),
		},
	}
	if d.DriverVersion != "" {
		attributes["driverVersion"] = resourceapi.DeviceAttribute{VersionValue: ptr.To(amdgpu.SemverDriverVersion(d.DriverVersion))}
		attributes["driverVersionFull"] = resourceapi.DeviceAttribute{StringValue: ptr.To(d.DriverVersion)}
	}
	if d.DeviceID != "" {
		attributes["deviceID"] = resourceapi.DeviceAttribute{StringValue: ptr.To(d.DeviceID)}
	}

	// Add PCI bus ID and PCIe root attributes if available
	if d.pciBusIDAttr.Name != "" {
		attributes[d.pciBusIDAttr.Name] = d.pciBusIDAttr.Value
	}
	if d.pcieRootAttr.Name != "" {
		attributes[d.pcieRootAttr.Name] = d.pcieRootAttr.Value
	}

	// Build partitions capacity entry. For partition types with count > 1,
	// use RequestPolicy so each allocation consumes 1 partition by default.
	// For SPX (count=1), omit RequestPolicy since the device cannot be
	// shared and AllowMultipleAllocations would be false.
	partitionsCapacity := resourceapi.DeviceCapacity{
		Value: *resource.NewQuantity(int64(d.PartitionCount), resource.DecimalSI),
	}
	if d.PartitionCount > 1 {
		// ValidValues pins this to exactly one partition's worth: Default alone only
		// supplies a value when a request omits the capacity, it does not cap an
		// explicit request. Without it, a claim could request e.g. partitions: "2"
		// in a single result and get one PartitionShare/CDI device for hardware the
		// scheduler believes it allocated two of.
		oneQty := resource.NewQuantity(1, resource.DecimalSI)
		partitionsCapacity.RequestPolicy = &resourceapi.CapacityRequestPolicy{
			Default:     oneQty,
			ValidValues: []resource.Quantity{*oneQty},
		}
	}

	// Build capacity entries for memory, computeUnits, simdUnits.
	// d.MemoryBytes/ComputeUnits/SimdUnits are per-partition values.
	// Value = total for device (per-partition * count), Default = per-partition.
	memoryCapacity := resourceapi.DeviceCapacity{
		Value: *resource.NewQuantity(int64(d.MemoryBytes)*int64(d.PartitionCount), resource.BinarySI),
	}
	computeUnitsCapacity := resourceapi.DeviceCapacity{
		Value: *resource.NewQuantity(int64(d.ComputeUnits)*int64(d.PartitionCount), resource.DecimalSI),
	}
	simdUnitsCapacity := resourceapi.DeviceCapacity{
		Value: *resource.NewQuantity(int64(d.SimdUnits)*int64(d.PartitionCount), resource.DecimalSI),
	}
	if d.PartitionCount > 1 {
		// Same reasoning as partitionsCapacity above: ValidValues caps each of these
		// at exactly the per-partition amount so an explicit request can't ask for
		// more than one partition's worth of memory/computeUnits/simdUnits either.
		memoryQty := resource.NewQuantity(int64(d.MemoryBytes), resource.BinarySI)
		memoryCapacity.RequestPolicy = &resourceapi.CapacityRequestPolicy{
			Default:     memoryQty,
			ValidValues: []resource.Quantity{*memoryQty},
		}
		computeUnitsQty := resource.NewQuantity(int64(d.ComputeUnits), resource.DecimalSI)
		computeUnitsCapacity.RequestPolicy = &resourceapi.CapacityRequestPolicy{
			Default:     computeUnitsQty,
			ValidValues: []resource.Quantity{*computeUnitsQty},
		}
		simdUnitsQty := resource.NewQuantity(int64(d.SimdUnits), resource.DecimalSI)
		simdUnitsCapacity.RequestPolicy = &resourceapi.CapacityRequestPolicy{
			Default:     simdUnitsQty,
			ValidValues: []resource.Quantity{*simdUnitsQty},
		}
	}

	device := resourceapi.Device{
		Name:       d.CanonicalName(),
		Attributes: attributes,
		Capacity: map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
			"partitions":   partitionsCapacity,
			"memory":       memoryCapacity,
			"computeUnits": computeUnitsCapacity,
			"simdUnits":    simdUnitsCapacity,
		},
		ConsumesCounters: []resourceapi.DeviceCounterConsumption{
			{
				CounterSet: mutexCounterSetName(d.GPUIndex),
				Counters:   mutexCounters(),
			},
		},
	}

	// AllowMultipleAllocations is true for partition types with count > 1
	if d.PartitionCount > 1 {
		device.AllowMultipleAllocations = ptr.To(true)
	}

	// Apply taints if any (e.g., memory partition conflicts)
	if len(d.Taints) > 0 {
		device.Taints = d.Taints
	}

	return device
}

// mutexCounterSetName returns the shared counter set name for a GPU's partition
// mutex. The name a device consumes (ConsumesCounters) and the name the counter
// set is published under must be identical, or the scheduler silently stops
// enforcing one-partition-mode-per-GPU, so both sides call this.
func mutexCounterSetName(gpuIndex int) string {
	return fmt.Sprintf("gpu-%d-mutex", gpuIndex)
}

// mutexCounters returns the counter map for a GPU's partition-mode mutex. The
// capacity of 1 is what makes the modes mutually exclusive: the scheduler can
// satisfy only one partition device per GPU at a time.
func mutexCounters() map[string]resourceapi.Counter {
	return map[string]resourceapi.Counter{
		"partition-mode": {
			Value: *resource.NewQuantity(1, resource.DecimalSI),
		},
	}
}

// buildMutexCounterSet returns the CounterSet for a GPU's partition-mode mutex.
func buildMutexCounterSet(gpuIndex int) resourceapi.CounterSet {
	return resourceapi.CounterSet{
		Name:     mutexCounterSetName(gpuIndex),
		Counters: mutexCounters(),
	}
}

// IsCompatibleMemoryMode reports whether requestedMode can be satisfied while
// activeMode is in effect. Memory mode is node-wide, so once a mode is locked
// only that same mode is allowed; an empty activeMode means the node is unlocked.
func IsCompatibleMemoryMode(activeMode, requestedMode string) bool {
	return activeMode == "" || activeMode == requestedMode
}

// parseSyntheticPartitionDeviceName parses a device name like "gpu-0-cpx-nps4"
// and returns the gpuIndex, compute partition mode, and memory partition mode.
func parseSyntheticPartitionDeviceName(name string) (gpuIndex int, compute, memory string, err error) {
	_, err = fmt.Sscanf(name, "gpu-%d-", &gpuIndex)
	if err != nil {
		return 0, "", "", fmt.Errorf("failed to parse synthetic-partition device name %s: %v", name, err)
	}

	// Parse the remaining parts after "gpu-<index>-"
	prefix := fmt.Sprintf("gpu-%d-", gpuIndex)
	remainder := name[len(prefix):]

	// Valid patterns: "spx-nps1", "dpx-nps2", "cpx-nps1", "cpx-nps4"
	for _, cfg := range consts.ValidPartitionConfigs {
		expected := fmt.Sprintf("%s-%s", cfg.Compute, cfg.Memory)
		if remainder == expected {
			return gpuIndex, cfg.Compute, cfg.Memory, nil
		}
	}

	return 0, "", "", fmt.Errorf("unrecognized synthetic-partition device name: %s", name)
}
