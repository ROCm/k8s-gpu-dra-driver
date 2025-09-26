#!/usr/bin/env bash
set -exuo pipefail

SCRIPT_DIR="$(cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd)"
source "${SCRIPT_DIR}/common.sh"

TMP_DIR="$(mktemp -d)"; trap 'rm -rf "${TMP_DIR}"' EXIT

cd "${PROJECT_DIR}"

make docker-generate
make -f deployments/container/Makefile "${DRIVER_IMAGE_PLATFORM}"
