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
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/golang/glog"
)

// GetDriverVersion reads the AMDGPU driver version
func GetDriverVersion() string {
	matches, _ := filepath.Glob(filepath.Join(DRMClassPath, "card*/device/driver/module/version"))
	if len(matches) == 0 {
		glog.Warningf("No AMD GPU cards found for driver version reading; driverVersion attribute will be omitted")
		return ""
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
	// Return empty so the caller omits the driverVersion attribute entirely rather than
	// publishing a synthetic value; the ResourceSlice is still valid without it.
	glog.Warningf("Failed to read AMDGPU driver version from any card; driverVersion attribute will be omitted")
	return ""
}

// SemverDriverVersion trims the AMDGPU version to MAJOR.MINOR.PATCH. The
// out-of-tree amdgpu module reports a 4-component string (e.g. "6.19.14.31400000"
// where the trailing field is a build number), which the DRA ResourceSlice
// VersionValue rejects for not being valid semver 2.0.0. Keep the first three
// numeric components and drop the build metadata; the full raw string is
// published separately as a string attribute.
func SemverDriverVersion(version string) string {
	parts := strings.Split(version, ".")
	if len(parts) <= 3 {
		return version
	}
	return strings.Join(parts[:3], ".")
}

// parseDRMIndex returns the numeric index of a DRM node name (for example 128
// from "renderD128"). ok is false when the prefix is missing or the suffix is
// not a plain decimal, so a malformed name is skipped instead of read as index 0.
func parseDRMIndex(name, prefix string) (int, bool) {
	suffix, found := strings.CutPrefix(name, prefix)
	if !found || suffix == "" {
		return 0, false
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(suffix)
	if err != nil {
		return 0, false
	}
	return n, true
}

// resolveDRMIdentity reads a GPU's card index, render minor, KFD node ID, and
// unique (kfd) ID from its drm entries, starting fresh on every call so a device
// never inherits the previous one's identity. ok is true only when both a card
// and a render node resolve to a valid index; otherwise the caller skips the
// device instead of publishing it with a borrowed or default identity.
func resolveDRMIdentity(devPaths []string, topologyInfo map[int]*TopologyInfo) (card, renderD, nodeId int, devID string, ok bool) {
	haveCard, haveRender, conflict := false, false, false
	for _, devPath := range devPaths {
		name := filepath.Base(devPath)
		switch {
		case strings.HasPrefix(name, "card"):
			n, valid := parseDRMIndex(name, "card")
			if !valid {
				continue
			}
			if haveCard && n != card {
				conflict = true
			}
			card, haveCard = n, true
		case strings.HasPrefix(name, "renderD"):
			n, valid := parseDRMIndex(name, "renderD")
			if !valid {
				continue
			}
			if haveRender && n != renderD {
				conflict = true
			}
			renderD, haveRender = n, true
			// Keep nodeId/devID tied to the current renderD; a render node without
			// topology contributes no identity rather than a previous one's.
			if info, exists := topologyInfo[n]; exists {
				devID, nodeId = info.UniqueID, info.NodeID
			} else {
				devID, nodeId = "", 0
			}
		}
	}
	return card, renderD, nodeId, devID, haveCard && haveRender && !conflict
}

// uniquePhysicalParent returns the one physical GPU sharing devID. Zero matches means
// the parent has not been discovered, and more than one means two GPUs reported the same
// kfd id, so neither can be attributed. Both are errors rather than a first-match guess,
// since map iteration order would otherwise decide which parent a partition inherits.
func uniquePhysicalParent(devID string, physical map[string]map[string]interface{}) (map[string]interface{}, error) {
	var found map[string]interface{}
	var addrs []string
	for _, device := range physical {
		if device["kfdID"] != devID {
			continue
		}
		addr, _ := device["pciAddr"].(string)
		addrs = append(addrs, addr)
		found = device
	}
	switch len(addrs) {
	case 0:
		return nil, fmt.Errorf("no physical GPU reports kfd id %q", devID)
	case 1:
		return found, nil
	default:
		sort.Strings(addrs)
		return nil, fmt.Errorf("kfd id %q is reported by %d physical GPUs (%s)", devID, len(addrs), strings.Join(addrs, ", "))
	}
}

// pciAddrOf returns the PCI address encoded in a sysfs device path.
func pciAddrOf(devicePath string) string { return filepath.Base(devicePath) }

// readPartitionMode reads a current_*_partition file. A missing file means the device
// does not support partitioning, which is a whole GPU. Any other error is returned
// rather than reported as an empty mode, because an empty mode reads downstream as
// "no partitioning" and would publish a partitioned GPU as one whole device.
func readPartitionMode(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("unable to read %s: %w", path, err)
	}
	return strings.ToLower(strings.TrimSpace(string(data))), nil
}

// discoveryAttempts and discoveryBackoff bound the wait for a GPU whose sysfs entries are
// still appearing. Discovery runs once at startup, so a device skipped here stays missing
// until the plugin restarts.
var (
	discoveryAttempts = 5
	discoveryBackoff  = 200 * time.Millisecond
)

// SetDiscoveryRetry overrides the retry bounds. Used by tests to avoid waiting out the
// real backoff for a device that is meant to stay incomplete.
func SetDiscoveryRetry(attempts int, backoff time.Duration) (restore func()) {
	prevAttempts, prevBackoff := discoveryAttempts, discoveryBackoff
	discoveryAttempts, discoveryBackoff = attempts, backoff
	return func() { discoveryAttempts, discoveryBackoff = prevAttempts, prevBackoff }
}

// GetAMDGPUs returns the AMD GPUs on the node, keyed by part of the PCI address. A device
// bound to amdgpu whose DRM or KFD entries have not appeared yet is retried a few times,
// since discovery runs once and would otherwise leave the GPU missing for the lifetime of
// the process. A device still incomplete after that is skipped as before.
func GetAMDGPUs() map[string]map[string]interface{} {
	seen := 0
	for attempt := 1; ; attempt++ {
		devices, skipped := discoverAMDGPUs()
		// Fewer devices than a previous attempt saw means the tree changed underneath
		// this pass, so it is no more complete than the one that skipped something.
		if len(devices) < seen {
			glog.Warningf("discovery saw %d device(s) after a previous attempt saw %d; treating the pass as incomplete", len(devices), seen)
			skipped++
		}
		seen = max(seen, len(devices))
		if skipped == 0 {
			return devices
		}
		if attempt >= discoveryAttempts {
			glog.Warningf("%d device(s) still had incomplete sysfs entries after %d attempts; they are not published until the plugin restarts", skipped, attempt)
			return devices
		}
		glog.Infof("%d device(s) have incomplete sysfs entries; retrying discovery (%d/%d)", skipped, attempt, discoveryAttempts)
		time.Sleep(discoveryBackoff)
	}
}

// discoverAMDGPUs runs one discovery pass, returning the devices it published and how many
// it skipped for a condition that may still resolve.
func discoverAMDGPUs() (map[string]map[string]interface{}, int) {
	if _, err := os.Stat(AMDGPUDriversPath); err != nil {
		// Retryable: the module may be reloading. On a node with no AMD GPU at all
		// this costs one bounded backoff at startup and still returns empty.
		glog.Warningf("amdgpu driver unavailable: %s", err)
		return make(map[string]map[string]interface{}), 1
	}

	//ex: /sys/module/amdgpu/drivers/pci:amdgpu/0000:19:00.0
	matches, _ := filepath.Glob(filepath.Join(AMDGPUDriversPath, "pci:amdgpu/[0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F]:*"))

	devices := make(map[string]map[string]interface{})
	skipped := 0

	// Get comprehensive topology information once instead of multiple calls
	topologyInfo := GetTopologyInfo()

	// Get driver version once for all devices
	globalDriverVersion := GetDriverVersion()

	for _, path := range matches {
		computePartitionFile := filepath.Join(path, "current_compute_partition")
		memoryPartitionFile := filepath.Join(path, "current_memory_partition")
		numaNodeFile := filepath.Join(path, "numa_node")

		var numaNode int

		// Read the compute partition
		computePartitionType, err := readPartitionMode(computePartitionFile)
		if err != nil {
			glog.Errorf("Skipping device %s: %s", pciAddrOf(path), err)
			skipped++
			continue
		}

		// Read the memory partition
		memoryPartitionType, err := readPartitionMode(memoryPartitionFile)
		if err != nil {
			glog.Errorf("Skipping device %s: %s", pciAddrOf(path), err)
			skipped++
			continue
		}

		// The two files appear and disappear together on a partitioned GPU. Exactly one
		// of them means the read caught the tree mid-change, and reading it as "no
		// partitioning" would publish a partitioned GPU whole.
		if (computePartitionType == "") != (memoryPartitionType == "") {
			glog.Warningf("Skipping device %s: compute partition %q and memory partition %q are not both present", pciAddrOf(path), computePartitionType, memoryPartitionType)
			skipped++
			continue
		}

		if data, err := os.ReadFile(numaNodeFile); err == nil {
			numaNodeStr := strings.TrimSpace(string(data))
			numaNode, err = strconv.Atoi(numaNodeStr)
			if err != nil {
				glog.Warningf("Failed to convert 'numa_node' value to int: %s", err)
				skipped++
				continue
			}
		} else {
			glog.Warningf("Failed to read 'numa_node' file at %s: %s", numaNodeFile, err)
			skipped++
			continue
		}

		glog.Info(path)
		devPaths, _ := filepath.Glob(path + "/drm/*")
		// Extract PCI address from path (e.g., "0000:19:00.0" from "/sys/module/amdgpu/drivers/pci:amdgpu/0000:19:00.0")
		pciAddr := filepath.Base(path)
		card, renderD, nodeId, devID, ok := resolveDRMIdentity(devPaths, topologyInfo)
		if !ok {
			glog.Warningf("Skipping device %s: no valid card and renderD drm entries", pciAddr)
			skipped++
			continue
		}
		if devID == "" {
			// Partitions find their parent by this id, and the platform loop skips a
			// partition without one, so publishing this device alone would advertise
			// part of a partitioned GPU. A device that is not partitioned is still
			// usable whole, so only the partitioned case is dropped.
			if computePartitionType != "" && computePartitionType != "spx" {
				glog.Warningf("Skipping device %s (card%d renderD%d): compute mode %q but no KFD compute identity yet, so its partitions cannot be matched", pciAddr, card, renderD, computePartitionType)
				skipped++
				continue
			}
			glog.Warningf("device %s (card%d renderD%d) has no KFD compute identity (empty unique id); publishing it as a full GPU anyway", pciAddr, card, renderD)
		}

		// Get product name
		productName := ""
		productNamePath := filepath.Join(DRMClassPath, fmt.Sprintf("card%d/device/product_name", card))
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

	// The platform loop appends to devices, so search a snapshot of the physical GPUs: a
	// partition must match its parent, never an XCP sibling an earlier iteration added.
	physicalDevices := make(map[string]map[string]interface{}, len(devices))
	maps.Copy(physicalDevices, devices)

	// certain products have additional devices (such as MI300's partitions)
	//ex: /sys/devices/platform/amdgpu_xcp_30
	platformMatches, _ := filepath.Glob(filepath.Join(PlatformDevicesPath, "amdgpu_xcp_*"))

	for _, path := range platformMatches {
		glog.Info(path)
		devPaths, _ := filepath.Glob(path + "/drm/*")

		computePartitionType, memoryPartitionType := "", ""
		numaNode := -1
		parentPciAddr := ""
		productName := ""
		sysfsDeviceID := ""

		card, renderD, nodeId, devID, ok := resolveDRMIdentity(devPaths, topologyInfo)
		if !ok || devID == "" {
			glog.Warningf("Skipping platform device %s: unresolved GPU identity", filepath.Base(path))
			skipped++
			continue
		}
		// Inherit the compute/memory partition, NUMA node, PCI address, product name,
		// and device ID from the parent GPU that shares this kfd (unique) ID.
		parent, err := uniquePhysicalParent(devID, physicalDevices)
		if err != nil {
			glog.Warningf("Skipping platform device %s: %s", filepath.Base(path), err)
			skipped++
			continue
		}
		parentPciAddr = parent["pciAddr"].(string)
		numaNode = parent["numaNode"].(int)
		productName = parent["productName"].(string)
		sysfsDeviceID = parent["deviceID"].(string)
		computePartitionType = parent["computePartitionType"].(string)
		memoryPartitionType = parent["memoryPartitionType"].(string)
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
	return devices, skipped
}

// GetDeviceID reads the PCI device ID from sysfs for the given DRM card
// Returns the device ID string (e.g., "0x740f") or empty string on failure
func GetDeviceID(cardName string) string {
	sysfsDevicePath := filepath.Join(DRMClassPath, cardName, "device/device")
	b, err := os.ReadFile(sysfsDevicePath)
	if err != nil {
		glog.Warningf("Failed to read device ID from %s: %s", sysfsDevicePath, err)
		return ""
	}
	return strings.TrimSpace(string(b))
}

// AMDGPU check if a particular card is an AMD GPU by checking the device's vendor ID
func AMDGPU(cardName string) bool {
	sysfsVendorPath := filepath.Join(DRMClassPath, cardName, "device/vendor")
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
	topoRoot := KFDTopologyPath
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
