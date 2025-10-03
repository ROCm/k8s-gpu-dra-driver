#!/usr/bin/env bash

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

set -euo pipefail

export REQUIRE_HELM=true
SCRIPT_DIR="$(cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd)"
source "${SCRIPT_DIR}/common.sh"

CHART_DIR="${PROJECT_DIR}/helm-charts-k8s"
CHART_NAME="${DRIVER_NAME}"

if [[ -z "${VERSION:-}" ]]; then
  if [[ -f "${CHART_DIR}/Chart.yaml" ]]; then
    if command -v helm >/dev/null 2>&1; then
      VERSION="$(helm show chart "${CHART_DIR}" 2>/dev/null | sed -n 's/^version: //p')"
    else
      echo "ERROR: helm binary not found in PATH; cannot derive VERSION" >&2
    fi
  fi
  if [[ -z "${VERSION}" ]]; then
    echo "ERROR: Could not derive VERSION; set VERSION explicitly." >&2
    exit 1
  fi
fi

echo "Packaging Helm chart ${CHART_NAME} version ${VERSION} from ${CHART_DIR}" >&2
helm package --version "${VERSION}" --app-version "${VERSION}" "${CHART_DIR}" >/dev/null
echo "${CHART_NAME}-${VERSION}.tgz"
