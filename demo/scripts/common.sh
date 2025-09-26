#!/usr/bin/env bash

# Copyright 2023 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Demo wrapper common: sources generic scripts/common.sh and adds KIND/GPU helpers.

DEMO_SCRIPT_DIR="$(cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd)"
PROJECT_DIR="$(cd -- "${DEMO_SCRIPT_DIR}/../.." >/dev/null 2>&1 && pwd || true)"
source "${PROJECT_DIR}/scripts/common.sh"

# Demo / KIND specific defaults
: ${KIND_K8S_REPO:="https://github.com/kubernetes/kubernetes.git"}

# The kubernetes tag to build the kind cluster from
# From ${KIND_K8S_REPO}/tags
: ${KIND_K8S_TAG:="v1.34.0"}

# At present, kind has a new enough node image that we don't need to build our
# own. This won't always be true and we may need to set the variable below to
# 'true' from time to time as things change.
: ${BUILD_KIND_IMAGE:="false"}

# The name of the kind cluster to create
: ${KIND_CLUSTER_NAME:="${DRIVER_NAME}-cluster"}
: ${KIND_CLUSTER_CONFIG_PATH:="${DEMO_SCRIPT_DIR}/kind-cluster-config.yaml"}
: ${KIND:="env KIND_EXPERIMENTAL_PROVIDER=docker kind"}

# Derived kind node image reference (namespace local to developer)
: ${KIND_IMAGE:="kindest/node:${KIND_K8S_TAG}"}

check_gpu_device_nodes() {
  local missing=()
  for p in /dev/dri /dev/kfd; do
    if [[ ! -e "$p" ]]; then
      missing+=("$p")
    fi
  done
  if (( ${#missing[@]} > 0 )); then
    echo "[demo/scripts/common.sh] WARN: Missing GPU device paths: ${missing[*]} (demo cluster may not expose GPUs)." >&2
  fi
}
