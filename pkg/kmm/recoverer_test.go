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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// stubResourceInterface implements only Delete; every other method panics on
// the nil-embedded dynamic.NamespaceableResourceInterface if called, which is
// fine since TriggerReload only calls Delete.
type stubResourceInterface struct {
	dynamic.NamespaceableResourceInterface
	deleteFunc func(ctx context.Context, name string, opts metav1.DeleteOptions, subresources ...string) error
}

func (s stubResourceInterface) Delete(ctx context.Context, name string, opts metav1.DeleteOptions, subresources ...string) error {
	return s.deleteFunc(ctx, name, opts, subresources...)
}

// stubDynamicClient implements only Resource; TriggerReload never calls
// anything else on dynamic.Interface.
type stubDynamicClient struct {
	dynamic.Interface
	resource stubResourceInterface
}

func (s stubDynamicClient) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return s.resource
}

// TestTriggerReload_BoundsHangingDelete guards the fix for an unbounded
// NodeModulesConfig delete: previously the caller passed context.TODO(), so a
// hung API call could block TriggerReload (and the partition/DeviceState lock
// held by its caller) forever. TriggerReload must now return a deadline error
// on its own instead of hanging past triggerReloadTimeout.
func TestTriggerReload_BoundsHangingDelete(t *testing.T) {
	origUnload := unloadAmdgpuModule
	origTimeout := triggerReloadTimeout
	defer func() {
		unloadAmdgpuModule = origUnload
		triggerReloadTimeout = origTimeout
	}()

	// Stub out the actual hardware call so the test doesn't need a real
	// modprobe binary or root privileges.
	unloadAmdgpuModule = func(ctx context.Context) ([]byte, error) {
		return nil, nil
	}
	// Shrink the bound so the test doesn't take a full minute.
	triggerReloadTimeout = 200 * time.Millisecond

	hangingDelete := func(ctx context.Context, name string, opts metav1.DeleteOptions, subresources ...string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	client := stubDynamicClient{
		resource: stubResourceInterface{deleteFunc: hangingDelete},
	}

	r := NewRecoverer(client, "test-node")

	done := make(chan error, 1)
	go func() { done <- r.TriggerReload(context.Background()) }()

	select {
	case err := <-done:
		assert.Error(t, err, "TriggerReload must fail when the NodeModulesConfig delete hangs past the deadline")
	case <-time.After(2 * time.Second):
		t.Fatal("TriggerReload did not return within 2s of a hanging delete call; the context bound did not take effect")
	}
}

// TestTriggerReload_SucceedsOnNotFound guards the existing NotFound-is-success
// behavior across the context-bounding change.
func TestTriggerReload_SucceedsOnNotFound(t *testing.T) {
	origUnload := unloadAmdgpuModule
	defer func() { unloadAmdgpuModule = origUnload }()
	unloadAmdgpuModule = func(ctx context.Context) ([]byte, error) { return nil, nil }

	notFoundDelete := func(ctx context.Context, name string, opts metav1.DeleteOptions, subresources ...string) error {
		return apierrors.NewNotFound(schema.GroupResource{Group: "kmm.sigs.x-k8s.io", Resource: "nodemodulesconfigs"}, name)
	}
	client := stubDynamicClient{
		resource: stubResourceInterface{deleteFunc: notFoundDelete},
	}

	r := NewRecoverer(client, "test-node")
	assert.NoError(t, r.TriggerReload(context.Background()))
}
