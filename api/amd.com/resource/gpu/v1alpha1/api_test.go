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

package v1alpha1

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIOMMUBackendPolicy_Validate(t *testing.T) {
	assert.NoError(t, IOMMUBackendPolicyLegacyOnly.Validate())
	assert.NoError(t, IOMMUBackendPolicyPreferIommuFD.Validate())
	assert.Error(t, IOMMUBackendPolicy("InvalidPolicy").Validate())
	assert.Error(t, IOMMUBackendPolicy("").Validate())
}

func TestIOMMUConfig_ShouldPreferIommuFD(t *testing.T) {
	assert.True(t, (&IOMMUConfig{BackendPolicy: IOMMUBackendPolicyPreferIommuFD}).ShouldPreferIommuFD())
	assert.False(t, (&IOMMUConfig{BackendPolicy: IOMMUBackendPolicyLegacyOnly}).ShouldPreferIommuFD())
}

func TestIOMMUConfig_ShouldEnableAPIDevice(t *testing.T) {
	tr := true
	fa := false
	assert.True(t, (&IOMMUConfig{EnableAPIDevice: &tr}).ShouldEnableAPIDevice())
	assert.False(t, (&IOMMUConfig{EnableAPIDevice: &fa}).ShouldEnableAPIDevice())
	assert.False(t, (&IOMMUConfig{EnableAPIDevice: nil}).ShouldEnableAPIDevice())
}

func TestIOMMUConfig_Validate(t *testing.T) {
	assert.NoError(t, (&IOMMUConfig{BackendPolicy: IOMMUBackendPolicyLegacyOnly}).Validate())
	assert.NoError(t, (&IOMMUConfig{BackendPolicy: IOMMUBackendPolicyPreferIommuFD}).Validate())
	assert.Error(t, (&IOMMUConfig{BackendPolicy: "Bad"}).Validate())
}

func TestVfioDeviceConfig_Normalize_DefaultsIommu(t *testing.T) {
	c := &VfioDeviceConfig{}
	assert.NoError(t, c.Normalize())
	assert.NotNil(t, c.Iommu)
	assert.Equal(t, IOMMUBackendPolicyLegacyOnly, c.Iommu.BackendPolicy)
	assert.NotNil(t, c.Iommu.EnableAPIDevice)
	assert.False(t, *c.Iommu.EnableAPIDevice)
}

func TestVfioDeviceConfig_Normalize_EmptyPolicy(t *testing.T) {
	c := &VfioDeviceConfig{Iommu: &IOMMUConfig{}}
	assert.NoError(t, c.Normalize())
	assert.Equal(t, IOMMUBackendPolicyLegacyOnly, c.Iommu.BackendPolicy)
}

func TestVfioDeviceConfig_Normalize_NilEnableAPIDevice(t *testing.T) {
	c := &VfioDeviceConfig{Iommu: &IOMMUConfig{BackendPolicy: IOMMUBackendPolicyPreferIommuFD}}
	assert.NoError(t, c.Normalize())
	assert.NotNil(t, c.Iommu.EnableAPIDevice)
	assert.False(t, *c.Iommu.EnableAPIDevice)
}

func TestVfioDeviceConfig_Validate_WithIommu(t *testing.T) {
	c := &VfioDeviceConfig{Iommu: &IOMMUConfig{BackendPolicy: IOMMUBackendPolicyPreferIommuFD}}
	assert.NoError(t, c.Validate())

	c2 := &VfioDeviceConfig{Iommu: &IOMMUConfig{BackendPolicy: "Invalid"}}
	assert.Error(t, c2.Validate())
}

func TestVfioDeviceConfig_Validate_NilIommu(t *testing.T) {
	c := &VfioDeviceConfig{}
	assert.NoError(t, c.Validate())
}

func TestGpuConfigNormalize(t *testing.T) {
	tests := map[string]struct {
		gpuConfig   *GpuConfig
		expected    *GpuConfig
		expectedErr error
	}{
		"nil GpuConfig": {
			gpuConfig:   nil,
			expectedErr: errors.New("config is 'nil'"),
		},
		"empty GpuConfig": {
			gpuConfig: &GpuConfig{},
			expected:  &GpuConfig{},
		},
		"default GpuConfig is already normalized": {
			gpuConfig: DefaultGpuConfig(),
			expected:  DefaultGpuConfig(),
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := test.gpuConfig.Normalize()
			assert.Equal(t, test.expected, test.gpuConfig)
			assert.Equal(t, test.expectedErr, err)
		})
	}
}
