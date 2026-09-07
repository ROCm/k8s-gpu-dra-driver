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

// Package amdsmi provides CGo bindings to libamd_smi for setting GPU
// compute and memory partition modes. It replaces the previous approach
// of shelling out to the amd-smi CLI binary.
package amdsmi

/*
#cgo CFLAGS: -I${SRCDIR}/../../third_party/amd_smi/include
#cgo LDFLAGS: -L${SRCDIR}/../../third_party/amd_smi/lib -lamd_smi
#include "amdsmi.h"
*/
import "C"
import (
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	klog "k8s.io/klog/v2"
)

const (
	// retryCount is the number of retry attempts for AMDSMI_STATUS_BUSY.
	retryCount = 3
	// retryBackoff is the delay between retries when GPU is busy.
	retryBackoff = 5 * time.Second

	// statusBusy maps to AMDSMI_STATUS_BUSY (30).
	statusBusy = 30
	// statusSettingUnavailable maps to AMDSMI_STATUS_SETTING_UNAVAILABLE (55).
	statusSettingUnavailable = 55
)

var (
	initMu   sync.Mutex
	initDone bool

	// gpuHandles maps the driver's GPU index to the AMD SMI processor handle for
	// that physical GPU. The mapping is resolved by PCI address at Init() time
	// rather than by enumeration position: the driver assigns GPU indices by
	// sorted PCI address, while AMD SMI enumerates by socket then processor, and
	// nothing guarantees the two orders agree. Indexing a positionally-ordered
	// handle slice with a PCI-ordered index would drive partition changes against
	// the wrong physical GPU, and would disagree with the sysfs fallback paths,
	// which address GPUs by PCI address.
	gpuHandles map[int]C.amdsmi_processor_handle
)

// Init initializes the AMD SMI library for GPU operations and binds each driver
// GPU index to its AMD SMI processor handle using gpuPCIAddresses (GPU index ->
// PCI address, e.g. "0000:19:00.0"), as produced by device discovery.
//
// It is idempotent: subsequent calls after the first successful init are no-ops.
func Init(gpuPCIAddresses map[int]string) error {
	initMu.Lock()
	defer initMu.Unlock()

	if initDone {
		return nil
	}

	ret := C.amdsmi_init(C.AMDSMI_INIT_AMD_GPUS)
	if ret != C.AMDSMI_STATUS_SUCCESS {
		return fmt.Errorf("amdsmi_init failed with status %d", int(ret))
	}

	// Enumerate and cache all GPU processor handles before any partitioning
	handles, err := enumerateGPUHandles()
	if err != nil {
		C.amdsmi_shut_down()
		return fmt.Errorf("failed to enumerate GPU handles: %v", err)
	}

	byIndex, err := mapHandlesByPCIAddress(handles, gpuPCIAddresses)
	if err != nil {
		C.amdsmi_shut_down()
		return err
	}
	gpuHandles = byIndex

	initDone = true
	klog.Infof("AMD SMI initialized successfully, bound %d of %d enumerated GPUs by PCI address",
		len(gpuHandles), len(handles))
	return nil
}

// mapHandlesByPCIAddress resolves each enumerated processor handle to its PCI
// address via amdsmi_get_gpu_device_bdf and keys it by the driver's GPU index.
//
// Every requested GPU index must resolve: a missing one means the driver could
// not identify the GPU it would later partition, so this fails rather than
// silently falling back to enumeration order.
func mapHandlesByPCIAddress(handles []C.amdsmi_processor_handle, gpuPCIAddresses map[int]string) (map[int]C.amdsmi_processor_handle, error) {
	handlesByAddr := make(map[string]C.amdsmi_processor_handle, len(handles))
	for i, handle := range handles {
		var bdf C.amdsmi_bdf_t
		if ret := C.amdsmi_get_gpu_device_bdf(handle, &bdf); ret != C.AMDSMI_STATUS_SUCCESS {
			klog.Warningf("amdsmi_get_gpu_device_bdf failed for enumerated GPU %d with status %d, skipping", i, int(ret))
			continue
		}
		addr := formatBDF(bdfToUint64(bdf))
		handlesByAddr[addr] = handle
		klog.V(2).Infof("AMD SMI enumerated GPU %d has PCI address %s", i, addr)
	}

	byIndex := make(map[int]C.amdsmi_processor_handle, len(gpuPCIAddresses))
	var missing []string
	for gpuIndex, pciAddr := range gpuPCIAddresses {
		handle, ok := handlesByAddr[strings.ToLower(pciAddr)]
		if !ok {
			missing = append(missing, fmt.Sprintf("GPU %d (%s)", gpuIndex, pciAddr))
			continue
		}
		byIndex[gpuIndex] = handle
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		enumerated := make([]string, 0, len(handlesByAddr))
		for addr := range handlesByAddr {
			enumerated = append(enumerated, addr)
		}
		sort.Strings(enumerated)
		return nil, fmt.Errorf("no AMD SMI handle found for %s; enumerated PCI addresses: %v",
			strings.Join(missing, ", "), enumerated)
	}

	return byIndex, nil
}

// bdfToUint64 reads amdsmi_bdf_t as the single 64-bit value its `as_uint` member
// aliases. cgo renders the union as an opaque [8]byte because its other members
// are bitfields, so the value is decoded from those bytes rather than accessed as
// a field. x86_64 and aarch64 are both little-endian.
func bdfToUint64(bdf C.amdsmi_bdf_t) uint64 {
	return binary.LittleEndian.Uint64(bdf[:])
}

// formatBDF renders the packed amdsmi_bdf_t value as a canonical Linux PCI
// address ("domain:bus:device.function", e.g. "0000:19:00.0") so it can be
// compared with the addresses discovery reads from sysfs.
//
// Bitfields are laid out from the least significant bit: function [0:3),
// device [3:8), bus [8:16), domain [16:64).
func formatBDF(raw uint64) string {
	function := raw & 0x7
	device := (raw >> 3) & 0x1f
	bus := (raw >> 8) & 0xff
	domain := raw >> 16
	return fmt.Sprintf("%04x:%02x:%02x.%d", domain, bus, device, function)
}

// Shutdown shuts down the AMD SMI library.
// It is idempotent: calls when not initialized are no-ops.
func Shutdown() {
	initMu.Lock()
	defer initMu.Unlock()

	if !initDone {
		return
	}

	C.amdsmi_shut_down()
	gpuHandles = nil
	initDone = false
	klog.Infof("AMD SMI shut down successfully")
}

// enumerateGPUHandles enumerates all GPU processor handles via the AMD SMI
// socket/processor hierarchy. Called once during Init() to cache handles
// before any partitioning changes GPU indices.
func enumerateGPUHandles() ([]C.amdsmi_processor_handle, error) {
	// Step 1: get socket count
	var socketCount C.uint32_t
	ret := C.amdsmi_get_socket_handles(&socketCount, nil)
	if ret != C.AMDSMI_STATUS_SUCCESS {
		return nil, fmt.Errorf("amdsmi_get_socket_handles (count) failed with status %d", int(ret))
	}

	if socketCount == 0 {
		return nil, fmt.Errorf("no AMD SMI sockets found")
	}

	// Step 2: get socket handles
	sockets := make([]C.amdsmi_socket_handle, socketCount)
	ret = C.amdsmi_get_socket_handles(&socketCount, &sockets[0])
	if ret != C.AMDSMI_STATUS_SUCCESS {
		return nil, fmt.Errorf("amdsmi_get_socket_handles failed with status %d", int(ret))
	}

	// Step 3: iterate sockets and processors to collect all GPU handles
	var handles []C.amdsmi_processor_handle
	for i := 0; i < int(socketCount); i++ {
		// Get processor count for this socket
		var procCount C.uint32_t
		ret = C.amdsmi_get_processor_handles(sockets[i], &procCount, nil)
		if ret != C.AMDSMI_STATUS_SUCCESS {
			klog.Warningf("amdsmi_get_processor_handles (count) failed for socket %d with status %d, skipping", i, int(ret))
			continue
		}

		if procCount == 0 {
			continue
		}

		// Get processor handles
		processors := make([]C.amdsmi_processor_handle, procCount)
		ret = C.amdsmi_get_processor_handles(sockets[i], &procCount, &processors[0])
		if ret != C.AMDSMI_STATUS_SUCCESS {
			klog.Warningf("amdsmi_get_processor_handles failed for socket %d with status %d, skipping", i, int(ret))
			continue
		}

		for j := 0; j < int(procCount); j++ {
			var procType C.processor_type_t
			ret = C.amdsmi_get_processor_type(processors[j], &procType)
			if ret != C.AMDSMI_STATUS_SUCCESS {
				klog.Warningf("amdsmi_get_processor_type failed for socket %d processor %d with status %d, skipping", i, j, int(ret))
				continue
			}

			if procType != C.AMDSMI_PROCESSOR_TYPE_AMD_GPU {
				continue
			}

			handles = append(handles, processors[j])
		}
	}

	return handles, nil
}

// getProcessorHandle returns the processor handle bound to the driver's GPU
// index. Handles are resolved by PCI address at Init() time, before any
// partitioning occurs, so the binding stays correct regardless of the order AMD
// SMI enumerated its processors in.
func getProcessorHandle(gpuIndex int) (C.amdsmi_processor_handle, error) {
	handle, ok := gpuHandles[gpuIndex]
	if !ok {
		return nil, fmt.Errorf("no AMD SMI handle bound for GPU index %d (have %d GPUs)", gpuIndex, len(gpuHandles))
	}
	return handle, nil
}

// computePartitionToC maps a lowercase compute partition mode string to the
// corresponding C enum value.
func computePartitionToC(mode string) (C.amdsmi_compute_partition_type_t, error) {
	switch strings.ToUpper(mode) {
	case "SPX":
		return C.AMDSMI_COMPUTE_PARTITION_SPX, nil
	case "DPX":
		return C.AMDSMI_COMPUTE_PARTITION_DPX, nil
	case "QPX":
		return C.AMDSMI_COMPUTE_PARTITION_QPX, nil
	case "CPX":
		return C.AMDSMI_COMPUTE_PARTITION_CPX, nil
	default:
		return C.AMDSMI_COMPUTE_PARTITION_SPX, fmt.Errorf("unknown compute partition mode: %q", mode)
	}
}

// memoryPartitionToC maps a lowercase memory partition mode string to the
// corresponding C enum value.
func memoryPartitionToC(mode string) (C.amdsmi_memory_partition_type_t, error) {
	switch strings.ToUpper(mode) {
	case "NPS1":
		return C.AMDSMI_MEMORY_PARTITION_NPS1, nil
	case "NPS2":
		return C.AMDSMI_MEMORY_PARTITION_NPS2, nil
	case "NPS4":
		return C.AMDSMI_MEMORY_PARTITION_NPS4, nil
	case "NPS8":
		return C.AMDSMI_MEMORY_PARTITION_NPS8, nil
	default:
		return C.AMDSMI_MEMORY_PARTITION_NPS1, fmt.Errorf("unknown memory partition mode: %q", mode)
	}
}

// SetComputePartition sets the compute partition mode on the GPU at the given
// index. The mode string should be one of: "spx", "dpx", "qpx", "cpx"
// (case-insensitive).
//
// On AMDSMI_STATUS_BUSY (30), retries up to 3 times with a 5-second backoff.
// On AMDSMI_STATUS_SETTING_UNAVAILABLE (55), returns an error immediately.
func SetComputePartition(gpuIndex int, mode string) error {
	handle, err := getProcessorHandle(gpuIndex)
	if err != nil {
		return fmt.Errorf("failed to get processor handle for GPU %d: %v", gpuIndex, err)
	}

	computeType, err := computePartitionToC(mode)
	if err != nil {
		return err
	}

	for attempt := 0; attempt <= retryCount; attempt++ {
		ret := C.amdsmi_set_gpu_compute_partition(handle, computeType)
		if ret == C.AMDSMI_STATUS_SUCCESS {
			klog.Infof("Successfully set compute partition mode %q on GPU %d", strings.ToUpper(mode), gpuIndex)
			return nil
		}

		retCode := int(ret)
		if retCode == statusSettingUnavailable {
			return fmt.Errorf("compute partition mode %q is not available on GPU %d (AMDSMI_STATUS_SETTING_UNAVAILABLE)", mode, gpuIndex)
		}

		if retCode == statusBusy && attempt < retryCount {
			klog.Warningf("GPU %d is busy (AMDSMI_STATUS_BUSY), retrying in %v (attempt %d/%d)",
				gpuIndex, retryBackoff, attempt+1, retryCount)
			time.Sleep(retryBackoff)
			continue
		}

		return fmt.Errorf("amdsmi_set_gpu_compute_partition failed on GPU %d with status %d", gpuIndex, retCode)
	}

	return fmt.Errorf("amdsmi_set_gpu_compute_partition failed on GPU %d after %d retries (GPU busy)", gpuIndex, retryCount)
}

// SetMemoryPartition sets the memory partition mode on the GPU at the given
// index. The mode string should be one of: "nps1", "nps2", "nps4", "nps8"
// (case-insensitive).
//
// Since ROCm 7.0.0, amdsmi_set_gpu_memory_partition no longer reloads the driver
// automatically; the caller must invoke ReloadDriver once after staging the mode
// on all GPUs. Reload is kept separate so callers can skip it when the amdgpu
// driver is KMM-managed (a manual reload would restore the inbox driver).
//
// On AMDSMI_STATUS_BUSY (30), retries up to 3 times with a 5-second backoff.
// On AMDSMI_STATUS_SETTING_UNAVAILABLE (55), returns an error immediately.
func SetMemoryPartition(gpuIndex int, mode string) error {
	handle, err := getProcessorHandle(gpuIndex)
	if err != nil {
		return fmt.Errorf("failed to get processor handle for GPU %d: %v", gpuIndex, err)
	}

	memType, err := memoryPartitionToC(mode)
	if err != nil {
		return err
	}

	for attempt := 0; attempt <= retryCount; attempt++ {
		ret := C.amdsmi_set_gpu_memory_partition(handle, memType)
		if ret == C.AMDSMI_STATUS_SUCCESS {
			klog.Infof("Successfully staged memory partition mode %q on GPU %d", strings.ToUpper(mode), gpuIndex)
			return nil
		}

		retCode := int(ret)
		if retCode == statusSettingUnavailable {
			return fmt.Errorf("memory partition mode %q is not available on GPU %d (AMDSMI_STATUS_SETTING_UNAVAILABLE)", mode, gpuIndex)
		}

		if retCode == statusBusy && attempt < retryCount {
			klog.Warningf("GPU %d is busy (AMDSMI_STATUS_BUSY), retrying in %v (attempt %d/%d)",
				gpuIndex, retryBackoff, attempt+1, retryCount)
			time.Sleep(retryBackoff)
			continue
		}

		return fmt.Errorf("amdsmi_set_gpu_memory_partition failed on GPU %d with status %d", gpuIndex, retCode)
	}

	return fmt.Errorf("amdsmi_set_gpu_memory_partition failed on GPU %d after %d retries (GPU busy)", gpuIndex, retryCount)
}

// ReloadDriver reloads the amdgpu kernel driver via amd-smi. This is required
// once after changing the memory partition mode on ROCm 7.0.0+, since
// amdsmi_set_gpu_memory_partition no longer reloads automatically.
//
// It must NOT be called when the amdgpu driver is KMM-managed: a manual reload
// would bring back the inbox driver instead of the KMM-provisioned one. The
// caller is responsible for that gating.
func ReloadDriver() error {
	klog.Infof("Reloading amdgpu driver after memory partition change")

	// Call amdsmi_gpu_driver_reload() directly on the existing session. This
	// unloads and reloads the amdgpu kernel module, so the container MUST have the
	// host module tree mounted at /lib/modules (the amdgpu .ko + modules.dep for the
	// running kernel) — without it the reload fails with
	// AMDSMI_STATUS_AMDGPU_RESTART_ERR (54). The kubelet plugin DaemonSet mounts
	// /lib/modules for exactly this reason. An active session is required
	// (shutting it down first yields AMDSMI_STATUS_NOT_INIT, 32), so we do NOT
	// release the session around the call.
	ret := C.amdsmi_gpu_driver_reload()
	if ret != C.AMDSMI_STATUS_SUCCESS {
		return fmt.Errorf("amdsmi_gpu_driver_reload failed with status %d", int(ret))
	}
	klog.Infof("Driver reloaded successfully")
	return nil
}
