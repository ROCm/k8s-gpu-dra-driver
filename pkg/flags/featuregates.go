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

package flags

import (
	"fmt"
	"strconv"
	"strings"

	cli "github.com/urfave/cli/v2"

	"k8s.io/component-base/featuregate"

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/featuregates"
)

// gateMux implements pflag.Value, fronting multiple component-base registries
// behind a single --feature-gates flag. Each key=value is routed to the registry
// that owns the key. Unknown keys fail fast, matching component-base behavior.
type gateMux struct {
	registries []featuregate.MutableVersionedFeatureGate
}

// owner returns the registry that knows key, or nil if none do. GetAll includes
// GA/Deprecated gates (KnownFeatures hides them), so routing stays correct across
// maturity stages.
//
// Note: the built-in AllAlpha/AllBeta meta-gates are registered in every
// component-base registry, so they resolve to the FIRST registry only and are
// not fanned out to the others. Setting AllAlpha/AllBeta therefore affects just
// that first registry's gates. This is acceptable here (driver gates are set by
// name); revisit if broadcasting meta-gates across registries is ever needed.
func (m *gateMux) owner(key string) featuregate.MutableVersionedFeatureGate {
	for _, r := range m.registries {
		if _, ok := r.GetAll()[featuregate.Feature(key)]; ok {
			return r
		}
	}
	return nil
}

func (m *gateMux) Set(value string) error {
	perRegistry := map[featuregate.MutableVersionedFeatureGate]map[string]bool{}
	for _, pair := range strings.Split(value, ",") {
		if strings.TrimSpace(pair) == "" {
			continue
		}
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return fmt.Errorf("missing bool value for feature gate %q", strings.TrimSpace(kv[0]))
		}
		key := strings.TrimSpace(kv[0])
		b, err := strconv.ParseBool(strings.TrimSpace(kv[1]))
		if err != nil {
			return fmt.Errorf("invalid value for feature gate %s: %w", key, err)
		}
		owner := m.owner(key)
		if owner == nil {
			return fmt.Errorf("unrecognized feature gate: %s", key)
		}
		if perRegistry[owner] == nil {
			perRegistry[owner] = map[string]bool{}
		}
		perRegistry[owner][key] = b
	}
	for reg, keys := range perRegistry {
		if err := reg.SetFromMap(keys); err != nil {
			return err
		}
	}
	return nil
}

func (m *gateMux) String() string {
	parts := make([]string, 0, len(m.registries))
	for _, r := range m.registries {
		// String() is defined on the concrete *featureGate, not on the
		// MutableVersionedFeatureGate interface, so assert for it via fmt.Stringer.
		if s, ok := r.(fmt.Stringer); ok {
			if str := s.String(); str != "" {
				parts = append(parts, str)
			}
		}
	}
	return strings.Join(parts, ",")
}

func (m *gateMux) Type() string { return "mapStringBool" }

// newGateMux builds a mux fronting the logging and driver registries.
func newGateMux(logCfg *LoggingConfig) *gateMux {
	return &gateMux{registries: []featuregate.MutableVersionedFeatureGate{
		logCfg.FeatureGate(),
		featuregates.FeatureGates(),
	}}
}

// FeatureGateFlags returns the single --feature-gates flag (env FEATURE_GATES)
// backed by the logging and driver registries.
func FeatureGateFlags(logCfg *LoggingConfig) []cli.Flag {
	mux := newGateMux(logCfg)
	var known []string
	seen := map[string]struct{}{}
	for _, r := range mux.registries {
		for _, f := range r.KnownFeatures() {
			name := strings.SplitN(f, "=", 2)[0]
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			known = append(known, f)
		}
	}
	return []cli.Flag{
		&cli.GenericFlag{
			Name:     "feature-gates",
			Category: "Feature Gates:",
			Usage: "A set of key=value pairs that describe feature gates for alpha/experimental features. Options are:\n     " +
				strings.Join(known, "\n     "),
			Value:   mux,
			EnvVars: []string{"FEATURE_GATES"},
		},
	}
}
