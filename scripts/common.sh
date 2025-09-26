#!/usr/bin/env bash
# Generic build/release environment helpers

set -o pipefail

SCRIPT_DIR="$(cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd)"
PROJECT_DIR="$(cd -- "${SCRIPT_DIR}/.." >/dev/null 2>&1 && pwd || true)"
if [[ -z "${PROJECT_DIR}" || ! -d "${PROJECT_DIR}" ]]; then
  PROJECT_DIR="$(git rev-parse --show-toplevel 2>/dev/null || echo "${SCRIPT_DIR}/..")"
fi
# Source project-level env defaults if present (allows overriding behavior centrally)
if [[ -f "${PROJECT_DIR}/env.sh" ]]; then
  # shellcheck disable=SC1090
  source "${PROJECT_DIR}/env.sh"
fi

# Mandatory tools check
if ! command -v docker >/dev/null 2>&1; then
  echo "ERROR: 'docker' is required but not found in PATH." >&2
  return 1
fi

# Optional helm check (caller may export REQUIRE_HELM=true)
if ! command -v helm >/dev/null 2>&1; then
  if [[ "${REQUIRE_HELM:-false}" == "true" ]]; then
    echo "ERROR: 'helm' is required but not found in PATH." >&2
    return 1
  fi
fi

: ${DRIVER_NAME:=k8s-gpu-dra-driver}
: ${DRIVER_IMAGE_REGISTRY:="docker.io/rocm"}
: ${DRIVER_IMAGE_NAME:="${DRIVER_NAME}"}
: ${DRIVER_IMAGE_PLATFORM:="ubi-minimal-9.6"}
: ${DRIVER_CHART_REGISTRY:="docker.io/rocm"}

# Derive image tag from chart appVersion (fallback to dev) unless provided.
if [[ -z "${DRIVER_IMAGE_TAG:-}" ]]; then
  local_chart_path="${PROJECT_DIR}/helm-charts-k8s"
  tag_candidate=""
  if command -v helm >/dev/null 2>&1 && [[ -f "${local_chart_path}/Chart.yaml" ]]; then
    tag_candidate="$(helm show chart "${local_chart_path}" 2>/dev/null | sed -n 's/^appVersion: //p')"
  fi
  if [[ -z "${tag_candidate}" ]]; then
    tag_candidate="dev"
    echo "[scripts/common.sh] WARN: Could not derive appVersion; using '${tag_candidate}'." >&2
  fi
  DRIVER_IMAGE_TAG="${tag_candidate}"
fi

: ${DRIVER_IMAGE:="${DRIVER_IMAGE_REGISTRY}/${DRIVER_IMAGE_NAME}:${DRIVER_IMAGE_TAG}"}

export PROJECT_DIR DRIVER_IMAGE DRIVER_IMAGE_TAG DRIVER_IMAGE_NAME DRIVER_IMAGE_REGISTRY DRIVER_IMAGE_PLATFORM DRIVER_CHART_REGISTRY DRIVER_NAME
