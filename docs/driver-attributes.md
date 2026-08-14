# AMD GPU DRA Driver — Device Attributes and Capabilities

This document summarizes what the AMD GPU DRA driver exposes through
Kubernetes Dynamic Resource Allocation (DRA) ResourceSlices and how to
interpret those attributes and capacities when selecting devices.

The driver discovers AMD GPUs present on a node and advertises them as DRA
Devices. It supports:
- Full, unpartitioned GPUs
- Pre-partitioned devices (for platforms that expose partitions)
- Auto-partition mode (virtual partition devices created on-demand)

Device selection can then use DRA attributes to target either full GPUs or
partitions.

## Device identity and naming

- Full GPU / partition canonical name: `gpu-<cardIndex>-<renderIndex>`
  (e.g., `gpu-0-128`)
- Auto-partition canonical name: `gpu-<gpuIndex>-<computeMode>-<memoryMode>`
  (e.g., `gpu-0-cpx-nps4`)

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
- `memory` (quantity, bytes): Advertised VRAM size. When VRAM cannot be read, the
  driver publishes `0`. Zero is a sentinel for an unknown/unreadable value, not a
  measured physical capacity.
- `computeUnits` (quantity): Number of compute units (CUs)
- `simdUnits` (quantity): Number of SIMD units

Workloads which require a minimum amount of VRAM should explicitly select a
positive capacity, and should guard the reference so the selector does not abort
allocation on a device that has no `memory` capacity:

```cel
cel.bind(cap, device.capacity["gpu.amd.com"],
  has(cap.memory) && cap.memory.compareTo(quantity("16Gi")) >= 0)
```

A selector that references a capacity a device does not publish is an evaluation
error that aborts the whole allocation, not a simple non-match, so the
`has(cap.memory)` guard keeps the selector robust as other device types are
added. This driver always publishes `memory` (`0` when unreadable), so the guard
is precautionary here and matters mainly for capacities other drivers may leave
unset. Devices with unknown VRAM remain allocatable to claims which do not
reference memory.

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
- `memory` (quantity, bytes): VRAM capacity attributed to the partition, or `0`
  when it cannot be read (the same unknown/unreadable sentinel as for full GPUs)
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

You may also combine with capacities and other attributes (e.g., the `memory`
capacity, or the `deviceID`, `productName`, and PCIe topology attributes)
depending on scheduling needs.

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

## Auto-partition mode (virtual partition devices) [Beta]

When auto-partition is enabled (the `AutoPartition` feature gate, set via
`--feature-gates=AutoPartition=true` or `FEATURE_GATES=AutoPartition=true`), the
driver advertises virtual partition devices for every valid compute+memory
partition combination on each partitionable GPU. Non-partitionable GPUs are
still advertised as normal full GPU devices.

This mode requires Kubernetes 1.36+ with DRA beta features enabled.

> **Do not run the Device Config Manager (DCM) on nodes where auto-partition is
> enabled.** In auto-partition mode the DRA driver is the sole owner of GPU
> partitioning: it decides the partition layout dynamically at claim-prepare time
> and reconfigures the hardware via `amd-smi`. DCM is a second partitioning
> authority that sets a fixed layout out-of-band. Running both against the same
> GPUs makes them fight over the hardware — the DRA driver will repartition a GPU
> that DCM configured (and vice-versa), producing incorrect layouts, failed
> reloads, and workloads landing on the wrong partition. Pick one: use DCM for
> statically pre-partitioned nodes, or auto-partition for dynamic partitioning —
> never both on the same node.

### How it works

1. On startup the driver discovers physical GPUs.
2. For each GPU that supports compute partitioning, it generates one virtual
   device per valid compute+memory combination:
   - `spx-nps1` -- full GPU (1 partition)
   - `dpx-nps2` -- dual partition (2 partitions)
   - `cpx-nps1` -- 8-way compute partition, single memory domain
   - `cpx-nps4` -- 8-way compute partition, 4-way memory domain
3. When a ResourceClaim is allocated and prepared, the driver calls `amd-smi`
   to set the requested compute and memory partition modes on the physical GPU.
4. Per-GPU shared counters (mutex) prevent conflicting partition modes from
   being allocated on the same GPU simultaneously.
5. Device taints (`NoExecute`) are applied to devices whose memory mode
   conflicts with the currently active memory mode on the node. Taints are
   removed when all allocations are released.

### Auto-partition device naming

Virtual devices are named `gpu-<gpuIndex>-<computeMode>-<memoryMode>`, for
example `gpu-0-cpx-nps4` or `gpu-1-spx-nps1`.

### Device types in auto-partition mode

The `type` attribute on auto-partition devices uses the same values as normal
devices to maintain compatibility with existing selectors:
- `type = amdgpu` for SPX mode (full GPU, 1 partition)
- `type = amdgpu-partition` for DPX/CPX modes (partitioned)

### Attributes for an auto-partition device

- `type` (string): `amdgpu` or `amdgpu-partition` (see above)
- `computePartition` (string): compute partition mode -- one of `spx`, `dpx`,
  `cpx`
- `memoryPartition` (string): memory partition mode -- one of `nps1`, `nps2`,
  `nps4`
- `gpuIndex` (int): physical GPU index on the node (0-based, sorted by PCI
  address)
- `pciAddr` (string): PCI bus address of the physical GPU
- `productName` (string): product name of the physical GPU
- `deviceID` (string): PCI device ID of the physical GPU (e.g., `0x740f`)
- `driverVersion` (semver): kernel driver version (normalized to semver)
- `driverVersionFull` (string): full kernel driver version string
- `numaNode` (int): NUMA node the GPU is attached to
- Topology attributes: `resource.kubernetes.io/pciBusID` and
  `resource.kubernetes.io/pcieRoot` (when derivable)

### Capacity values for auto-partition devices

- `partitions` (quantity): number of partitions this configuration creates
  (1 for SPX, 2 for DPX, 8 for CPX). This is a consumable capacity --
  multiple allocations can share the same partition configuration up to this
  count. A default request of 1 is applied when no explicit request is made.
- `memory` (quantity, bytes): per-partition VRAM (total GPU VRAM divided by
  partition count)
- `computeUnits` (quantity): per-partition CUs
- `simdUnits` (quantity): per-partition SIMD units

### Shared counters

Each partitionable GPU has a shared counter set named `gpu-<gpuIndex>-mutex`
with a single counter `partition-mode` of capacity 1. Every virtual partition
device for that GPU consumes this counter, so the scheduler ensures only one
partition configuration can be active per GPU at a time.

### Memory partition taints

When the first partition allocation on a node sets the memory mode (e.g.,
`nps4`), all virtual partition devices with a different memory mode receive a
`NoExecute` taint with key `gpu.amd.com/memory-partition-conflict`. This
prevents the scheduler from allocating incompatible memory modes. When all
partition allocations are released, taints are removed and any memory mode
becomes available again.

### Enabling auto-partition via Helm

Enable the `AutoPartition` feature gate in your Helm values:

```yaml
featureGates:
  AutoPartition: true
```

Or via command line:
```bash
helm install amd-gpu-driver ./helm-charts-k8s \
  --set featureGates.AutoPartition=true
```

### Selecting auto-partition devices in claims

Request a specific partition configuration using `computePartition` and
`memoryPartition` attribute selectors:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: cpx-partition
spec:
  devices:
    requests:
    - name: gpu
      deviceClassName: gpu.amd.com
      selectors:
        - cel:
            expression: 'device.attributes["gpu.amd.com"].computePartition == "cpx"'
        - cel:
            expression: 'device.attributes["gpu.amd.com"].memoryPartition == "nps4"'
```

Request a full GPU (SPX mode):

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: full-gpu
spec:
  devices:
    requests:
    - name: gpu
      deviceClassName: gpu.amd.com
      selectors:
        - cel:
            expression: 'device.attributes["gpu.amd.com"].computePartition == "spx"'
```

### Migrating from the device plugin

If your workloads currently request partitioned GPUs from the AMD GPU device
plugin using extended resource names such as `amd.com/cpx_nps4`, see
[Migrating partitioned-GPU workloads from the device plugin to DRA](device-plugin-to-dra-partitions.md)
for the mapping to DRA ResourceClaims. The DRA driver does not advertise
per-partition extended resource names; partition configurations are selected via
claim attributes instead.

## Current capabilities and notes

- Discovery: the driver walks the relevant sysfs paths to find AMD GPUs and
  (when present) additional exposed partitions (e.g., on platforms that publish
  partition nodes). It correlates DRM indices and KFD topology to enrich device
  information (VRAM, SIMD/CU counts).
- Pre-partitioned devices: supported and reported as distinct DRA Devices with
  their own identity and capacities. Partitions share the same `pciBusID`
  as their parent GPU, enabling same-parent and different-parent constraint
  patterns.
- Auto-partition mode: when enabled, the driver advertises virtual partition
  devices for all valid compute+memory combinations and dynamically partitions
  GPUs at claim-prepare time via `amd-smi`. Shared counters and device taints
  enforce partition mode exclusivity. Requires Kubernetes 1.36+.
- Topology hinting: `resource.kubernetes.io/pcieRoot` and
  `resource.kubernetes.io/pciBusID` standard attributes enable topology-aware
  scheduling.
- NUMA node discovery: the driver reads the NUMA node for each GPU from sysfs
  and exposes it as an integer attribute for NUMA-aware scheduling.
- Unreadable metrics: when VRAM cannot be read reliably, the driver publishes a
  `memory` capacity of `0` (a sentinel for unknown, not a real size) rather than
  guessing. Memory-aware claims should require a positive capacity behind an
  existence guard (see the selector example above).

## VFIO passthrough devices

When the `VFIOPassthrough` feature gate is enabled, the driver discovers GIM
SR-IOV VFs and advertises them as VFIO passthrough devices with `type = vfio`.

### Attributes for a VFIO device

| Attribute | Type | Description |
|-----------|------|-------------|
| `type` | string | Always `vfio` |
| `numaNode` | int | NUMA node affinity |
| `iommuGroup` | string | IOMMU group number |
| `pciAddr` | string | PCI BDF address (e.g., `0000:1b:02.0`) |
| `isVF` | bool | `true` for SR-IOV VFs, `false` for PF passthrough |
| `productName` | string | GPU product name (e.g., `Instinct_MI300X`) |
| `deviceID` | string | PCI device ID (e.g., `0x740f`) |
| `vendorID` | string | PCI vendor ID (`0x1002` for AMD) |

Standard topology attributes (`resource.kubernetes.io/pciBusID`,
`resource.kubernetes.io/pcieRoot`) are also published when available.

### Selecting VFIO devices

```yaml
selectors:
- cel:
    expression: >-
      device.driver == 'gpu.amd.com' &&
      device.attributes['gpu.amd.com'].type == 'vfio'
```

---

If you need additional attributes or different representations, please open an
issue discussing your use case.

For troubleshooting missing attributes or unexpected values, see the
[Troubleshooting Guide](troubleshooting.md#expected-attributes-missing-from-resourceslices).
