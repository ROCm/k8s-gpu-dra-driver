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
// of AMD GPU via Linux kernel interfaces like sysfs.
package amdgpu

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/golang/glog"
)

// GetDriverVersion reads the AMDGPU driver version
func GetDriverVersion() string {
	matches, _ := filepath.Glob("/sys/class/drm/card*/device/driver/module/version")
	if len(matches) == 0 {
		glog.Warningf("No AMD GPU cards found for driver version reading, using fallback 0.0.0")
		return "0.0.0"
	}

	for _, versionPath := range matches {
		b, err := os.ReadFile(versionPath)
		if err != nil {
			continue
		}
		driverVersion := strings.TrimSpace(string(b))
		if driverVersion != "" {
			return driverVersion
		}
	}

	// In-kernel amdgpu module may not set a version string (empty /sys/module/amdgpu/version).
	// Fall back to "0.0.0" so the ResourceSlice semver validation succeeds.
	glog.Warningf("Failed to read AMDGPU driver version from any card, using fallback 0.0.0")
	return "0.0.0"
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
	globalDriverVersion := GetDriverVersion()

	for _, path := range matches {
		computePartitionFile := filepath.Join(path, "current_compute_partition")
		memoryPartitionFile := filepath.Join(path, "current_memory_partition")
		numaNodeFile := filepath.Join(path, "numa_node")

		computePartitionType, memoryPartitionType := "", ""
		var numaNode int

		// Read the compute partition
		if data, err := os.ReadFile(computePartitionFile); err == nil {
			computePartitionType = strings.ToLower(strings.TrimSpace(string(data)))
		} else {
			glog.Warningf("Failed to read 'current_compute_partition' file at %s: %s", computePartitionFile, err)
		}

		// Read the memory partition
		if data, err := os.ReadFile(memoryPartitionFile); err == nil {
			memoryPartitionType = strings.ToLower(strings.TrimSpace(string(data)))
		} else {
			glog.Warningf("Failed to read 'current_memory_partition' file at %s: %s", memoryPartitionFile, err)
		}

		if data, err := os.ReadFile(numaNodeFile); err == nil {
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

		// Get product name
		productName := ""
		productNamePath := fmt.Sprintf("/sys/class/drm/card%d/device/product_name", card)
		if b, err := os.ReadFile(productNamePath); err != nil {
			glog.Warningf("Failed to read product name from %s: %s", productNamePath, err)
		} else {
			replacer := strings.NewReplacer(" ", "_", "(", "", ")", "")
			productName = replacer.Replace(strings.TrimSpace(string(b)))
		}

		sysfsDeviceID := GetDeviceID(fmt.Sprintf("card%d", card))

		// add devID and topology info so that we can identify later which gpu should get reported under which resource type
		deviceInfo := map[string]interface{}{
			"card":                 card,
			"renderD":              renderD,
			"kfdID":                devID,
			"deviceID":             sysfsDeviceID,
			"pciAddr":              pciAddr,
			"driverVersion":        globalDriverVersion,
			"computePartitionType": computePartitionType,
			"memoryPartitionType":  memoryPartitionType,
			"numaNode":             numaNode,
			"nodeId":               nodeId,
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
		productName := ""
		sysfsDeviceID := ""

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
					if device["kfdID"] == devID {
						parentPciAddr = device["pciAddr"].(string)
						numaNode = device["numaNode"].(int)
						productName = device["productName"].(string)
						sysfsDeviceID = device["deviceID"].(string)
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
			"kfdID":                devID,
			"deviceID":             sysfsDeviceID,
			"pciAddr":              parentPciAddr,
			"driverVersion":        globalDriverVersion,
			"computePartitionType": computePartitionType,
			"memoryPartitionType":  memoryPartitionType,
			"numaNode":             numaNode,
			"nodeId":               nodeId,
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

// GetDeviceID reads the PCI device ID from sysfs for the given DRM card
// Returns the device ID string (e.g., "0x740f") or empty string on failure
func GetDeviceID(cardName string) string {
	sysfsDevicePath := "/sys/class/drm/" + cardName + "/device/device"
	b, err := os.ReadFile(sysfsDevicePath)
	if err != nil {
		glog.Warningf("Failed to read device ID from %s: %s", sysfsDevicePath, err)
		return ""
	}
	return strings.TrimSpace(string(b))
}

// AMDGPU check if a particular card is an AMD GPU by checking the device's vendor ID
func AMDGPU(cardName string) bool {
	sysfsVendorPath := "/sys/class/drm/" + cardName + "/device/vendor"
	b, err := os.ReadFile(sysfsVendorPath)
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
var topoSimdCountRe = regexp.MustCompile(`simd_count\s(\d+)`)
var topoSimdPerCuRe = regexp.MustCompile(`simd_per_cu\s(\d+)`)
var topoSizeInBytesRe = regexp.MustCompile(`size_in_bytes\s(\d+)`)
var topoLocationIdRe = regexp.MustCompile(`location_id\s(\d+)`)
var topoDomainRe = regexp.MustCompile(`domain\s(\d+)`)

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
		locationId, e := ParseTopologyProperties(nodeFile, topoLocationIdRe)
		if e != nil {
			glog.Error(e)
			continue
		}

		// Parse domain
		domain, e := ParseTopologyProperties(nodeFile, topoDomainRe)
		if e != nil {
			glog.Error(e)
			continue
		}

		dev := (locationId >> 3) & 0x1f
		bus := (locationId >> 8) & 0xff
		devID := fmt.Sprintf("%04x:%02x:%02x:0", domain, bus, dev)

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
			UniqueID:       devID,
			NodeID:         nodeId,
			SimdCount:      int(simdCount),
			SimdPerCU:      int(simdPerCU),
			CUCount:        cuCount,
			VramBytes:      vramBytes,
		}
	}

	return topologyInfoMap
}
