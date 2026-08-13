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

var defaultFeatureGates = map[featuregate.Feature]featuregate.VersionedSpecs{
	DeviceMetadata: {
		{Default: false, PreRelease: featuregate.Alpha, Version: version.MajorMinor(0, 1)},
	},
	VFIOPassthrough: {
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

// ValidateFeatureGates checks cross-gate constraints. No constraints exist yet;
// this is a stub kept in place so callers and wiring are ready for future gates.
func ValidateFeatureGates() error { return nil }
