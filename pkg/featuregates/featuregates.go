/*
 * Copyright 2026 The Kubernetes Authors.
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

package featuregates

import (
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/component-base/featuregate"
)

// emulationVersion pins the driver registry to the driver's own version line.
// Because this registry contains ONLY driver-owned gates (never upstream gates
// registered at Kubernetes versions), this value never causes a gate to fall
// through to PreAlpha, so there is no startup-panic risk.
var emulationVersion = version.MajorMinor(0, 1)

// DeviceMetadata enables KEP-5304 device metadata: device attributes are
// published alongside prepared devices so the scheduler and kubelet can
// inspect per-device properties (numaNode, pciBusID, pcieRoot, etc.).
const DeviceMetadata featuregate.Feature = "DeviceMetadata"

// VFIOPassthrough enables VFIO passthrough support: discovery of AMD GPUs
// already bound to vfio-pci (PF passthrough) and on-demand binding of GPUs
// from amdgpu to vfio-pci when a VfioDeviceConfig is present in the claim.
const VFIOPassthrough featuregate.Feature = "VFIOPassthrough"

// AutoPartition enables auto-partition mode: the driver advertises every valid
// compute+memory partition configuration as a virtual device and dynamically
// reconfigures the GPU hardware via amd-smi when a ResourceClaim is prepared.
// Requires Kubernetes 1.36+ with DRAPartitionableDevices, DRAConsumableCapacity,
// and DRADeviceTaints enabled.
//
// Drain the node before enabling or disabling this gate. Enabling it changes how
// GPUs are advertised (physical devices become synthetic partition devices), and
// allocations made under the previous mode are not tracked by the partition
// state. A claim prepared after the switch can therefore repartition a GPU that a
// pod from before the switch is still using, which resets that GPU mid-workload.
const AutoPartition featuregate.Feature = "AutoPartition"

// GPUSharing advertises every whole GPU as shareable (AllowMultipleAllocations)
// with consumable memory and computeUnits capacity, so several claims can each
// take a slice of one GPU. When a claim consumes less than the whole GPU, the
// driver pins it to a free set of CUs with HSA_CU_MASK and, if a vGPU hook is
// configured (--vgpu-hook-host-path), caps its VRAM with HIP_DEVICE_MEMORY_LIMIT_0
// enforced by that hook. Requires DRAConsumableCapacity in the cluster.
//
// Drain the node before toggling this gate: claims prepared under the other
// mode do not carry a CU assignment, so they are not counted when new slices
// are placed.
const GPUSharing featuregate.Feature = "GPUSharing"

var defaultFeatureGates = map[featuregate.Feature]featuregate.VersionedSpecs{
	DeviceMetadata: {
		{Default: false, PreRelease: featuregate.Alpha, Version: version.MajorMinor(0, 1)},
	},
	VFIOPassthrough: {
		{Default: false, PreRelease: featuregate.Alpha, Version: version.MajorMinor(0, 1)},
	},
	AutoPartition: {
		{Default: false, PreRelease: featuregate.Alpha, Version: version.MajorMinor(0, 1)},
	},
	GPUSharing: {
		{Default: false, PreRelease: featuregate.Alpha, Version: version.MajorMinor(0, 1)},
	},
}

var (
	once         sync.Once
	featureGates featuregate.MutableVersionedFeatureGate
)

// FeatureGates returns the process-wide driver feature-gate registry.
func FeatureGates() featuregate.MutableVersionedFeatureGate {
	once.Do(func() {
		fg := featuregate.NewVersionedFeatureGate(emulationVersion)
		if err := fg.AddVersioned(defaultFeatureGates); err != nil {
			panic(err) // programmer error: malformed static registry
		}
		featureGates = fg
	})
	return featureGates
}

// Enabled reports whether the named driver feature gate is enabled.
func Enabled(f featuregate.Feature) bool { return FeatureGates().Enabled(f) }

// KnownFeatures returns help strings for all known driver gates.
func KnownFeatures() []string { return FeatureGates().KnownFeatures() }

// ToMap returns the current enablement state of all driver gates.
func ToMap() map[string]bool {
	fg := FeatureGates()
	out := map[string]bool{}
	for f := range fg.GetAll() {
		out[string(f)] = fg.Enabled(f)
	}
	return out
}

// ValidateFeatureGates checks cross-gate constraints.
func ValidateFeatureGates() error {
	// AutoPartition replaces whole GPUs with synthetic partition devices, while
	// GPUSharing slices whole GPUs; the two describe the same hardware differently.
	if Enabled(AutoPartition) && Enabled(GPUSharing) {
		return fmt.Errorf("feature gates %s and %s cannot both be enabled", AutoPartition, GPUSharing)
	}
	return nil
}
