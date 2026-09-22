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
# theRock (rockrel) distribution tarball. Version-guarded: a no-op unless the
# version in the tarball filename differs from third_party/amd_smi/lib/.version.
#
# ROCM_TARBALL_URL is read from env.sh (sourced via common.sh). Override it there
# (or export it) to move to a different ROCm release, then run
# `make rocm-tarball-fetch`. Set ROCM_TARBALL_FORCE=1 to re-pull the same version.

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
VERSION_FILE="${LIB_DIR}/.version"

# The rocm_sysdeps libraries libamd_smi.so needs. The tarball ships ~40; only
# these are vendored.
#
# DT_NEEDED alone is not enough. The netlink trio below is linked and shows up in
# `readelf -d libamd_smi.so`, but the DRM pair is dlopen'd at runtime and appears
# nowhere in the ELF headers — `strings libamd_smi.so | grep ^lib` is what
# surfaces it. Omitting it does not fail the build or the link: amd-smi starts,
# then silently returns garbage from the queries backed by DRM (device BDFs came
# back as uninitialised memory), so check both when adding a ROCm release.
#
# librocm_sysdeps_drm_amdgpu.so.1 additionally needs librocm_sysdeps_drm.so.2,
# and nl_genl_3 needs nl_3.
AMDSMI_SYSDEPS=(
  'librocm_sysdeps_nl_3.so*'
  'librocm_sysdeps_nl_genl_3.so*'
  'librocm_sysdeps_mnl.so*'
  'librocm_sysdeps_drm.so*'
  'librocm_sysdeps_drm_amdgpu.so*'
)

# The tarball filename is the version, and it distinguishes builds the library
# itself cannot: 10.0.0rc2 and 10.0.0 GA ship the same soname (.27), so only the
# name tells them apart. Record it next to the libraries and compare on each run.
#
# .../therock-dist-linux-multiarch-10.0.0.tar.gz -> 10.0.0
want_version="$(basename "${ROCM_TARBALL_URL}")"
want_version="${want_version#therock-dist-linux-multiarch-}"
want_version="${want_version%.tar.gz}"

have_version=""
[[ -f "${VERSION_FILE}" ]] && have_version="$(cat "${VERSION_FILE}")"

# ROCM_TARBALL_FORCE=1 re-pulls even when the versions agree, for the case the
# filename cannot see: upstream respinning a tarball under the same URL.
if [[ -z "${ROCM_TARBALL_FORCE:-}" \
      && "${have_version}" == "${want_version}" \
      && -e "${LIB_DIR}/libamd_smi.so" ]]; then
  echo "amd-smi ${want_version} already vendored under third_party/amd_smi; nothing to do."
  echo "  Set ROCM_TARBALL_FORCE=1 to re-pull the same version."
  exit 0
fi

echo "Refreshing amd-smi: ${have_version:-<none>} -> ${want_version}${ROCM_TARBALL_FORCE:+ (forced)}"
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
rm -f "${LIB_DIR}"/libamd_smi.so* "${LIB_DIR}"/librocm_sysdeps_*.so* "${LIB_DIR}"/libdrm*.so*
cp -a "${stage}"/lib/libamd_smi.so* "${LIB_DIR}/"
for pattern in "${AMDSMI_SYSDEPS[@]}"; do
  cp -a "${stage}"/lib/rocm_sysdeps/lib/${pattern} "${LIB_DIR}/" 2>/dev/null || true
done
cp -a "${stage}"/include/amd_smi/amdsmi.h "${INCLUDE_DIR}/amdsmi.h"

# amd-smi dlopens the DRM backend by either name, trying librocm_sysdeps_drm_*
# first and falling back to the classic libdrm_* ones. The tarball ships the
# latter only as symlinks in a directory we do not copy wholesale, so recreate
# them next to their targets.
ln -sf librocm_sysdeps_drm.so.2 "${LIB_DIR}/libdrm.so"
ln -sf librocm_sysdeps_drm_amdgpu.so.1 "${LIB_DIR}/libdrm_amdgpu.so"

# Record the vendored version; this is what the no-op check above compares.
echo "${want_version}" > "${VERSION_FILE}"

# Report the amdsmi build the tarball was cut from. Printed rather than stored:
# it identifies the artifact for the commit message, but nothing reads it back.
amdsmi_build=""
if [[ -f "${stage}/libexec/amdsmi_cli/_version.py" ]]; then
  amdsmi_build="$(sed -n 's/.*__version__ *= *"\([^"]*\)".*/\1/p' \
    "${stage}/libexec/amdsmi_cli/_version.py" | head -1)"
fi

echo "amd-smi ${want_version} vendored into third_party/amd_smi${amdsmi_build:+ (amdsmi ${amdsmi_build})}."
