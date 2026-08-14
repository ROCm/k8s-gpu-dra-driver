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

package kmm

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	klog "k8s.io/klog/v2"
)

// nodeModulesConfigGVR is the KMM NodeModulesConfig custom resource. It is
// cluster-scoped and named after the node. Deleting it makes the KMM operator
// re-provision (and thus reload) its managed amdgpu driver on that node.
var nodeModulesConfigGVR = schema.GroupVersionResource{
	Group:    "kmm.sigs.x-k8s.io",
	Version:  "v1beta1",
	Resource: "nodemodulesconfigs",
}

// modprobeTimeout bounds the `modprobe -rv amdgpu` unload step. The KMM operator
// handles the subsequent (slow) reload out of band; this only covers the unload.
const modprobeTimeout = 30 * time.Second

// Recoverer triggers a KMM-managed amdgpu driver reload on the local node. It is
// used instead of the amd-smi driver reload, which on a KMM node would restore
// the inbox driver rather than the KMM-provisioned one.
type Recoverer struct {
	dynClient dynamic.Interface
	nodeName  string
}

// NewRecoverer builds a Recoverer for the given node using the provided dynamic
// client (used to delete the node's NodeModulesConfig CR).
func NewRecoverer(dynClient dynamic.Interface, nodeName string) *Recoverer {
	return &Recoverer{dynClient: dynClient, nodeName: nodeName}
}

// TriggerReload initiates a KMM-managed driver reload: it unloads the amdgpu
// module (`modprobe -rv amdgpu`) and deletes the node's NodeModulesConfig CR so
// the KMM operator re-provisions and reloads the managed driver.
//
// It returns once the reload has been *triggered*; it does NOT wait for the KMM
// operator to finish (that can take minutes). The caller polls sysfs for
// convergence separately. A NotFound on the CR delete is treated as success
// (KMM will recreate it), so the call is safe to retry.
func (r *Recoverer) TriggerReload(ctx context.Context) error {
	if r == nil {
		return fmt.Errorf("kmm recoverer is nil")
	}
	if r.nodeName == "" {
		return fmt.Errorf("kmm recoverer has empty node name")
	}

	// Step 1: unload the amdgpu module.
	mctx, cancel := context.WithTimeout(ctx, modprobeTimeout)
	defer cancel()
	cmd := exec.CommandContext(mctx, "modprobe", "-rv", "amdgpu")
	if out, err := cmd.CombinedOutput(); err != nil {
		if mctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("timeout running 'modprobe -rv amdgpu'")
		}
		return fmt.Errorf("'modprobe -rv amdgpu' failed: %v, output: %s", err, string(out))
	}
	klog.Infof("KMM recovery: unloaded amdgpu module via modprobe -rv")

	// Step 2: delete the node's NodeModulesConfig so KMM re-provisions the driver.
	if r.dynClient == nil {
		return fmt.Errorf("kmm recoverer has no dynamic client")
	}
	err := r.dynClient.Resource(nodeModulesConfigGVR).Delete(ctx, r.nodeName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete NodeModulesConfig %q: %v", r.nodeName, err)
	}
	klog.Infof("KMM recovery: deleted NodeModulesConfig %q, KMM will reload the managed driver", r.nodeName)
	return nil
}
