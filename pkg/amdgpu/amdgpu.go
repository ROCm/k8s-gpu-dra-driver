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

// Package amdgpu is a collection of utility functions to access various properties
// of AMD GPU via Linux kernel interfaces like sysfs and ioctl (using libdrm.)
package amdgpu

// #cgo pkg-config: libdrm libdrm_amdgpu
// #include <stdint.h>
// #include <xf86drm.h>
// #include <drm.h>
// #include <amdgpu.h>
// #include <amdgpu_drm.h>
import "C"
import (
	"bufio"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/golang/glog"
)

// FamilyID to String convert AMDGPU_FAMILY_* into string
// AMDGPU_FAMILY_* as defined in https://github.com/torvalds/linux/blob/master/include/uapi/drm/amdgpu_drm.h#L986
func FamilyIDtoString(familyId uint32) (string, error) {
	switch familyId {
	case C.AMDGPU_FAMILY_SI:
		return "SI", nil
	case C.AMDGPU_FAMILY_CI:
		return "CI", nil
	case C.AMDGPU_FAMILY_KV:
		return "KV", nil
	case C.AMDGPU_FAMILY_VI:
		return "VI", nil
	case C.AMDGPU_FAMILY_CZ:
		return "CZ", nil
	case C.AMDGPU_FAMILY_AI:
		return "AI", nil
	case C.AMDGPU_FAMILY_RV:
		return "RV", nil
	case C.AMDGPU_FAMILY_NV:
		return "NV", nil
	default:
		ret := ""
		err := fmt.Errorf("Unknown Family ID: %d", familyId)
		return ret, err
	}

}

func GetCardFamilyName(cardName string) (string, error) {
	devHandle, err := openAMDGPU(cardName)
	if err != nil {
		return "", err
	}
	defer C.amdgpu_device_deinitialize(devHandle)

	var info C.struct_amdgpu_gpu_info
	rc := C.amdgpu_query_gpu_info(devHandle, &info)

	if rc < 0 {
		return "", fmt.Errorf("Fail to get FamilyID %s: %d", cardName, rc)
	}

	return FamilyIDtoString(uint32(info.family_id))
}

// GetDriverVersion reads the AMDGPU driver version and source version
func GetDriverVersion() (string, string) {
	// Find all available cards to read driver version from
	matches, _ := filepath.Glob("/sys/class/drm/card*/device/driver/module/version")
	if len(matches) == 0 {
		glog.Warningf("No AMD GPU cards found for driver version reading")
		return "", ""
	}

	for _, versionPath := range matches {
		b, err := ioutil.ReadFile(versionPath)
		if err != nil {
			continue
		}
		driverVersion := strings.TrimSpace(string(b))

		srcVersionPath := strings.Replace(versionPath, "/version", "/srcversion", 1)
		b, err = ioutil.ReadFile(srcVersionPath)
		if err != nil {
			continue
		}
		driverSrcVersion := strings.TrimSpace(string(b))

		// Return as soon as we find a valid driver version and src version
		if driverVersion != "" && driverSrcVersion != "" {
			return driverVersion, driverSrcVersion
		}
	}

	glog.Warningf("Failed to read AMDGPU driver version or src version from any card")
	return "", ""
}

// GetAMDGPUs return a map of AMD GPU on a node identified by the part of the pci address
func GetAMDGPUs() map[string]map[string]interface{} {
	if _, err := os.Stat("/sys/module/amdgpu/drivers/"); err != nil {
		glog.Warningf("amdgpu driver unavailable: %s", err)
		return make(map[string]map[string]interface{})
	}

	//ex: /sys/module/amdgpu/drivers/pci:amdgpu/0000:19:00.0
	matches, _ := filepath.Glob("/sys/module/amdgpu/drivers/pci:amdgpu/[0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F]:*")

	devID := ""
	devices := make(map[string]map[string]interface{})
	card, renderD, nodeId := 0, 128, 0

	// Get comprehensive topology information once instead of multiple calls
	topologyInfo := GetTopologyInfo()

	// Get driver version once for all devices
	globalDriverVersion, globalDriverSrcVersion := GetDriverVersion()

	for _, path := range matches {
		computePartitionFile := filepath.Join(path, "current_compute_partition")
		memoryPartitionFile := filepath.Join(path, "current_memory_partition")
		numaNodeFile := filepath.Join(path, "numa_node")

		computePartitionType, memoryPartitionType := "", ""
		numaNode := -1

		// Read the compute partition
		if data, err := ioutil.ReadFile(computePartitionFile); err == nil {
			computePartitionType = strings.ToLower(strings.TrimSpace(string(data)))
		} else {
			glog.Warningf("Failed to read 'current_compute_partition' file at %s: %s", computePartitionFile, err)
		}

		// Read the memory partition
		if data, err := ioutil.ReadFile(memoryPartitionFile); err == nil {
			memoryPartitionType = strings.ToLower(strings.TrimSpace(string(data)))
		} else {
			glog.Warningf("Failed to read 'current_memory_partition' file at %s: %s", memoryPartitionFile, err)
		}

		if data, err := ioutil.ReadFile(numaNodeFile); err == nil {
			numaNodeStr := strings.TrimSpace(string(data))
			numaNode, err = strconv.Atoi(numaNodeStr)
			if err != nil {
				glog.Warningf("Failed to convert 'numa_node' value to int: %s", err)
				continue
			}
		} else {
			glog.Warningf("Failed to read 'numa_node' file at %s: %s", numaNodeFile, err)
			continue
		}

		glog.Info(path)
		devPaths, _ := filepath.Glob(path + "/drm/*")

		for _, devPath := range devPaths {
			switch name := filepath.Base(devPath); {
			case name[0:4] == "card":
				card, _ = strconv.Atoi(name[4:])
			case name[0:7] == "renderD":
				renderD, _ = strconv.Atoi(name[7:])
				if info, exists := topologyInfo[renderD]; exists {
					devID = info.UniqueID
					nodeId = info.NodeID
				}
			}

		}
		// Extract PCI address from path (e.g., "0000:19:00.0" from "/sys/module/amdgpu/drivers/pci:amdgpu/0000:19:00.0")
		pciAddr := filepath.Base(path)

		// Get card family name
		familyName := ""
		if cardFamily, err := GetCardFamilyName(fmt.Sprintf("card%d", card)); err != nil {
			glog.Warningf("Failed to get card family name for card%d: %s", card, err)
		} else {
			familyName = cardFamily
		}

		// Get product name
		productName := ""
		productNamePath := fmt.Sprintf("/sys/class/drm/card%d/device/product_name", card)
		if b, err := ioutil.ReadFile(productNamePath); err != nil {
			glog.Warningf("Failed to read product name from %s: %s", productNamePath, err)
		} else {
			replacer := strings.NewReplacer(" ", "_", "(", "", ")", "")
			productName = replacer.Replace(strings.TrimSpace(string(b)))
		}

		// add devID and topology info so that we can identify later which gpu should get reported under which resource type
		deviceInfo := map[string]interface{}{
			"card":                 card,
			"renderD":              renderD,
			"devID":                devID,
			"pciAddr":              pciAddr,
			"driverVersion":        globalDriverVersion,
			"driverSrcVersion":     globalDriverSrcVersion,
			"computePartitionType": computePartitionType,
			"memoryPartitionType":  memoryPartitionType,
			"numaNode":             numaNode,
			"nodeId":               nodeId,
			"family":               familyName,
			"productName":          productName,
		}

		// Add SIMD and CU information from topology if available
		if info, exists := topologyInfo[renderD]; exists {
			deviceInfo["simdCount"] = info.SimdCount
			deviceInfo["simdPerCU"] = info.SimdPerCU
			deviceInfo["cuCount"] = info.CUCount
			deviceInfo["vramBytes"] = info.VramBytes
		}

		devices[filepath.Base(path)] = deviceInfo
	}

	// certain products have additional devices (such as MI300's partitions)
	//ex: /sys/devices/platform/amdgpu_xcp_30
	platformMatches, _ := filepath.Glob("/sys/devices/platform/amdgpu_xcp_*")

	for _, path := range platformMatches {
		glog.Info(path)
		devPaths, _ := filepath.Glob(path + "/drm/*")

		computePartitionType, memoryPartitionType := "", ""
		numaNode := -1
		parentPciAddr := ""
		familyName := ""
		productName := ""

		for _, devPath := range devPaths {
			switch name := filepath.Base(devPath); {
			case name[0:4] == "card":
				card, _ = strconv.Atoi(name[4:])
			case name[0:7] == "renderD":
				renderD, _ = strconv.Atoi(name[7:])
				if info, exists := topologyInfo[renderD]; exists {
					devID = info.UniqueID
					nodeId = info.NodeID
				}
				// Set the computePartitionType, memoryPartitionType, numaNode, PCI address from the real GPU using the common devID
				for _, device := range devices {
					if device["devID"] == devID {
						parentPciAddr = device["pciAddr"].(string)
						numaNode = device["numaNode"].(int)
						familyName = device["family"].(string)
						productName = device["productName"].(string)
						if device["computePartitionType"].(string) != "" && device["memoryPartitionType"].(string) != "" {
							computePartitionType = device["computePartitionType"].(string)
							memoryPartitionType = device["memoryPartitionType"].(string)
							break
						}
					}
				}
			}
		}
		// This is needed because some of the visible renderD are actually not valid
		// Their validity depends on topology information from KFD

		if _, exists := topologyInfo[renderD]; !exists {
			continue
		}
		if numaNode == -1 || parentPciAddr == "" {
			continue
		}

		deviceInfo := map[string]interface{}{
			"card":                 card,
			"renderD":              renderD,
			"devID":                devID,
			"pciAddr":              parentPciAddr,
			"driverVersion":        globalDriverVersion,
			"driverSrcVersion":     globalDriverSrcVersion,
			"computePartitionType": computePartitionType,
			"memoryPartitionType":  memoryPartitionType,
			"numaNode":             numaNode,
			"nodeId":               nodeId,
			"family":               familyName,
			"productName":          productName,
		}

		// Add SIMD and CU information from topology if available
		if info, exists := topologyInfo[renderD]; exists {
			deviceInfo["simdCount"] = info.SimdCount
			deviceInfo["simdPerCU"] = info.SimdPerCU
			deviceInfo["cuCount"] = info.CUCount
			deviceInfo["vramBytes"] = info.VramBytes
		}

		devices[filepath.Base(path)] = deviceInfo
	}
	glog.Infof("Devices map: %v", devices)
	return devices
}

// AMDGPU check if a particular card is an AMD GPU by checking the device's vendor ID
func AMDGPU(cardName string) bool {
	sysfsVendorPath := "/sys/class/drm/" + cardName + "/device/vendor"
	b, err := ioutil.ReadFile(sysfsVendorPath)
	if err == nil {
		vid := strings.TrimSpace(string(b))

		// AMD vendor ID is 0x1002
		if "0x1002" == vid {
			return true
		}
	} else {
		glog.Errorf("Error opening %s: %s", sysfsVendorPath, err)
	}
	return false
}

func openAMDGPU(cardName string) (C.amdgpu_device_handle, error) {
	if !AMDGPU(cardName) {
		return nil, fmt.Errorf("%s is not an AMD GPU", cardName)
	}
	devPath := "/dev/dri/" + cardName

	dev, err := os.Open(devPath)

	if err != nil {
		return nil, fmt.Errorf("Fail to open %s: %s", devPath, err)
	}
	defer dev.Close()

	devFd := C.int(dev.Fd())

	var devHandle C.amdgpu_device_handle
	var major C.uint32_t
	var minor C.uint32_t

	rc := C.amdgpu_device_initialize(devFd, &major, &minor, &devHandle)

	if rc < 0 {
		return nil, fmt.Errorf("Fail to initialize %s: %d", devPath, err)
	}
	glog.Infof("Initialized AMD GPU version: major %d, minor %d", major, minor)

	return devHandle, nil

}

// ParseTopologyProperties parse for a property value in kfd topology file
// The format is usually one entry per line <name> <value>.  Examples in
// testdata/topology-parsing/.
func ParseTopologyProperties(path string, re *regexp.Regexp) (int64, error) {
	f, e := os.Open(path)
	if e != nil {
		return 0, e
	}

	e = errors.New("Topology property not found.  Regex: " + re.String())
	v := int64(0)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		m := re.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}

		v, e = strconv.ParseInt(m[1], 0, 64)
		break
	}
	f.Close()

	return v, e
}

// ParseTopologyProperties parse for a property value in kfd topology file as string
// The format is usually one entry per line <name> <value>.  Examples in
// testdata/topology-parsing/.
func ParseTopologyPropertiesString(path string, re *regexp.Regexp) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}

	e = errors.New("Topology property not found.  Regex: " + re.String())
	v := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		m := re.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}

		v = m[1]
		e = nil
		break
	}
	f.Close()

	return v, e
}

// TopologyInfo holds topology information for a render device
type TopologyInfo struct {
	RenderDeviceID int    // The render device ID (e.g., 134 for renderD134)
	UniqueID       string // Unique ID from topology
	NodeID         int    // KFD node ID
	SimdCount      int    // Number of SIMD units
	SimdPerCU      int    // SIMD units per compute unit
	CUCount        int    // Computed: SimdCount / SimdPerCU
	VramBytes      uint64 // VRAM size in bytes
}

var topoDrmRenderMinorRe = regexp.MustCompile(`drm_render_minor\s(\d+)`)
var topoUniqueIdRe = regexp.MustCompile(`unique_id\s(\d+)`)
var topoSimdCountRe = regexp.MustCompile(`simd_count\s(\d+)`)
var topoSimdPerCuRe = regexp.MustCompile(`simd_per_cu\s(\d+)`)
var topoSizeInBytesRe = regexp.MustCompile(`size_in_bytes\s(\d+)`)

// GetTopologyInfo returns comprehensive topology information for all render devices
// This combines the functionality of GetDevIdsFromTopology and GetNodeIdsFromTopology
func GetTopologyInfo(topoRootParam ...string) map[int]*TopologyInfo {
	topoRoot := "/sys/class/kfd/kfd"
	if len(topoRootParam) == 1 {
		topoRoot = topoRootParam[0]
	}

	topologyInfoMap := make(map[int]*TopologyInfo)
	var nodeFiles []string
	var err error

	if nodeFiles, err = filepath.Glob(topoRoot + "/topology/nodes/*/properties"); err != nil {
		glog.Fatalf("glob error: %s", err)
		return topologyInfoMap
	}

	for _, nodeFile := range nodeFiles {
		glog.Info("Parsing " + nodeFile)

		// Parse render device minor number
		renderMinor, e := ParseTopologyProperties(nodeFile, topoDrmRenderMinorRe)
		if e != nil {
			glog.Error(e)
			continue
		}

		if renderMinor <= 0 {
			continue
		}

		// Parse unique ID
		uniqueID, e := ParseTopologyPropertiesString(nodeFile, topoUniqueIdRe)
		if e != nil {
			glog.Error(e)
			continue
		}

		// Extract node ID from file path
		nodeIndex := filepath.Base(filepath.Dir(nodeFile))
		nodeId, err := strconv.Atoi(nodeIndex)
		if err != nil {
			glog.Errorf("Failed to convert node index %s to int: %v", nodeIndex, err)
			continue
		}

		// Parse SIMD count
		simdCount, e := ParseTopologyProperties(nodeFile, topoSimdCountRe)
		if e != nil {
			glog.Warningf("Failed to parse simd_count from %s: %v", nodeFile, e)
			simdCount = 0 // Default to 0 if not available
		}

		// Parse SIMD per CU
		simdPerCU, e := ParseTopologyProperties(nodeFile, topoSimdPerCuRe)
		if e != nil {
			glog.Warningf("Failed to parse simd_per_cu from %s: %v", nodeFile, e)
			simdPerCU = 1 // Default to 1 to avoid division by zero
		}

		// Calculate CU count
		cuCount := 0
		if simdPerCU > 0 {
			cuCount = int(simdCount / simdPerCU)
		}

		// Parse VRAM information from mem_banks
		var vramBytes uint64 = 0
		vramPropertiesPath := fmt.Sprintf("%s/topology/nodes/%d/mem_banks/0/properties", topoRoot, nodeId)
		vramSize, e := ParseTopologyProperties(vramPropertiesPath, topoSizeInBytesRe)
		if e != nil {
			glog.Warningf("Failed to parse VRAM size from %s: %v", vramPropertiesPath, e)
			// VRAM parsing failed, continue with 0
		} else {
			vramBytes = uint64(vramSize)
			glog.Infof("Found VRAM size: %d bytes for renderD%d", vramBytes, renderMinor)
		}

		// Create topology info structure
		topologyInfoMap[int(renderMinor)] = &TopologyInfo{
			RenderDeviceID: int(renderMinor),
			UniqueID:       uniqueID,
			NodeID:         nodeId,
			SimdCount:      int(simdCount),
			SimdPerCU:      int(simdPerCU),
			CUCount:        cuCount,
			VramBytes:      vramBytes,
		}
	}

	return topologyInfoMap
}
