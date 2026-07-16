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

package amdgpu

import "testing"

func TestSemverDriverVersion(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"four components drops build metadata", "6.19.14.31400000", "6.19.14"},
		{"three components unchanged", "6.19.14", "6.19.14"},
		{"two components unchanged", "6.19", "6.19"},
		{"single component unchanged", "6", "6"},
		{"more than four components keeps first three", "6.19.14.3.1", "6.19.14"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SemverDriverVersion(tt.in); got != tt.want {
				t.Errorf("SemverDriverVersion(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
