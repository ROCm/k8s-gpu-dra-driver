/*
 * Copyright 2025 The Kubernetes Authors.
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

func TestDriverRegistryShipsNoGates(t *testing.T) {
	if len(defaultFeatureGates) != 0 {
		t.Fatalf("driver ships the feature-gate machinery only; defaultFeatureGates should be empty, got %v", defaultFeatureGates)
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
