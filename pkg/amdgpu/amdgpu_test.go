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

import (
	"os"
	"path/filepath"
	"testing"
)

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

func TestGfxTargetName(t *testing.T) {
	tests := []struct {
		version int64
		want    string
	}{
		{90402, "gfx942"},
		{90010, "gfx90a"},
		{100300, "gfx1030"},
		{110501, "gfx1151"},
		{120001, "gfx1201"},
		{0, ""},
	}
	for _, tt := range tests {
		if got := GfxTargetName(tt.version); got != tt.want {
			t.Errorf("GfxTargetName(%d) = %q, want %q", tt.version, got, tt.want)
		}
	}
}

// The properties below are an integrated GPU's KFD node, as the kernel writes it,
// trimmed to the fields GetTopologyInfo reads; node 0 is the CPU.
func TestGetTopologyInfoReadsGfxTarget(t *testing.T) {
	root := t.TempDir()
	nodes := map[string]string{
		"0": "cpu_cores_count 32\nsimd_count 0\ngfx_target_version 0\nlocation_id 0\ndomain 0\ndrm_render_minor 0\n",
		"1": "cpu_cores_count 0\nsimd_count 80\nsimd_per_cu 2\ngfx_target_version 110501\nlocation_id 49664\ndomain 0\ndrm_render_minor 128\n",
	}
	for node, properties := range nodes {
		dir := filepath.Join(root, "topology", "nodes", node)
		if err := os.MkdirAll(filepath.Join(dir, "mem_banks", "0"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "properties"), []byte(properties), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "mem_banks", "0", "properties"), []byte("heap_type 1\nsize_in_bytes 67153870848\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	topology := GetTopologyInfo(root)

	if len(topology) != 1 {
		t.Fatalf("got %d GPU nodes, want 1: %v", len(topology), topology)
	}
	info := topology[128]
	if info == nil {
		t.Fatalf("no entry for renderD128: %v", topology)
	}
	if info.GfxTarget != "gfx1151" {
		t.Errorf("GfxTarget = %q, want gfx1151", info.GfxTarget)
	}
	if info.CUCount != 40 {
		t.Errorf("CUCount = %d, want 40", info.CUCount)
	}
}
