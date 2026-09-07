#!/usr/bin/env bash

# Copyright (c) Advanced Micro Devices, Inc. All rights reserved.
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
# Refresh the vendored AMD SMI libraries under third_party/amd_smi from the ROCm
# theRock (rockrel) distribution tarball. A no-op when a library is already
# vendored, so it never downloads by accident.
#
# ROCM_TARBALL_URL is read from env.sh (sourced via common.sh). Override it there
# (or export it) to move to a different ROCm release, then run
# `ROCM_TARBALL_FORCE=1 make rocm-tarball-fetch` — the force flag is required
# because the no-op check cannot see a version change within the same soname.

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"
# common.sh sources env.sh, giving us PROJECT_DIR and ROCM_TARBALL_URL.
# shellcheck source=/dev/null
source "${SCRIPT_DIR}/common.sh"

if [[ -z "${ROCM_TARBALL_URL:-}" ]]; then
  echo "ERROR: ROCM_TARBALL_URL is not set (define it in env.sh)." >&2
  exit 1
fi

AMDSMI_DIR="${PROJECT_DIR}/third_party/amd_smi"
LIB_DIR="${AMDSMI_DIR}/lib"
INCLUDE_DIR="${AMDSMI_DIR}/include"

# The rocm_sysdeps netlink libraries that libamd_smi.so DT_NEEDEDs (verified with
# `readelf -d libamd_smi.so`). The tarball ships ~40 sysdeps libs; AMD SMI links
# only this netlink trio (nl_genl_3 additionally needs nl_3).
AMDSMI_SYSDEPS=(
  'librocm_sysdeps_nl_3.so*'
  'librocm_sysdeps_nl_genl_3.so*'
  'librocm_sysdeps_mnl.so*'
)

# Derive the desired version from the tarball URL, e.g.
# .../therock-dist-linux-multiarch-10.0.0.tar.gz -> 10.0.0
want_version="$(basename "${ROCM_TARBALL_URL}")"
want_version="${want_version#therock-dist-linux-multiarch-}"
want_version="${want_version%.tar.gz}"

# The vendored version is not tracked in a separate marker file: ROCM_TARBALL_URL
# in env.sh already names it, and a second copy only invites the two to disagree.
# What is on disk is identified by the library soname instead, which comes from
# the artifact itself.
#
# The soname is coarse (it only changes on a major bump, so 10.0.0rc2 and 10.0.0
# GA both report .27), so it cannot tell two builds of the same major apart. That
# is what ROCM_TARBALL_FORCE is for: bumping ROCM_TARBALL_URL within a major, or
# re-pulling after upstream respins a tarball under the same URL, needs
# ROCM_TARBALL_FORCE=1. Refreshing when nothing changed is harmless, so the
# default stays cheap and offline.
have_soname=""
if [[ -e "${LIB_DIR}/libamd_smi.so" ]]; then
  have_soname="$(readlink "${LIB_DIR}/libamd_smi.so" 2>/dev/null || true)"
fi

if [[ -z "${ROCM_TARBALL_FORCE:-}" && -n "${have_soname}" ]]; then
  echo "amd-smi already vendored under third_party/amd_smi (${have_soname}); nothing to do."
  echo "  ROCM_TARBALL_URL: ${ROCM_TARBALL_URL}"
  echo "  Set ROCM_TARBALL_FORCE=1 to re-pull (required when changing versions"
  echo "  within the same soname, e.g. an rc -> GA bump)."
  exit 0
fi

echo "Refreshing amd-smi -> ${want_version}${ROCM_TARBALL_FORCE:+ (forced)}"
echo "  source: ${ROCM_TARBALL_URL}"

stage="$(mktemp -d)"
trap 'rm -rf "${stage}"' EXIT

# Stream-extract only the amd-smi bits from the (large) multi-arch tarball.
curl -fSL "${ROCM_TARBALL_URL}" \
  | tar -xz -C "${stage}" --wildcards --no-anchored \
      'amdsmi.h' 'libamd_smi.so*' 'librocm_sysdeps_*.so*' 'amdsmi_cli/_version.py'

mkdir -p "${LIB_DIR}" "${INCLUDE_DIR}"

# Refresh the library, its required sysdeps, and the header. Clear old libs first
# so a soname bump doesn't leave stale files behind.
rm -f "${LIB_DIR}"/libamd_smi.so* "${LIB_DIR}"/librocm_sysdeps_*.so*
cp -a "${stage}"/lib/libamd_smi.so* "${LIB_DIR}/"
for pattern in "${AMDSMI_SYSDEPS[@]}"; do
  cp -a "${stage}"/lib/rocm_sysdeps/lib/${pattern} "${LIB_DIR}/" 2>/dev/null || true
done
cp -a "${stage}"/include/amd_smi/amdsmi.h "${INCLUDE_DIR}/amdsmi.h"

# Report the amdsmi build the tarball actually carried, so the refresh output
# records what was vendored without keeping a version file in the tree.
amdsmi_build=""
if [[ -f "${stage}/libexec/amdsmi_cli/_version.py" ]]; then
  amdsmi_build="$(sed -n 's/.*__version__ *= *"\([^"]*\)".*/\1/p' \
    "${stage}/libexec/amdsmi_cli/_version.py" | head -1)"
fi

echo "amd-smi ${want_version} vendored into third_party/amd_smi${amdsmi_build:+ (amdsmi ${amdsmi_build})}."
