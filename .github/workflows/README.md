# CI/CD Workflows

This repository uses three GitHub Actions workflows that together handle continuous integration, pre-release image builds, and production releases. All workflows delegate their heavy lifting to reusable workflows in [ROCm/common-infra-operator](https://github.com/ROCm/common-infra-operator).

## Workflows

### CI (`ci.yaml`)

**Trigger:** Pull requests targeting `develop`, `main`, or `release-v*` branches.

Runs two jobs in parallel:

| Job | What it does |
|-----|-------------|
| **Lint** | Runs `make check` which executes `golangci-lint`, `ineffassign`, and format checks. |
| **Test** | Runs `make test` to execute the Go test suite. |

### Image (`image.yaml`)

**Trigger:** Pushes to `develop` or `release-v*` branches, and pull requests with the `ci-run` label.

Uses a concurrency group per branch to serialize builds and prevent tag race conditions.

| Job | When it runs | What it does |
|-----|-------------|-------------|
| **Auto Tag** | Push events only | Computes and pushes an auto-incrementing git tag. On `develop` this produces `develop-N` tags. On `release-v*` branches this produces `vX.Y.Z-N` build tags. |
| **Build & Push** | Push events (after Auto Tag) | Builds the container image and pushes it to `docker.io/amdpsdo/k8s-gpu-dra-driver:<tag>`. |
| **PR Build** | PRs labeled `ci-run` | Builds the container image tagged `pr-<number>` **without pushing** — validates that the image builds successfully. |

### Release (`release.yaml`)

**Trigger:** Pushed tags matching clean semver format `vX.Y.Z` (e.g. `v1.0.0`). Build tags like `v1.0.0-3` are excluded.

Runs three jobs:

| Job | What it does |
|-----|-------------|
| **Release Image** | Builds and pushes the container image to `docker.io/rocm/k8s-gpu-dra-driver:<version>` (the production registry). |
| **Publish Helm Chart** | Packages the Helm chart from `helm-charts-k8s/`, then commits it to the `gh-pages` branch to update the Helm repository at `https://rocm.github.io/k8s-gpu-dra-driver`. |
| **Draft Release** | Waits for the image and Helm chart to publish, then creates a draft GitHub Release with auto-generated release notes and the packaged Helm chart `.tgz` attached as an asset. |

## Lifecycle Summary

```
PR opened/updated
  └─► CI: lint + test
  └─► Image (if labeled "ci-run"): build without push

Merge to develop
  └─► Image: auto-tag (develop-N) → build & push to amdpsdo

Merge to release-v*
  └─► Image: auto-tag (vX.Y.Z-N) → build & push to amdpsdo

Push tag vX.Y.Z
  └─► Release: build & push to rocm → publish Helm chart → draft GitHub release
```