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

package flags

import (
	"strings"
	"testing"

	cli "github.com/urfave/cli/v2"

	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/component-base/featuregate"
)

// buildTwoRegistries returns two independent registries: one owning "Alpha1",
// one owning "Beta1", to exercise routing without depending on real gates.
func buildTwoRegistries(t *testing.T) (featuregate.MutableVersionedFeatureGate, featuregate.MutableVersionedFeatureGate) {
	t.Helper()
	a := featuregate.NewVersionedFeatureGate(version.MajorMinor(0, 1))
	if err := a.AddVersioned(map[featuregate.Feature]featuregate.VersionedSpecs{
		"Alpha1": {{Default: false, PreRelease: featuregate.Alpha, Version: version.MajorMinor(0, 1)}},
	}); err != nil {
		t.Fatal(err)
	}
	b := featuregate.NewVersionedFeatureGate(version.MajorMinor(0, 1))
	if err := b.AddVersioned(map[featuregate.Feature]featuregate.VersionedSpecs{
		"Beta1": {{Default: false, PreRelease: featuregate.Alpha, Version: version.MajorMinor(0, 1)}},
	}); err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestMuxRoutesEachKeyToOwner(t *testing.T) {
	a, b := buildTwoRegistries(t)
	mux := &gateMux{registries: []featuregate.MutableVersionedFeatureGate{a, b}}
	if err := mux.Set("Alpha1=true,Beta1=true"); err != nil {
		t.Fatalf("Set error: %v", err)
	}
	if !a.Enabled("Alpha1") {
		t.Errorf("Alpha1 should be enabled in registry a")
	}
	if !b.Enabled("Beta1") {
		t.Errorf("Beta1 should be enabled in registry b")
	}
}

func TestMuxUnknownGateFailsFast(t *testing.T) {
	a, b := buildTwoRegistries(t)
	mux := &gateMux{registries: []featuregate.MutableVersionedFeatureGate{a, b}}
	if err := mux.Set("Nope=true"); err == nil {
		t.Fatalf("expected error for unknown gate")
	}
}

func TestMuxRejectsMalformed(t *testing.T) {
	a, b := buildTwoRegistries(t)
	mux := &gateMux{registries: []featuregate.MutableVersionedFeatureGate{a, b}}
	if err := mux.Set("Alpha1"); err == nil {
		t.Fatalf("expected error for missing '='")
	}
	if err := mux.Set("Alpha1=notabool"); err == nil {
		t.Fatalf("expected error for bad bool")
	}
}

func TestMuxToleratesEmptySegments(t *testing.T) {
	a, b := buildTwoRegistries(t)
	mux := &gateMux{registries: []featuregate.MutableVersionedFeatureGate{a, b}}
	if err := mux.Set("Alpha1=true,"); err != nil {
		t.Fatalf("trailing comma should be tolerated, got %v", err)
	}
	if !a.Enabled("Alpha1") {
		t.Errorf("Alpha1 should be enabled")
	}
}

func TestMuxStringReflectsEnabledGates(t *testing.T) {
	a, b := buildTwoRegistries(t)
	mux := &gateMux{registries: []featuregate.MutableVersionedFeatureGate{a, b}}
	if err := mux.Set("Alpha1=true"); err != nil {
		t.Fatalf("Set error: %v", err)
	}
	if got := mux.String(); !strings.Contains(got, "Alpha1=true") {
		t.Fatalf("String() should contain Alpha1=true, got %q", got)
	}
}

func TestFeatureGateFlagsSingleFlag(t *testing.T) {
	logCfg := NewLoggingConfig()
	flags := FeatureGateFlags(logCfg)
	if len(flags) != 1 {
		t.Fatalf("expected exactly one feature-gates flag, got %d", len(flags))
	}
	if flags[0].Names()[0] != "feature-gates" {
		t.Fatalf("expected flag name feature-gates, got %v", flags[0].Names())
	}
	gf, ok := flags[0].(*cli.GenericFlag)
	if !ok {
		t.Fatalf("expected *cli.GenericFlag, got %T", flags[0])
	}
	if len(gf.EnvVars) != 1 || gf.EnvVars[0] != "FEATURE_GATES" {
		t.Fatalf("expected EnvVars [FEATURE_GATES], got %v", gf.EnvVars)
	}
	if gf.Category != "Feature Gates:" {
		t.Fatalf("expected category 'Feature Gates:', got %q", gf.Category)
	}
}

func TestFeatureGateFlagsRouteLoggingAndReject(t *testing.T) {
	logCfg := NewLoggingConfig()
	mux := newGateMux(logCfg)
	// The driver ships no gates of its own yet, so only logging gates are known
	// through the shared flag. A logging gate routes to the logging registry.
	if err := mux.Set("ContextualLogging=false"); err != nil {
		t.Fatalf("logging gate should route: %v", err)
	}
	// A bogus gate is rejected by every registry, so the mux fails fast.
	if err := mux.Set("Bogus=true"); err == nil {
		t.Fatalf("bogus gate should fail")
	}
}
