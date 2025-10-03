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
#
# create-cluster.sh
# Builds an optional custom kind node image (if BUILD_KIND_IMAGE=true), creates the
# kind cluster, and loads an existing driver image if present locally.

# A reference to the current directory where this script is located
CURRENT_DIR="$(cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd)"

set -ex
set -o pipefail

source "${CURRENT_DIR}/scripts/common.sh"

# Always attempt to build the kind image
${DEMO_SCRIPT_DIR}/build-kind-image.sh

# Build the driver image if it does not exist locally
if docker image inspect "${DRIVER_IMAGE}" >/dev/null 2>&1; then
	echo "Driver image ${DRIVER_IMAGE} already present locally"
else
	echo "Driver image ${DRIVER_IMAGE} not found locally - invoking 'make build'"
	make build
fi

# Create the kind cluster
${DEMO_SCRIPT_DIR}/create-kind-cluster.sh

# If a driver image already exists load it into the cluster
if docker image inspect "${DRIVER_IMAGE}" >/dev/null 2>&1; then
	echo "Loading driver image ${DRIVER_IMAGE} into kind cluster ${KIND_CLUSTER_NAME}"
	${DEMO_SCRIPT_DIR}/load-driver-image-into-kind.sh
fi

set +x
printf '\033[0;32m'
echo "Cluster creation complete: ${KIND_CLUSTER_NAME}"
printf '\033[0m'
