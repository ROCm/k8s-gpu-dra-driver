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

package main

import (
	"encoding/json"
	"testing"
)

// TestCheckpointPartitionFieldsOmittedWhenUnset guards the checkpoint upgrade
// path. VerifyChecksum re-marshals the decoded struct rather than hashing the
// bytes on disk, so a field that serializes while unset changes those bytes and
// makes a driver that does not know the field reject the checkpoint as
// corrupted. Every partition field is omitempty to keep the JSON identical when
// auto-partition is not in use.
func TestCheckpointPartitionFieldsOmittedWhenUnset(t *testing.T) {
	cp := newCheckpoint()

	out, err := json.Marshal(cp.V1)
	if err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{"activeMemoryMode", "gpuComputeModes", "memoryReload", "assignedSlots", "vfioConversions"} {
		var decoded map[string]interface{}
		if err := json.Unmarshal(out, &decoded); err != nil {
			t.Fatal(err)
		}
		if _, present := decoded[field]; present {
			t.Errorf("field %q serialized while unset; an older driver would reject the checkpoint as corrupted (got %s)", field, out)
		}
	}
}

// TestCheckpointRoundTripWithPartitionState verifies the partition fields, and
// in particular the per-share slot assignments, survive a write/read cycle with
// a valid checksum.
func TestCheckpointRoundTripWithPartitionState(t *testing.T) {
	cp := newCheckpoint()
	cp.V1.ActiveMemoryMode = "nps4"
	cp.V1.GPUComputeModes = map[int]string{0: "cpx"}
	cp.V1.AssignedSlots = map[int]map[string]int{
		0: {
			"claim-a/req0/gpu-0-cpx-nps4/": 0,
			"claim-a/req1/gpu-0-cpx-nps4/": 1,
		},
	}

	data, err := cp.MarshalCheckpoint()
	if err != nil {
		t.Fatal(err)
	}

	got := &Checkpoint{}
	if err := got.UnmarshalCheckpoint(data); err != nil {
		t.Fatal(err)
	}
	if err := got.VerifyChecksum(); err != nil {
		t.Fatalf("checksum verification failed on round trip: %v", err)
	}
	if got.V1.ActiveMemoryMode != "nps4" {
		t.Errorf("activeMemoryMode = %q, want nps4", got.V1.ActiveMemoryMode)
	}
	if len(got.V1.AssignedSlots[0]) != 2 {
		t.Fatalf("expected 2 recovered slots, got %d", len(got.V1.AssignedSlots[0]))
	}
	if got.V1.AssignedSlots[0]["claim-a/req1/gpu-0-cpx-nps4/"] != 1 {
		t.Error("slot assignment did not survive the round trip")
	}
}

// TestCheckpointRoundTripWithVfioConversions verifies GPU->VFIO conversion
// records survive a write/read cycle with a valid checksum.
func TestCheckpointRoundTripWithVfioConversions(t *testing.T) {
	cp := newCheckpoint()
	cp.V1.VfioConversions = map[string]map[string]*VfioConversionRecord{
		"claim-a": {"gpu-0-128": {PCIAddress: "0000:0d:00.0", IOMMUGroup: "42", CardIndex: 0, RenderIndex: 128}},
	}

	data, err := cp.MarshalCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	got := &Checkpoint{}
	if err := got.UnmarshalCheckpoint(data); err != nil {
		t.Fatal(err)
	}
	if err := got.VerifyChecksum(); err != nil {
		t.Fatalf("checksum verification failed on round trip: %v", err)
	}
	rec := got.V1.VfioConversions["claim-a"]["gpu-0-128"]
	if rec == nil || rec.PCIAddress != "0000:0d:00.0" || rec.RenderIndex != 128 || rec.IOMMUGroup != "42" {
		t.Fatalf("conversion record not preserved: %+v", rec)
	}
}
