#!/usr/bin/env bash
set -exuo pipefail

SCRIPT_DIR="$(cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd)"
source "${SCRIPT_DIR}/common.sh"

make -f deployments/container/Makefile push
