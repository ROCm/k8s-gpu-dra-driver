# Installation & Developer Guide

This document collects the common build, package, and demo commands used for
working with the k8s-gpu-dra-driver repository. It pulls together the Makefile
and demo script workflows so you can reproduce builds and demos locally.

## Quick Start

Install the DRA driver from the published Helm repository:

```bash
# Install Helm
curl -fsSL -o get_helm.sh https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3
chmod 700 get_helm.sh
./get_helm.sh

# Add the Helm repository
helm repo add rocm-k8s-gpu-dra-driver https://rocm.github.io/k8s-gpu-dra-driver
helm repo update

# Install the DRA driver
helm install k8s-gpu-dra-driver rocm-k8s-gpu-dra-driver/k8s-gpu-dra-driver \
  --namespace kube-amd-gpu \
  --create-namespace
```

## Prerequisites

- GNU Make 3.81+
- Docker with BuildKit support and image build/push capabilities
- helm v3.7.0+
- kubectl

A build container has been provided with Makefile targets to invoke builds inside
the container (e.g., `make docker-build`). If you plan to build locally, you need
a Go toolchain.

## Environment

The repository exposes defaults in `env.sh`. You can override any of these on
the command line or in your shell. Common variables:

- `DRIVER_IMAGE_REGISTRY` — container registry to push images to (default `docker.io/rocm`)
- `DRIVER_IMAGE_NAME` — image name (default project driver name)
- `DRIVER_IMAGE_TAG` — image tag (default derived from chart appVersion or `dev`)
- `KIND_CLUSTER_NAME` — kind cluster name used by demo scripts

Example to override registry and tag for a one-off build:

```bash
DRIVER_IMAGE_REGISTRY=docker.io/rocm DRIVER_IMAGE_TAG=dev make build
```
Note: `env.mk` is a generated Makefile fragment produced from `env.sh` by the Makefile. Do not edit `env.mk` directly — edit `env.sh` and run any Makefile target (e.g., `make build`) to regenerate it.

## Build the driver image

Use the Makefile's `build` target which wraps the repository build script and
containerized build rules. This ensures consistent images and stamping.

```bash
# Build (containerized build or local, as configured in Makefile)
make build

# Build locally without container wrapper (if desired)
make docker-cmds
```

The `build` target depends on `env.mk` (generated from `env.sh`) so ensure any
overrides are passed to `make` or exported in your shell.

## Pushing images and charts to registries

Image pushing is performed by the `Makefile` target `push` (invokes script
`scripts/push-driver-image.sh`). Run `make push` to push images to your driver
registry. Helm charts are available in rocm to install without local builds.

## Package the Helm chart

The repo provides a `helm` Make target that packages the chart under
`helm-charts-k8s/`.

```bash
# Create the packaged chart (tarball in helm-charts-k8s/)
make helm
```

The packaging uses the chart under `helm-chart-k8s/` as the source directory.
The packaging uses the chart under `helm-chart-k8s/` as the source directory.

## Install the driver via Helm

Install the driver using a packaged chart or directly from the chart directory:

```bash
# Install from packaged chart (package created by `make helm`)
helm install --create-namespace --namespace k8s-gpu-dra-driver k8s-gpu-dra-driver \
  helm-charts-k8s/k8s-gpu-dra-driver-helm-k8s-<version>.tgz

# Or install directly from the chart directory during development
helm install --create-namespace --namespace k8s-gpu-dra-driver \
  k8s-gpu-dra-driver helm-chart-k8s/ \
  --set image.repository=${DRIVER_IMAGE_REGISTRY}/${DRIVER_IMAGE_NAME} \
  --set image.tag=${DRIVER_IMAGE_TAG}
```

Adjust values via `--set` or by editing `helm-chart-k8s/values.yaml`.

## Demos and examples

For an end-to-end walkthrough of creating a kind cluster, loading the driver image, installing the chart, and running example workloads with verification steps, see:

- https://github.com/ROCm/k8s-gpu-dra-driver/blob/main/docs/demo.md

## Helm Configuration Reference

### Driver prerequisite

The DRA driver relies on the `amdgpu` kernel driver to enumerate GPU devices.
The Helm chart enforces this by running a `driver-init` init container in the
kubelet plugin DaemonSet pod. This container polls `/sys/class/kfd` and
`/sys/module/amdgpu/drivers/` and blocks the plugin from starting until the
kernel driver is loaded.

### VFIO passthrough

VFIO passthrough enables GPU allocation to VMs via KubeVirt or similar
frameworks. All VFIO functionality requires the `VFIOPassthrough` feature gate.

**Enabling VFIO:**

```bash
# CLI
--feature-gates=VFIOPassthrough=true

# Helm
featureGates:
  VFIOPassthrough: true
```

**Host prerequisites:**

- **IOMMU enabled** — set `amd_iommu=on` (or `intel_iommu=on`) in kernel cmdline
- **vfio_pci module loaded** — `modprobe vfio_pci` or add to `/etc/modules-load.d/`
- **GIM driver** (SR-IOV VF mode) — the AMD GPU-IOV Module (`gim`) must be loaded
  on the PF for VF discovery. VFs are created via GIM's SR-IOV interface.
- **CAP_SYS_ADMIN** — the kubelet plugin pod requires privileged mode (default in
  the Helm chart) for sysfs driver bind/unbind operations
- **IOMMU groups** — each VF must be in its own IOMMU group for safe passthrough.
  Check with `ls /sys/kernel/iommu_groups/`

Without IOMMU enabled, the VFIO manager will not initialize and no VFIO devices
will be advertised. GPU compute devices remain available.

**Usage:**

To allocate a VFIO GPU, create a ResourceClaim with a `VfioDeviceConfig`:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: gpu-vfio
spec:
  devices:
    requests:
    - name: gpu
      exactly:
        deviceClassName: gpu.amd.com
    config:
    - opaque:
        driver: gpu.amd.com
        parameters:
          apiVersion: gpu.resource.amd.com/v1alpha1
          kind: VfioDeviceConfig
```

The driver binds the allocated VF to `vfio-pci` during Prepare and unbinds
on release. VFIO devices appear in the ResourceSlice with `type = vfio`.

**Dual-entry advertising:**

When `VFIOPassthrough` is enabled, each compute GPU also appears as a
`type=vfio` device in the ResourceSlice. You can claim a VFIO GPU directly
by selecting `type=vfio` without using a `VfioDeviceConfig`:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: gpu-vfio-direct
spec:
  devices:
    requests:
    - name: gpu
      exactly:
        deviceClassName: gpu.amd.com
        selectors:
        - cel:
            expression: 'device.attributes["gpu.amd.com"].type == "vfio"'
```

This is the simpler path for VFIO allocation. The `VfioDeviceConfig` approach
(shown above) is still supported for on-demand conversion of compute GPUs.

When either the compute or VFIO entry is allocated, the other is temporarily
removed from the ResourceSlice. It returns when the claim is released.

### Key values

| Value | Default | Description |
|-------|---------|-------------|
| `image.repository` | `docker.io/rocm/k8s-gpu-dra-driver` | Driver container image repository |
| `image.tag` | Chart `appVersion` | Driver container image tag |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy |
| `kubeletPlugin.containers.init.image` | `busybox:1.36` | Init container image used for the driver readiness check |
| `kubeletPlugin.containers.init.securityContext` | `{privileged: true}` | Security context for the init container |
| `kubeletPlugin.containers.init.resources` | `{}` | Resource requests/limits for the init container |
| `kubeletPlugin.containers.plugin.securityContext` | `{privileged: true}` | Security context for the plugin container |
| `kubeletPlugin.containers.plugin.resources` | `{}` | Resource requests/limits for the plugin container |
| `kubeletPlugin.containers.plugin.healthcheckPort` | `51515` | gRPC health check port; set negative to disable |
| `kubeletPlugin.nodeSelector` | `{}` | Node selector for the kubelet plugin DaemonSet |
| `kubeletPlugin.tolerations` | `[]` | Tolerations for the kubelet plugin DaemonSet |
| `kubeletPlugin.priorityClassName` | `system-node-critical` | Priority class for the kubelet plugin pods |
| `cdi.dynamicPath` | `/var/run/cdi` | Host path for CDI spec files |

## Contributing

We welcome issues, bug reports, and PRs of any size.

### Before you start
- Search existing issues/PRs to avoid duplicates.
- Open an issue to discuss substantial changes before coding.

### Creating a Pull Request
1. Fork the repository on GitHub.
2. Create a new branch for your changes.
3. Make your changes and commit them with clear, descriptive messages.
4. Push your changes to your fork.
5. Open a pull request against the main repository and link related issues.

Please ensure your code follows our coding standards and includes appropriate tests.

### Coding and docs
- Keep PRs focused and small when possible.
- Update docs, examples, and Helm values when behavior or flags change.
- Run formatting and linters locally; ensure builds are clean.
- Add or update unit/integration tests for new behavior.

### Commit and review
- Use descriptive titles; include context in the PR description.
- Reference issues (e.g., “Fixes #123”).
- Sign your commits.
- Address review feedback promptly; squash commits if requested.

---