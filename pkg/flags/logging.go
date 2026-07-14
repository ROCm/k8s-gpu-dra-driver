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

	"github.com/spf13/pflag"
	cli "github.com/urfave/cli/v2"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/component-base/featuregate"
	logsapi "k8s.io/component-base/logs/api/v1"

	_ "k8s.io/component-base/logs/json/register" // for JSON log output support
)

type LoggingConfig struct {
	featureGate featuregate.MutableVersionedFeatureGate
	config      *logsapi.LoggingConfiguration
}

func NewLoggingConfig() *LoggingConfig {
	fg := featuregate.NewFeatureGate()
	l := &LoggingConfig{
		featureGate: fg,
		config:      logsapi.NewLoggingConfiguration(),
	}
	utilruntime.Must(logsapi.AddFeatureGates(l.featureGate))
	utilruntime.Must(l.featureGate.SetFromMap(map[string]bool{string(logsapi.ContextualLogging): true}))
	return l
}

// Apply should be called in a cli.App.Before directly after parsing command
// line flags and before running any code which emits log entries.
func (l *LoggingConfig) Apply() error {
	return logsapi.ValidateAndApply(l.config, l.featureGate)
}

// FeatureGate exposes the logging feature-gate registry so it can be fronted by
// the shared --feature-gates flag mux.
func (l *LoggingConfig) FeatureGate() featuregate.MutableVersionedFeatureGate {
	return l.featureGate
}

// Flags returns the flags for the configuration.
func (l *LoggingConfig) Flags() []cli.Flag {
	var fs pflag.FlagSet
	logsapi.AddFlags(l.config, &fs)

	var flags []cli.Flag
	fs.VisitAll(func(flag *pflag.Flag) {
		flags = append(flags, pflagToCLI(flag, "Logging:"))
	})
	return flags
}

func pflagToCLI(flag *pflag.Flag, category string) cli.Flag {
	return &cli.GenericFlag{
		Name:        flag.Name,
		Category:    category,
		Usage:       flag.Usage,
		Value:       flag.Value,
		Destination: flag.Value,
		EnvVars:     []string{strings.ToUpper(strings.ReplaceAll(flag.Name, "-", "_"))},
	}
}
