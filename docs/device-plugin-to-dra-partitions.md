# Migrating partitioned-GPU workloads from the device plugin to DRA

This guide is for users who today request **partitioned** AMD GPUs through the
[AMD GPU device plugin](https://github.com/ROCm/k8s-device-plugin) using extended
resource names such as `amd.com/cpx_nps4`, and want to move those workloads to the
AMD GPU **DRA driver** with the `AutoPartition` feature.

## Background: how the device plugin names partitions

The device plugin advertises GPUs as Kubernetes *extended resources* under the
`amd.com` namespace. Its resource-naming behaviour depends on the
`resource_naming_strategy` flag:

| Strategy | Node layout | Advertised resource(s) | Pod request |
|---|---|---|---|
| `single` | any | `amd.com/gpu` | `amd.com/gpu: 1` |
| `mixed` (homogeneous node) | all GPUs in the same partition mode | `amd.com/<compute>_<memory>` | `amd.com/cpx_nps4: 1` |
| `mixed` (heterogeneous node) | GPUs in different partition modes | one name per mode present, e.g. `amd.com/cpx_nps4`, `amd.com/dpx_nps2` | the matching name |

The partition-specific name is `amd.com/<computePartition>_<memoryPartition>`,
lowercased — for example `amd.com/cpx_nps4`, `amd.com/dpx_nps2`, `amd.com/spx_nps1`.
`spx_nps1` is a full, unpartitioned GPU.

Under this model the **hardware is partitioned out-of-band** (statically, or by the
Device Config Manager), and the plugin only reports whatever partitions already
exist. A pod asks for one by extended-resource name and relies on node selectors to
land where that name is advertised.

## What changes under the DRA driver

The DRA driver does **not** mirror the per-partition extended resource names
(`amd.com/cpx_nps4`, …). There is a single umbrella DeviceClass, `gpu.amd.com`,
which keeps the classic `amd.com/gpu` extended resource for whole-GPU requests. A
specific *partition configuration* is expressed in the **ResourceClaim** instead,
using CEL selectors against the device's `computePartition` and `memoryPartition`
attributes.

With the `AutoPartition` feature gate enabled, the driver also *creates* the
requested partition on demand at claim-prepare time — you no longer pre-partition
the hardware yourself (and you must **not** run DCM on the same nodes; see the
[auto-partition documentation](driver-attributes.md#auto-partition-mode-virtual-partition-devices-beta)).

> The DRA claim is strictly more expressive than the extended-resource name: it
> selects by attributes, requests capacity, and (with auto-partition) provisions
> the layout — all in one object.

## Mapping table

| Device-plugin request | DRA equivalent |
|---|---|
| `amd.com/gpu: 1` | ResourceClaim, `deviceClassName: gpu.amd.com` (optionally select `computePartition == "spx"` for a full GPU) |
| `amd.com/spx_nps1: 1` | full GPU — `computePartition == "spx" && memoryPartition == "nps1"` |
| `amd.com/dpx_nps1: 1` | `computePartition == "dpx" && memoryPartition == "nps1"` |
| `amd.com/dpx_nps2: 1` | `computePartition == "dpx" && memoryPartition == "nps2"` |
| `amd.com/qpx_nps1: 1` | `computePartition == "qpx" && memoryPartition == "nps1"` |
| `amd.com/cpx_nps1: 1` | `computePartition == "cpx" && memoryPartition == "nps1"` |
| `amd.com/cpx_nps4: 1` | `computePartition == "cpx" && memoryPartition == "nps4"` |

## Example: `amd.com/cpx_nps4` → DRA ResourceClaim

### Before (device plugin)

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: gpu-workload
spec:
  containers:
  - name: workload
    image: my-image
    resources:
      limits:
        amd.com/cpx_nps4: 1     # requires the node to already be in cpx/nps4
  nodeSelector:
    amd.com/compute-memory-partition: cpx_nps4   # land on a matching node
```

### After (DRA driver, auto-partition)

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: cpx-nps4-partition
spec:
  devices:
    requests:
    - name: gpu
      exactly:
        deviceClassName: gpu.amd.com
        selectors:
        - cel:
            expression: >-
              device.attributes["gpu.amd.com"].computePartition == "cpx" &&
              device.attributes["gpu.amd.com"].memoryPartition == "nps4"
        capacity:
          requests:
            partitions: "1"
---
apiVersion: v1
kind: Pod
metadata:
  name: gpu-workload
spec:
  containers:
  - name: workload
    image: my-image
    resources:
      claims:
      - name: gpu
  resourceClaims:
  - name: gpu
    resourceClaimName: cpx-nps4-partition
```

The driver selects a GPU that can provide a `cpx/nps4` partition, reconfigures the
hardware to that layout at prepare time, and injects the partition's device nodes
into the container. No node selector or pre-partitioning is required — the driver
finds a capable GPU and partitions it on demand.

A ready-to-run example lives at
[`example/auto-partition-cpx-nps4.yaml`](../example/auto-partition-cpx-nps4.yaml).

## Notes on behaviour differences

- **No pre-partitioning / no DCM.** With auto-partition the driver owns
  partitioning. Do not statically partition the nodes and do not run the Device
  Config Manager on them.
- **`amd.com/gpu` still works** for whole-GPU requests via the `gpu.amd.com`
  DeviceClass's `extendedResourceName`. Homogeneous, pre-partitioned nodes that
  only ever asked for `amd.com/gpu` continue to work; use node selectors to target
  the desired nodes as before.
- **Per-partition extended resource names are not provided by the DRA driver.**
  If you specifically need `amd.com/cpx_nps4`-style extended resources (rather than
  DRA claims), that remains a device-plugin capability. There is no plan to add
  per-partition DeviceClasses/extended resources to the DRA driver unless a concrete
  request arises — the DRA claim above is the supported path.
- **Requesting multiple partitions.** The `partitions` capacity on a device is
  consumable: several claims can share one partition configuration on the same GPU
  up to the partition count (e.g. 8 for CPX). See
  [Capacity values for auto-partition devices](driver-attributes.md#capacity-values-for-auto-partition-devices).

## See also

- [Device attributes and capabilities](driver-attributes.md) — full attribute and
  capacity reference, including the auto-partition section.
- [Demo & examples](demo.md) — end-to-end walkthrough including auto-partition mode.
