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
	"strings"
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

// triggerReloadTimeout bounds the entire TriggerReload call: the modprobe unload
// plus the NodeModulesConfig delete. Without an overall bound, the delete (which
// runs after modprobeTimeout has already elapsed for its own step) inherited
// whatever deadline the caller's context happened to carry — often none — and
// could hang indefinitely holding the partition/DeviceState lock. It is a
// variable (like retryBackoff in pkg/amdsmi) so tests can shrink it.
var triggerReloadTimeout = 1 * time.Minute

// inboxReloadTimeout bounds the full unload+load cycle on the non-KMM path.
// Unlike the KMM path, nothing else brings the driver back, so this covers
// re-probing every GPU and is correspondingly longer.
const inboxReloadTimeout = 5 * time.Minute

// ReloadInboxDriver reloads the inbox amdgpu kernel module to apply a staged
// memory-partition change. It unloads and reloads the module in one bounded,
// synchronous call, and returns only once the module is back.
//
// This replaces amdsmi_gpu_driver_reload(), which ROCm 10.0 removed;
// amdsmi_set_gpu_memory_partition only stages the mode, so something still has to
// cycle the driver for it to take effect. device-config-manager made the same
// change for the same reason.
//
// It must NOT be used when the amdgpu driver is KMM-managed: reloading by hand
// would bring back the inbox driver instead of the KMM-provisioned one. Use
// Recoverer.TriggerReload on that path.
//
// The container must have the host module tree mounted at /lib/modules (the
// amdgpu .ko plus modules.dep for the running kernel) and the kmod binaries
// available; the kubelet plugin DaemonSet provides both.
func ReloadInboxDriver(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, inboxReloadTimeout)
	defer cancel()

	klog.Infof("Reloading inbox amdgpu driver to apply memory partition change")
	for _, args := range [][]string{{"-rv", "amdgpu"}, {"-v", "amdgpu"}} {
		out, err := exec.CommandContext(ctx, "modprobe", args...).CombinedOutput()
		if err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("timeout running 'modprobe %s' after %v", strings.Join(args, " "), inboxReloadTimeout)
			}
			return fmt.Errorf("'modprobe %s' failed: %v, output: %s", strings.Join(args, " "), err, string(out))
		}
	}
	klog.Infof("Inbox amdgpu driver reloaded")
	return nil
}

// unloadAmdgpuModule runs `modprobe -rv amdgpu`. It is a variable so tests can
// stub the actual hardware call while still exercising TriggerReload's own
// timeout/error handling around it.
var unloadAmdgpuModule = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "modprobe", "-rv", "amdgpu").CombinedOutput()
}

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

	// Bound the whole trigger operation (unload + API delete), not just the
	// modprobe step, so a hung API call can't block forever.
	ctx, cancel := context.WithTimeout(ctx, triggerReloadTimeout)
	defer cancel()

	// Step 1: unload the amdgpu module.
	mctx, mcancel := context.WithTimeout(ctx, modprobeTimeout)
	defer mcancel()
	if out, err := unloadAmdgpuModule(mctx); err != nil {
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
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("timeout deleting NodeModulesConfig %q after %v", r.nodeName, triggerReloadTimeout)
		}
		return fmt.Errorf("failed to delete NodeModulesConfig %q: %v", r.nodeName, err)
	}
	klog.Infof("KMM recovery: deleted NodeModulesConfig %q, KMM will reload the managed driver", r.nodeName)
	return nil
}
