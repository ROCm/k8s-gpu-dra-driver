# Copyright (c) Advanced Micro Devices, Inc. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the \"License\");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an \"AS IS\" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Project-level environment defaults for k8s-gpu-dra-driver
# Override any of these by exporting them in your shell before running scripts.

# Basic driver identity
: ${DRIVER_NAME:=k8s-gpu-dra-driver}
: ${MODULE:=github.com/ROCm/${DRIVER_NAME}}

# Versioning / metadata
: ${VERSION:=v0.1.0}
: ${VENDOR:=amd.com}
: ${APIS:=gpu/v1alpha1}

# Toolchain
: ${GOLANG_VERSION:=1.24.2}
: ${BUILDIMAGE_TAG:=v1.0}

# Container/image defaults
: ${DRIVER_IMAGE_REGISTRY:=docker.io/rocm}
: ${DRIVER_IMAGE_NAME:="${DRIVER_NAME}"}
: ${DRIVER_IMAGE_PLATFORM:=ubi-minimal-9.6}
: ${DRIVER_IMAGE_TAG:="${VERSION}"}

# Helm/chart defaults
: ${DRIVER_CHART_REGISTRY:=docker.io/rocm}

# Kind defaults
: ${KIND_K8S_REPO:=https://github.com/kubernetes/kubernetes.git}
: ${KIND_K8S_TAG:=v1.34.0}
: ${BUILD_KIND_IMAGE:=false}
: ${KIND_CLUSTER_NAME:=${DRIVER_NAME}-cluster}

# Defaults for Makefile/CI gates (these can be overridden externally)
: ${HELM:="go run helm.sh/helm/v3/cmd/helm@latest"}
