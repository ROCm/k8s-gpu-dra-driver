#!/usr/bin/env bash
set -euo pipefail

export REQUIRE_HELM=true
SCRIPT_DIR="$(cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd)"
source "${SCRIPT_DIR}/common.sh"

CHART_DIR="${PROJECT_DIR}/helm-charts-k8s"
CHART_NAME="${DRIVER_NAME}"

if [[ -z "${CHART_VERSION:-}" ]]; then
  if [[ -f "${CHART_DIR}/Chart.yaml" ]]; then
    if command -v helm >/dev/null 2>&1; then
      CHART_VERSION="$(helm show chart "${CHART_DIR}" 2>/dev/null | sed -n 's/^version: //p')"
    else
      echo "ERROR: helm binary not found in PATH; cannot derive CHART_VERSION" >&2
    fi
  fi
  if [[ -z "${CHART_VERSION}" ]]; then
    echo "ERROR: Could not derive CHART_VERSION; set CHART_VERSION explicitly." >&2
    exit 1
  fi
fi

echo "Packaging Helm chart ${CHART_NAME} version ${CHART_VERSION} from ${CHART_DIR}" >&2
helm package --version "${CHART_VERSION}" --app-version "${CHART_VERSION}" "${CHART_DIR}" >/dev/null
echo "${CHART_NAME}-${CHART_VERSION}.tgz"
