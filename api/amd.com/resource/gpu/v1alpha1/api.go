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

package v1alpha1

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/utils/ptr"
)

const (
	GroupName = "gpu.resource.amd.com"
	Version   = "v1alpha1"

	GpuConfigKind        = "GpuConfig"
	VfioDeviceConfigKind = "VfioDeviceConfig"
)

// Decoder implements a decoder for objects in this API group.
var Decoder runtime.Decoder

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// GpuConfig holds the set of parameters for configuring a GPU.
type GpuConfig struct {
	metav1.TypeMeta `json:",inline"`
}

// DefaultGpuConfig provides the default GPU configuration.
func DefaultGpuConfig() *GpuConfig {
	return &GpuConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: GroupName + "/" + Version,
			Kind:       GpuConfigKind,
		},
	}
}

// Normalize updates a GpuConfig config with implied default values based on other settings.
func (c *GpuConfig) Normalize() error {
	if c == nil {
		return fmt.Errorf("config is 'nil'")
	}
	return nil
}

// IOMMUBackendPolicy encodes the IOMMU backend selection policy.
type IOMMUBackendPolicy string

const (
	IOMMUBackendPolicyLegacyOnly    IOMMUBackendPolicy = "LegacyOnly"
	IOMMUBackendPolicyPreferIommuFD IOMMUBackendPolicy = "PreferIommuFD"
)

// Validate ensures that IOMMUBackendPolicy has a valid value.
func (p IOMMUBackendPolicy) Validate() error {
	switch p {
	case IOMMUBackendPolicyLegacyOnly, IOMMUBackendPolicyPreferIommuFD:
		return nil
	default:
		return fmt.Errorf("unknown IOMMU backend policy: %v", p)
	}
}

// IOMMUConfig holds parameters for configuring the IOMMU backend for VFIO devices.
type IOMMUConfig struct {
	BackendPolicy   IOMMUBackendPolicy `json:"backendPolicy"`
	EnableAPIDevice *bool              `json:"enableAPIDevice,omitempty"`
}

// ShouldPreferIommuFD returns true if the IOMMU backend policy is PreferIommuFD.
func (c *IOMMUConfig) ShouldPreferIommuFD() bool {
	return c.BackendPolicy == IOMMUBackendPolicyPreferIommuFD
}

// ShouldEnableAPIDevice returns true if the IOMMU API device should be
// made available to the workload.
func (c *IOMMUConfig) ShouldEnableAPIDevice() bool {
	return c.EnableAPIDevice != nil && *c.EnableAPIDevice
}

// Validate ensures that IOMMUConfig has a valid set of values.
func (c *IOMMUConfig) Validate() error {
	return c.BackendPolicy.Validate()
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// VfioDeviceConfig holds configuration for VFIO passthrough devices.
type VfioDeviceConfig struct {
	metav1.TypeMeta `json:",inline"`
	Iommu           *IOMMUConfig `json:"iommu,omitempty"`
}

// DefaultVfioDeviceConfig provides the default VFIO configuration.
func DefaultVfioDeviceConfig() *VfioDeviceConfig {
	return &VfioDeviceConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: GroupName + "/" + Version,
			Kind:       VfioDeviceConfigKind,
		},
		Iommu: &IOMMUConfig{
			BackendPolicy:   IOMMUBackendPolicyLegacyOnly,
			EnableAPIDevice: ptr.To(false),
		},
	}
}

// Normalize updates a VfioDeviceConfig with implied default values.
func (c *VfioDeviceConfig) Normalize() error {
	if c == nil {
		return fmt.Errorf("config is 'nil'")
	}
	if c.Iommu == nil {
		c.Iommu = &IOMMUConfig{
			BackendPolicy:   IOMMUBackendPolicyLegacyOnly,
			EnableAPIDevice: ptr.To(false),
		}
		return nil
	}
	if c.Iommu.BackendPolicy == "" {
		c.Iommu.BackendPolicy = IOMMUBackendPolicyLegacyOnly
	}
	if c.Iommu.EnableAPIDevice == nil {
		c.Iommu.EnableAPIDevice = ptr.To(false)
	}
	return nil
}

// Validate checks a VfioDeviceConfig for invalid settings.
func (c *VfioDeviceConfig) Validate() error {
	if c.Iommu == nil {
		return nil
	}
	return c.Iommu.Validate()
}

func init() {
	// Create a new scheme and add our types to it. If at some point in the
	// future a new version of the configuration API becomes necessary, then
	// conversion functions can be generated and registered to continue
	// supporting older versions.
	scheme := runtime.NewScheme()
	schemeGroupVersion := schema.GroupVersion{
		Group:   GroupName,
		Version: Version,
	}
	scheme.AddKnownTypes(schemeGroupVersion,
		&GpuConfig{},
		&VfioDeviceConfig{},
	)
	metav1.AddToGroupVersion(scheme, schemeGroupVersion)

	// Set up a json serializer to decode our types.
	Decoder = json.NewSerializerWithOptions(
		json.DefaultMetaFactory,
		scheme,
		scheme,
		json.SerializerOptions{
			Pretty: true, Strict: true,
		},
	)
}
