# AMD GPU Kubernetes Driver for Dynamic Resource Allocation (DRA)

This repository implements an AMD GPU resource driver for Kubernetes' Dynamic
Resource Allocation (DRA) feature. The driver exposes device classes and
implements allocation and lifecycle behavior for GPU resources on nodes.

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

## Project layout

- `cmd/` — command binaries (kubelet plugin, webhook, etc.)
- `pkg/` — driver implementation and platform helpers (AMDGPU interactions)
- `deployments/` — manifests and container build Makefile
- `helm-chart-k8s/` — Helm chart source used for packaging
- `demo/` — demo and helper scripts for local testing with `kind`
- `scripts/` — project-level build and release helpers
- `docs/` — documentation (installation, developer guides)

## Getting started

Read `docs/installation.md` for full, step-by-step installation and developer
workflows. Key quick actions:

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

- Detailed installation & developer guide: `docs/installation.md`
- Demo scripts: `demo/`
- Build logic: `Makefile` and `deployments/container/Makefile`

## Contributing

See `CONTRIBUTING.md` for how to contribute, coding standards, and the code of
conduct.

---
