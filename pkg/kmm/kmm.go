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

// Package kmm provides helpers for interacting with a Kernel Module Management
// (KMM) managed amdgpu driver. When the driver is KMM-managed, the amd-smi
// driver reload must not be used (it would restore the inbox driver instead of
// the KMM-provisioned one); the KMM operator reloads its own driver instead.
package kmm

import (
	"os"
	"strconv"
)

// EnvDriverEnabled is the environment variable that signals the amdgpu driver on
// this node is managed by KMM. It is set on the kubelet-plugin DaemonSet (via
// Helm, or by the GPU operator when it manages the deployment).
const EnvDriverEnabled = "KMM_DRIVER_ENABLED"

// IsDriverEnabled reports whether the amdgpu driver on this node is KMM-managed,
// based on the KMM_DRIVER_ENABLED environment variable. Any value parseable as a
// true boolean ("true", "1", etc.) enables it; unset or unparseable is false.
func IsDriverEnabled() bool {
	v, ok := os.LookupEnv(EnvDriverEnabled)
	if !ok {
		return false
	}
	enabled, err := strconv.ParseBool(v)
	if err != nil {
		return false
	}
	return enabled
}
