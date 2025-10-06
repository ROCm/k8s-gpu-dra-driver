# AMD GPU Kubernetes Driver for Dynamic Resource Allocation (DRA)

This repository implements an AMD GPU resource driver for Kubernetes' Dynamic
Resource Allocation (DRA) feature. The driver exposes device classes and
implements allocation and lifecycle behavior for GPU resources on nodes.

> Status: Experimental (alpha). Not recommended for production environments yet.

## DRA Concepts

- Device class: a logical grouping of devices exposed by the driver (for
  example `gpu.amd.com`). Device classes are the API surface workloads request
  from Kubernetes via ResourceClaims.
- ResourceClaim / ResourceClass: the Kubernetes API objects workloads use to
  request DRA-managed resources. The driver receives allocation requests and
  returns device identifiers or access information.
- Allocation lifecycle: the driver can perform setup and teardown when a
  resource is assigned or released. This includes device programming, security
  setup, and publishing device information to the consumer pod's environment.

DRA lets device drivers provide more advanced placement and sharing modes than
traditional device plugins. For expanded background see the upstream docs:
https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/

## Requirements

- Kubernetes 1.32 or newer. Dynamic Resource Allocation (DRA) entered beta in
  Kubernetes 1.32.
- Ensure the DRA APIs are enabled in your cluster version. The examples in this
  repo currently use `resource.k8s.io/v1` which was introduced in Kubernetes 1.34.
  If your cluster only provides `v1beta1`/`v1beta2` (introduced in Kubernetes 1.32/1.33
  respectively), adjust the `apiVersion` accordingly or use a newer Kubernetes release.

## Project layout

- `cmd/` — command binaries (kubelet plugin, webhook, etc.)
- `pkg/` — driver implementation and platform helpers (AMDGPU interactions)
- `deployments/` — manifests and container build Makefile
- `helm-chart-k8s/` — Helm chart source used for packaging
- `demo/` — demo and helper scripts for local testing with `kind` (browse: https://github.com/ROCm/k8s-gpu-dra-driver/tree/main/demo)
- `scripts/` — project-level build and release helpers
- `docs/` — documentation (installation, developer guides) (browse: https://github.com/ROCm/k8s-gpu-dra-driver/tree/main/docs)

## Getting started

Read the Installation & Developer Guide for full, step-by-step installation and developer
workflows (https://github.com/ROCm/k8s-gpu-dra-driver/blob/main/docs/installation.md). Key quick actions:

- Build the driver image (containerized build):

```bash
make build
```

- Package the Helm chart (chart tarball placed in `helm-charts-k8s/`):

```bash
make helm
```

- Create a local `kind` cluster and load the driver image (demo helpers):

```bash
./demo/create-cluster.sh

# When finished
./demo/delete-cluster.sh
```

## Where to find more

- Installation & Developer Guide: https://github.com/ROCm/k8s-gpu-dra-driver/blob/main/docs/installation.md
- Demo scripts: https://github.com/ROCm/k8s-gpu-dra-driver/tree/main/demo
- Build logic: https://github.com/ROCm/k8s-gpu-dra-driver/blob/main/Makefile and https://github.com/ROCm/k8s-gpu-dra-driver/blob/main/deployments/container/Makefile
- Examples: https://github.com/ROCm/k8s-gpu-dra-driver/tree/main/example
 - Demo & Examples Guide: https://github.com/ROCm/k8s-gpu-dra-driver/blob/main/docs/demo.md

## Contributing

See the Contributing section at the end of the Installation & Developer Guide:

- https://github.com/ROCm/k8s-gpu-dra-driver/blob/main/docs/installation.md#contributing

---
