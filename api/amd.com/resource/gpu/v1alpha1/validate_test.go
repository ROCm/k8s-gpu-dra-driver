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

func TestGpuConfigValidate(t *testing.T) {
	tests := map[string]struct {
		gpuConfig *GpuConfig
		expected  error
	}{
		"empty GpuConfig": {
			gpuConfig: &GpuConfig{},
			expected:  errors.New("no sharing strategy set"),
		},
		"default GpuConfig": {
			gpuConfig: DefaultGpuConfig(),
			expected:  nil,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := test.gpuConfig.Validate()
			assert.Equal(t, test.expected, err)
		})
	}
}
