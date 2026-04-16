# AMD GPU DRA Driver — Device Attributes and Capabilities

This document summarizes what the AMD GPU DRA driver exposes through
Kubernetes Dynamic Resource Allocation (DRA) ResourceSlices and how to
interpret those attributes when selecting devices.

The driver discovers AMD GPUs present on a node and advertises them as DRA
Devices. It supports:
- Full, unpartitioned GPUs
- Pre-partitioned devices (for platforms that expose partitions)

Device selection can then use DRA attributes to target either full GPUs or
partitions.

## Device identity and naming

- Canonical device name: `gpu-<card>-<renderD>` (e.g., `gpu-0-128`)

## Device types (full GPU vs partition)

The driver distinguishes full GPUs from partitions via the `type` attribute:
- Full GPU: `type = amdgpu`
- Partition: `type = amdgpu-partition`

You can use this attribute in a claim's `DeviceSelector` to select only
full GPUs or only partitions.

## Attributes for a full GPU

The following attributes are attached to each full GPU device:
- `type` (string): `amdgpu`
- `deviceID` (string): PCI device ID from sysfs (e.g., `0x740f`). All GPUs
  of the same model share the same `deviceID`.
- `productName` (string): Product name (normalized)
- `driverVersion` (semver): Kernel driver version
- `partitionProfile` (string): For platforms that support partitioning, the
  current compute+memory profile (e.g., `spx_nps1`); omitted on devices
  that do not support partitioning
- `numaNode` (int): NUMA node the GPU is attached to (read from sysfs)
- `resource.kubernetes.io/pciBusID` (string): PCI bus address in extended BDF
  notation (e.g., `0000:19:00.0`). Standard Kubernetes attribute. Unique per
  physical GPU — use this for same-parent or different-parent constraints.
- `resource.kubernetes.io/pcieRoot` (string): PCIe root complex (e.g.,
  `pci0000:00`). Standard Kubernetes attribute for topology-aware scheduling.

Capacity values for full GPUs:
- `memory` (quantity, bytes): Advertised VRAM size; if the underlying topology
  inspection cannot determine VRAM precisely, a conservative default is used
- `computeUnits` (quantity): Number of compute units (CUs)
- `simdUnits` (quantity): Number of SIMD units

## Attributes for a partition

The following attributes are attached to each GPU partition device:
- `type` (string): `amdgpu-partition`
- `deviceID` (string): PCI device ID of the parent GPU (same value as the
  parent's `deviceID`, since partitions share the same physical device)
- `productName` (string): parent product name
- `driverVersion` (semver): inherited from parent
- `partitionProfile` (string): compute+memory profile of the partition
- `numaNode` (int): NUMA node inherited from the parent GPU
- `resource.kubernetes.io/pciBusID` (string): PCI bus address of the parent
  GPU. Identical for all partitions from the same physical device — use this
  to match or distinguish parents.
- `resource.kubernetes.io/pcieRoot` (string): parent's PCIe root complex

Capacity values for partitions:
- `memory` (quantity, bytes): VRAM capacity attributed to the partition
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

You may also combine with other attributes (e.g., `memory`, `deviceID`,
`productName`, or the PCIe topology attributes) depending on scheduling needs.

### Request multiple partitions from the same parent GPU

To ensure two (or more) partitions come from the SAME physical GPU, use
`constraints.matchAttribute: resource.kubernetes.io/pciBusID` across multiple
named requests. Each request selects a single partition, and the constraint
enforces that the PCI bus address (and therefore the parent GPU) matches:

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
    - matchAttribute: resource.kubernetes.io/pciBusID
      requests: ["p0", "p1"]
```

Notes:
- This does not require hard-coding a specific PCI address; the scheduler
  will choose a parent that has enough partitions to satisfy all listed
  requests where possible.
- If you instead want partitions from DIFFERENT parents, use
  `constraints.distinctAttribute: resource.kubernetes.io/pciBusID`.

## NUMA-aware GPU scheduling

The `numaNode` attribute reports the NUMA node each GPU is attached to. Use it
to co-locate GPUs on the same NUMA node and reduce memory-access latency for
CPU-GPU workloads.

The recommended pattern is `constraints.matchAttribute` — the scheduler picks
any NUMA node but guarantees every matched request lands on the same one:

```yaml
spec:
  devices:
    requests:
      - name: g0
        deviceClassName: gpu.amd.com
      - name: g1
        deviceClassName: gpu.amd.com
      - name: g2
        deviceClassName: gpu.amd.com
      - name: g3
        deviceClassName: gpu.amd.com
    constraints:
      - matchAttribute: gpu.amd.com/numaNode
        requests: ["g0", "g1", "g2", "g3"]
```

See `example/example-numa-aligned-gpus.yaml` for a complete working example
that uses this pattern to run two tensor-parallel vLLM replicas, each pinned to
a single NUMA node.

If you need GPUs from a *specific* NUMA node, add a CEL selector instead:

```yaml
selectors:
  - cel:
      expression: 'device.attributes["gpu.amd.com"].numaNode == 0'
```

## Current capabilities and notes

- Discovery: the driver walks the relevant sysfs paths to find AMD GPUs and
  (when present) additional exposed partitions (e.g., on platforms that publish
  partition nodes). It correlates DRM indices and KFD topology to enrich device
  information (VRAM, SIMD/CU counts).
- Pre-partitioned devices: supported and reported as distinct DRA Devices with
  their own identity and capacities. Partitions share the same `pciBusID`
  as their parent GPU, enabling same-parent and different-parent constraint
  patterns.
- Topology hinting: `resource.kubernetes.io/pcieRoot` and
  `resource.kubernetes.io/pciBusID` standard attributes enable topology-aware
  scheduling.
- NUMA node discovery: the driver reads the NUMA node for each GPU from sysfs
  and exposes it as an integer attribute for NUMA-aware scheduling.
- Defaults: when certain metrics (like VRAM) cannot be read reliably, the
  driver falls back to conservative defaults to remain usable.

If you need additional attributes or different representations, please open an
issue discussing your use case.
