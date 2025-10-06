# Installation & Developer Guide

This document collects the common build, package, and demo commands used for
working with the k8s-gpu-dra-driver repository. It pulls together the Makefile
and demo script workflows so you can reproduce builds and demos locally.

## Prerequisites

- GNU Make 3.81+
- Docker with BuildKit support and image build/push capabilities
- helm v3.7.0+
- kubectl

If you plan to build and run the AMD/ROCm CGO code (`pkg/amdgpu`) locally, make
sure the development headers are installed (eg. `libdrm-dev` on Debian/Ubuntu). A
build container packaging the development headers has been provided with Makefile
targets to invoke builds inside the container.

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
helm upgrade -i --create-namespace k8s-gpu-dra-driver k8s-gpu-dra-driver \
  helm-charts-k8s/k8s-gpu-dra-driver-helm-k8s-<version>.tgz

# Or install directly from the chart directory during development
helm install -i --create-namespace k8s-gpu-dra-driver \
  k8s-gpu-dra-driver helm-chart-k8s/ \
  --set image.repository=${DRIVER_IMAGE_REGISTRY}/${DRIVER_IMAGE_NAME} \
  --set image.tag=${DRIVER_IMAGE_TAG}
```

Adjust values via `--set` or by editing `helm-chart-k8s/values.yaml`.

## Demos and examples

For an end-to-end walkthrough of creating a kind cluster, loading the driver image, installing the chart, and running example workloads with verification steps, see:

- https://github.com/ROCm/k8s-gpu-dra-driver/blob/main/docs/demo.md

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