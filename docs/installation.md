# Installation & Developer Guide

This document collects the common build, package, and demo commands used for
working with the k8s-gpu-dra-driver repository. It pulls together the Makefile
and demo script workflows so you can reproduce builds and demos locally.

## Prerequisites

- GNU Make 3.81+
- Docker with BuildKit support and image build/push capabilities
- kind (v0.17.0+)
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

## Demo

This section groups the demo workflow into three ordered steps you can follow
when evaluating the driver locally.

### Step 1 — Create / Delete a kind cluster

Use the provided demo scripts to create a local kind cluster. The create
script will build a kind node image (if needed), build the driver image
(if missing), create the cluster, and then load the driver image into the
cluster.

```bash
# Create cluster (builds kind image, builds driver image if needed, creates cluster, loads image)
./demo/create-cluster.sh

# Delete cluster
./demo/delete-cluster.sh
```

The demo scripts use the layered `scripts/common.sh` (project-level) and
`demo/scripts/common.sh` (demo-level) to derive variables like `KIND_IMAGE`,
`KIND_CLUSTER_NAME` and `DRIVER_IMAGE`. You can override environment variables
before running the scripts to tweak behavior.

### Step 2 — (Optional) Load a locally built driver image into kind

If you prefer to build the driver image separately (for example, with
`make build`) and then load it into the cluster, use the load helper or run
the steps manually:

```bash
# Save and load into kind (works with docker/podman)
docker save -o driver_image.tar ${DRIVER_IMAGE}
kind load image-archive --name ${KIND_CLUSTER_NAME} driver_image.tar
rm driver_image.tar
```

The demo helper `demo/scripts/load-driver-image-into-kind.sh` runs this flow
for you.

### Step 3 — Install the driver via Helm

After you have a running cluster (and the image available in the cluster
nodes), install the driver using the packaged chart or directly from the
chart directory:

```bash
# Install from packaged chart (package created by `make helm`)
helm install k8s-gpu-dra-driver helm-charts-k8s/k8s-gpu-dra-driver-helm-k8s-<version>.tgz

# Or install directly from the chart directory during development
helm install k8s-gpu-dra-driver helm-chart-k8s/ \
	--set image.repository=${DRIVER_IMAGE_REGISTRY}/${DRIVER_IMAGE_NAME} \
	--set image.tag=${DRIVER_IMAGE_TAG}
```

Adjust values via `--set` or by editing `helm-chart-k8s/values.yaml`.

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