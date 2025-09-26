# =====================================================================
# AUTO-GENERATED from env.sh. DO NOT EDIT. Edit env.sh instead.
# =====================================================================
DRIVER_NAME ?= k8s-gpu-dra-driver
MODULE ?= github.com/ROCm/${DRIVER_NAME}
VERSION ?= v0.1.0
VENDOR ?= amd.com
APIS ?= gpu/v1alpha1
GOLANG_VERSION ?= 1.24.2
BUILDIMAGE_TAG ?= v1.0
DRIVER_IMAGE_REGISTRY ?= docker.io/rocm
DRIVER_IMAGE_NAME ?= "${DRIVER_NAME}"
DRIVER_IMAGE_PLATFORM ?= ubi-minimal-9.6
DRIVER_CHART_REGISTRY ?= docker.io/rocm
KIND_K8S_REPO ?= https://github.com/kubernetes/kubernetes.git
KIND_K8S_TAG ?= v1.34.0
BUILD_KIND_IMAGE ?= false
KIND_CLUSTER_NAME ?= ${DRIVER_NAME}-cluster
HELM ?= "go run helm.sh/helm/v3/cmd/helm@latest"
