# AMD GPU DRA Driver — Device Attributes and Capabilities

This document summarizes what the AMD GPU DRA driver exposes today through
Kubernetes Dynamic Resource Allocation (DRA) ResourceSlices and how to
interpret those attributes when selecting devices.

The driver discovers AMD GPUs present on a node and advertises them as DRA
Devices. It supports:
- Full, unpartitioned GPUs
- Pre-partitioned devices (for platforms that expose partitions)

Device selection can then use DRA attributes to target either full GPUs or
partitions.

## Device identity and naming

- Canonical device name: `gpu-<cardIndex>-<renderIndex>`

## Device types (full GPU vs partition)

The driver distinguishes full GPUs from partitions via the `type` attribute:
- Full GPU: `type = amdgpu`
- Partition: `type = amdgpu-partition`

You can use this attribute in a claim’s `DeviceSelector` to select only
full GPUs or only partitions.

## Attributes for a full GPU

The following attributes are attached to each full GPU device:
- `type` (string): `amdgpu`
- `pciAddr` (string): PCI bus address of the device
- `cardIndex` (int): DRM card index (e.g., `card0` → 0)
- `renderIndex` (int): DRM render node index (e.g., `renderD128` → 128)
- `deviceID` (string): PCI device identifier (from sysfs)
- `family` (string): AMD GPU family string
- `productName` (string): Product name (normalized)
- `driverVersion` (semver): Kernel driver version
- `driverSrcVersion` (string): Kernel driver source version hash
- `partitionProfile` (string): For platforms that support partitioning, the
  current compute+memory profile (e.g., `spx_<mem>`); may be empty on devices
  that do not use partitioning
- Topology attribute: a PCIe root attribute is included when
  derivable; its qualified name and value come from the Kubernetes
  `deviceattribute` library and can be used by schedulers/topology-aware logic

Capacity values for full GPUs:
- `memory` (quantity, bytes): Advertised VRAM size; if the underlying topology
  inspection cannot determine VRAM precisely, a conservative default is used
- `computeUnits` (quantity): Number of compute units (CUs)
- `simdUnits` (quantity): Number of SIMD units

## Attributes for a partition

The following attributes are attached to each GPU partition device:
- `type` (string): `amdgpu-partition`
- `pciAddr` (string): PCI address of the parent GPU
- `cardIndex` (int): partition’s DRM card index
- `renderIndex` (int): partition’s DRM render node index
- `deviceID` (string): parent GPU PCI device ID
- Note: `deviceID` is identical for all partitions that belong to the
  same physical GPU. You can leverage this to target multiple partitions from
  the same parent device when co-location is desirable for performance or
  topology reasons.
- `family` (string): parent GPU family
- `productName` (string): parent product name
- `driverVersion` (semver): inherited from parent
- `driverSrcVersion` (string): inherited from parent
- `partitionProfile` (string): compute+memory profile of the partition
- Optional topology attribute: the parent’s PCIe root attribute is propagated

Capacity values for partitions:
- `memory` (quantity, bytes): VRAM capacity attributed to the partition; may
  use a conservative default when the exact value isn’t available
- `computeUnits` (quantity): number of CUs attributed to the partition
- `simdUnits` (quantity): number of SIMD units attributed to the partition

## How to select full GPUs vs partitions in claims

Use the `type` attribute selector in your ResourceClass/Claim to differentiate.
Examples (simplified):

Select only full GPUs:
```yaml
spec:
  devices:
    requests:
    - name: gpu
      deviceClassName: gpu.amd.com
      selectors:
        - cel:
            expression: 'device.attributes["gpu.amd.com"].type == "amdgpu"'
```

Select only partitions:
```yaml
spec:
  devices:
    requests:
    - name: gpu
      deviceClassName: gpu.amd.com
      selectors:
        - cel:
            expression: 'device.attributes["gpu.amd.com"].type == "amdgpu-partition"'
```

You may also combine with other attributes (e.g., `memory`, `family`,
`productName`, or the PCIe topology attribute) depending on scheduling needs.

### Request multiple partitions from the same parent GPU

To ensure two (or more) partitions come from the SAME physical GPU, use
`constraints.matchAttribute: deviceID` across multiple named requests.
Each request selects a single partition, and the constraint enforces that the
`deviceID` matches across those requests:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: two-partitions-same-parent
spec:
  devices:
    requests:
    - name: p0
      exactly:
        deviceClassName: gpu.amd.com
        allocationMode: ExactCount
        count: 1
        selectors:
          - cel:
              expression: 'device.attributes["gpu.amd.com"].type == "amdgpu-partition"'
    - name: p1
      exactly:
        deviceClassName: gpu.amd.com
        allocationMode: ExactCount
        count: 1
        selectors:
          - cel:
              expression: 'device.attributes["gpu.amd.com"].type == "amdgpu-partition"'
    constraints:
    - matchAttribute: gpu.amd.com/deviceID
      requests: ["p0", "p1"]
```

Notes:
- This does not require hard-coding a specific `deviceID`; the scheduler
  will choose a parent that has enough partitions to satisfy all listed
  requests where possible.
- If you instead want partitions from DIFFERENT parents, use
  `constraints.distinctAttribute: deviceID` across the requests.

## Current capabilities and notes

- Discovery: the driver walks the relevant sysfs paths to find AMD GPUs and
  (when present) additional exposed partitions (e.g., on platforms that publish
  partition nodes). It correlates DRM indices and KFD topology to enrich device
  information (family, VRAM, SIMD/CU counts).
- Pre-partitioned devices: supported and reported as distinct DRA Devices with
  their own identity and capacities, linked back to the parent GPU via
  attributes such as `pciAddr` and `deviceID`.
- Topology hinting: a PCIe root attribute is added when derivable, enabling
  topology-aware scheduling.
- Defaults: when certain metrics (like VRAM) cannot be read reliably, the
  driver falls back to conservative defaults to remain usable. These values can
  differ from the exact hardware amounts and are best used as coarse selectors.

If you need additional attributes or different representations, please open an
issue discussing your use case.
