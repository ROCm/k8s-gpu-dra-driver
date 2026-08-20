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

// sequencedRead yields the next value for a path on each call, so a test can move a
// partition mode between the two reads that are meant to detect exactly that.
func sequencedRead(values map[string][]string) func(string) (string, error) {
	calls := make(map[string]int)
	return func(path string) (string, error) {
		seq := values[path]
		i := calls[path]
		calls[path]++
		if i >= len(seq) {
			i = len(seq) - 1
		}
		return seq[i], nil
	}
}

func TestReadStablePartitionPair(t *testing.T) {
	const computeFile, memoryFile = "compute", "memory"
	tests := []struct {
		name        string
		values      map[string][]string
		wantCompute string
		wantMemory  string
		wantErr     bool
	}{
		{
			name:        "held still",
			values:      map[string][]string{computeFile: {"spx", "spx"}, memoryFile: {"nps1", "nps1"}},
			wantCompute: "spx",
			wantMemory:  "nps1",
		},
		{
			// The MI300X case: setting NPS4 moves compute to CPX, so a pass that read
			// compute first would otherwise publish a whole GPU that is now partitioned.
			name:    "compute moved between the reads",
			values:  map[string][]string{computeFile: {"spx", "cpx"}, memoryFile: {"nps4", "nps4"}},
			wantErr: true,
		},
		{
			name:    "memory moved between the reads",
			values:  map[string][]string{computeFile: {"dpx", "dpx"}, memoryFile: {"nps1", "nps4"}},
			wantErr: true,
		},
		{
			name:   "device without partitioning reports neither mode",
			values: map[string][]string{computeFile: {"", ""}, memoryFile: {"", ""}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compute, memory, err := readStablePartitionPair(sequencedRead(tt.values), computeFile, memoryFile)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if compute != tt.wantCompute || memory != tt.wantMemory {
				t.Errorf("got (%q, %q), want (%q, %q)", compute, memory, tt.wantCompute, tt.wantMemory)
			}
		})
	}
}

// A device count cannot show that discovery has settled: a pass can lose one GPU and gain
// another, or return the same GPUs under changed mappings, with the count never moving.
func TestDiscoveryFingerprintSeparatesEqualCounts(t *testing.T) {
	device := func(pciAddr string, card, render int, kfdID, compute, memory string) map[string]map[string]interface{} {
		return map[string]map[string]interface{}{
			"gpu-0-128": {
				"pciAddr": pciAddr, "card": card, "renderD": render, "kfdID": kfdID, "nodeId": 1,
				"computePartitionType": compute, "memoryPartitionType": memory,
			},
		}
	}
	base := device("0000:19:00.0", 0, 128, "19000", "spx", "nps1")

	tests := []struct {
		name  string
		other map[string]map[string]interface{}
		same  bool
	}{
		{"identical pass", device("0000:19:00.0", 0, 128, "19000", "spx", "nps1"), true},
		{"another GPU at the same count", device("0000:1a:00.0", 0, 128, "19000", "spx", "nps1"), false},
		{"card and render remapped", device("0000:19:00.0", 1, 129, "19000", "spx", "nps1"), false},
		{"different KFD identity", device("0000:19:00.0", 0, 128, "1a000", "spx", "nps1"), false},
		{"repartitioned in place", device("0000:19:00.0", 0, 128, "19000", "cpx", "nps4"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := discoveryFingerprint(tt.other) == discoveryFingerprint(base); got != tt.same {
				t.Errorf("fingerprints equal = %v, want %v (both passes hold one device)", got, tt.same)
			}
		})
	}
}

// Map iteration order is randomised, so without sorting the fingerprint would differ from
// itself between two passes over an unchanged node and never converge.
func TestDiscoveryFingerprintIsOrderStable(t *testing.T) {
	devices := make(map[string]map[string]interface{})
	for _, name := range []string{"gpu-c", "gpu-a", "gpu-e", "gpu-b", "gpu-d"} {
		devices[name] = map[string]interface{}{"pciAddr": name, "card": 0, "renderD": 128}
	}

	first := discoveryFingerprint(devices)
	for i := 0; i < 100; i++ {
		if got := discoveryFingerprint(devices); got != first {
			t.Fatalf("fingerprint changed over an unchanged map:\n%s\n%s", first, got)
		}
	}
}
