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
	"strings"
	"testing"
)

func physical(addr, kfdID string) map[string]interface{} {
	return map[string]interface{}{"pciAddr": addr, "kfdID": kfdID}
}

// A partition may only inherit from exactly one physical parent. No match means the
// parent was not discovered, and two means the kfd id does not identify a GPU, so
// picking either would depend on map order.
func TestUniquePhysicalParent(t *testing.T) {
	one := map[string]map[string]interface{}{
		"0000:19:00.0": physical("0000:19:00.0", "0000:19:00:0"),
		"0000:1a:00.0": physical("0000:1a:00.0", "0000:1a:00:0"),
	}
	two := map[string]map[string]interface{}{
		"0000:19:00.0": physical("0000:19:00.0", "dup"),
		"0000:1a:00.0": physical("0000:1a:00.0", "dup"),
	}

	t.Run("exactly one", func(t *testing.T) {
		got, err := uniquePhysicalParent("0000:19:00:0", one)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got["pciAddr"] != "0000:19:00.0" {
			t.Errorf("parent = %v, want 0000:19:00.0", got["pciAddr"])
		}
	})

	t.Run("no parent", func(t *testing.T) {
		if _, err := uniquePhysicalParent("0000:ff:00:0", one); err == nil {
			t.Error("a partition with no physical parent must be an error")
		}
	})

	t.Run("ambiguous parent names both", func(t *testing.T) {
		_, err := uniquePhysicalParent("dup", two)
		if err == nil {
			t.Fatal("two GPUs with the same kfd id must be an error")
		}
		for _, addr := range []string{"0000:19:00.0", "0000:1a:00.0"} {
			if !strings.Contains(err.Error(), addr) {
				t.Errorf("error %q does not name %s", err, addr)
			}
		}
	})
}
