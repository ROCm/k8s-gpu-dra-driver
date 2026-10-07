/*
Copyright (c) Advanced Micro Devices, Inc. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
)

// The common CDI spec must carry the node name that the driver parsed into its
// config flag, not whatever NODE_NAME happens to be in the process environment,
// so it stays consistent with the kubeletplugin and the ResourceSlice pool. The
// test drives NewCDIHandler so it guards the config -> handler -> spec wiring, and
// sets a conflicting NODE_NAME to prove the environment no longer wins.
func TestCreateCommonSpecFileUsesConfiguredNodeName(t *testing.T) {
	t.Setenv("NODE_NAME", "node-from-environment")

	cdiRoot := t.TempDir()
	config := &Config{
		flags: &Flags{
			cdiRoot:  cdiRoot,
			nodeName: "node-from-config",
		},
	}

	handler, err := NewCDIHandler(config)
	require.NoError(t, err)
	require.NoError(t, handler.CreateCommonSpecFile())

	entries, err := os.ReadDir(cdiRoot)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	spec, err := cdiapi.ReadSpec(filepath.Join(cdiRoot, entries[0].Name()), 0)
	require.NoError(t, err)
	require.Len(t, spec.Devices, 1)

	require.Contains(t, spec.Devices[0].ContainerEdits.Env, "KUBERNETES_NODE_NAME=node-from-config")
	require.NotContains(t, spec.Devices[0].ContainerEdits.Env, "KUBERNETES_NODE_NAME=node-from-environment")
}
