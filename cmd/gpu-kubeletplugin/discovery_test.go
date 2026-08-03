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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetMemoryBytes(t *testing.T) {
	withVram := map[string]interface{}{"vramBytes": uint64(16 * 1024 * 1024 * 1024)}
	assert.Equal(t, uint64(16*1024*1024*1024), getMemoryBytes(withVram, "device", "0000:00:00.0"))

	// Unreadable VRAM reports 0 instead of a fabricated capacity.
	assert.Equal(t, uint64(0), getMemoryBytes(map[string]interface{}{}, "device", "0000:00:00.0"))
	assert.Equal(t, uint64(0), getMemoryBytes(map[string]interface{}{"vramBytes": uint64(0)}, "partition", "0000:00:00.0"))
}
