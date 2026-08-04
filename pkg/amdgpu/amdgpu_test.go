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

func TestResolveDRMIdentity(t *testing.T) {
	topo := map[int]*TopologyInfo{
		128: {UniqueID: "gpu-A", NodeID: 2},
		129: {UniqueID: "gpu-B", NodeID: 3},
	}
	tests := []struct {
		name                           string
		devPaths                       []string
		wantCard, wantRender, wantNode int
		wantDevID                      string
		wantOK                         bool
	}{
		{"complete device", []string{"/d/card0", "/d/renderD128"}, 0, 128, 2, "gpu-A", true},
		{"another complete device", []string{"/d/card1", "/d/renderD129"}, 1, 129, 3, "gpu-B", true},
		{"renderD without topology is still valid", []string{"/d/card4", "/d/renderD200"}, 4, 200, 0, "", true},
		// The rest resolve no complete identity, so ok is false and the caller
		// skips them instead of publishing a borrowed or default device. Each runs
		// after the complete cases above to show no state carries over.
		{"no drm entries", nil, 0, 0, 0, "", false},
		{"card without renderD", []string{"/d/card7"}, 7, 0, 0, "", false},
		{"renderD without card", []string{"/d/renderD128"}, 0, 128, 2, "gpu-A", false},
		{"malformed card suffix", []string{"/d/cardfoo", "/d/renderD128"}, 0, 128, 2, "gpu-A", false},
		{"conflicting card entries", []string{"/d/card0", "/d/card1", "/d/renderD128"}, 1, 128, 2, "gpu-A", false},
		{"empty renderD suffix", []string{"/d/card1", "/d/renderD"}, 1, 0, 0, "", false},
		{"short unrelated entry", []string{"/d/x"}, 0, 0, 0, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			card, renderD, nodeId, devID, ok := resolveDRMIdentity(tt.devPaths, topo)
			if card != tt.wantCard || renderD != tt.wantRender || nodeId != tt.wantNode || devID != tt.wantDevID || ok != tt.wantOK {
				t.Fatalf("got (card=%d, renderD=%d, node=%d, devID=%q, ok=%v), want (card=%d, renderD=%d, node=%d, devID=%q, ok=%v)",
					card, renderD, nodeId, devID, ok, tt.wantCard, tt.wantRender, tt.wantNode, tt.wantDevID, tt.wantOK)
			}
		})
	}
}
