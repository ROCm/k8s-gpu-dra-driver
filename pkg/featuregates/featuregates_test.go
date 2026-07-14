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
	"testing"

	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/component-base/featuregate"
)

// exampleFeature is a synthetic gate used to exercise the registry machinery
// against a fresh registry, since the shipped registry is empty.
const exampleFeature featuregate.Feature = "ExampleFeature"

// newTestGates builds a standalone registry (not the singleton) seeded with a
// single Alpha exampleFeature, so tests can verify set/enable behavior.
func newTestGates(t *testing.T) featuregate.MutableVersionedFeatureGate {
	t.Helper()
	fg := featuregate.NewVersionedFeatureGate(emulationVersion)
	if err := fg.AddVersioned(map[featuregate.Feature]featuregate.VersionedSpecs{
		exampleFeature: {{Default: false, PreRelease: featuregate.Alpha, Version: version.MajorMinor(0, 1)}},
	}); err != nil {
		t.Fatal(err)
	}
	return fg
}

func TestDeviceMetadataGateDefaultsOff(t *testing.T) {
	fg := FeatureGates()
	if fg.Enabled(DeviceMetadata) {
		t.Fatalf("DeviceMetadata should default to false (Alpha)")
	}
}

func TestFeatureGatesSingletonNonNil(t *testing.T) {
	if FeatureGates() == nil {
		t.Fatalf("FeatureGates() should return a non-nil registry even with no driver gates")
	}
}

func TestValidateFeatureGatesNoop(t *testing.T) {
	if err := ValidateFeatureGates(); err != nil {
		t.Fatalf("ValidateFeatureGates() should return nil, got %v", err)
	}
}

func TestSetEnablesGate(t *testing.T) {
	fg := newTestGates(t)
	if fg.Enabled(exampleFeature) {
		t.Fatalf("exampleFeature should default to false")
	}
	if err := fg.Set("ExampleFeature=true"); err != nil {
		t.Fatalf("Set returned error: %v", err)
	}
	if !fg.Enabled(exampleFeature) {
		t.Fatalf("exampleFeature should be enabled after Set")
	}
}

func TestSetUnknownGateFails(t *testing.T) {
	fg := newTestGates(t)
	if err := fg.Set("Bogus=true"); err == nil {
		t.Fatalf("Set should reject unknown gate")
	}
}

// TestSetGateNotYetAvailableFails verifies that a gate whose introduction
// version is newer than the registry's emulation version resolves to PreAlpha
// and cannot be set. This is the version-boundary behavior our driver-only
// registry relies on.
func TestSetGateNotYetAvailableFails(t *testing.T) {
	fg := featuregate.NewVersionedFeatureGate(emulationVersion)
	const future featuregate.Feature = "FutureFeature"
	if err := fg.AddVersioned(map[featuregate.Feature]featuregate.VersionedSpecs{
		future: {{Default: false, PreRelease: featuregate.Alpha, Version: version.MajorMinor(9, 9)}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := fg.Set("FutureFeature=true"); err == nil {
		t.Fatalf("setting a gate newer than the emulation version should fail")
	}
}

// TestSetGraduatedGaGateIsLocked verifies that a gate graduated to GA is locked
// to its default: flipping it to the non-default value fails, while setting it
// to its default value succeeds.
func TestSetGraduatedGaGateIsLocked(t *testing.T) {
	fg := featuregate.NewVersionedFeatureGate(emulationVersion)
	const graduated featuregate.Feature = "GraduatedFeature"
	if err := fg.AddVersioned(map[featuregate.Feature]featuregate.VersionedSpecs{
		graduated: {{Default: true, PreRelease: featuregate.GA, LockToDefault: true, Version: version.MajorMinor(0, 1)}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := fg.Set("GraduatedFeature=false"); err == nil {
		t.Fatalf("flipping a GA locked-to-default gate to the non-default value should fail")
	}
	if err := fg.Set("GraduatedFeature=true"); err != nil {
		t.Fatalf("setting a GA gate to its default value should succeed, got %v", err)
	}
}
