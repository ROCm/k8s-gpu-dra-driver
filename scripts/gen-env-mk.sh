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

src=${1:-env.sh}
out=${2:-env.mk}

if [[ ! -f "$src" ]]; then
  echo "Source env file '$src' not found" >&2
  exit 1
fi

tmp="$out.tmp"
{
  echo '# Copyright (c) Advanced Micro Devices, Inc. All rights reserved.'
  echo '#'
  echo '# Licensed under the Apache License, Version 2.0 (the \"License\");'
  echo '# you may not use this file except in compliance with the License.'
  echo '# You may obtain a copy of the License at'
  echo '#'
  echo '#     http://www.apache.org/licenses/LICENSE-2.0'
  echo '#'
  echo '# Unless required by applicable law or agreed to in writing, software'
  echo '# distributed under the License is distributed on an \"AS IS\" BASIS,'
  echo '# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.'
  echo '# See the License for the specific language governing permissions and'
  echo '# limitations under the License.'
  echo '#'
  echo '# ====================================================================='
  echo '# AUTO-GENERATED from env.sh. DO NOT EDIT. Edit env.sh instead.'
  echo '# ====================================================================='
  awk '
    /^[[:space:]]*:[[:space:]]*\$\{[A-Za-z_][A-Za-z0-9_]*:=/ {
       # Capture key
       match($0, /\$\{([A-Za-z_][A-Za-z0-9_]*)/, m);
       key=m[1];
       # Extract everything after first := up to the matching }
       line=$0;
       sub(/^:[[:space:]]*\$\{[A-Za-z_][A-Za-z0-9_]*:=/, "", line);
       # Remove only the first closing brace at end if present
       sub(/}\s*$/, "", line);
       gsub(/^[[:space:]]+|[[:space:]]+$/, "", line);
       printf "%s ?= %s\n", key, line;
    }
  ' "$src"
} > "$tmp"

mv "$tmp" "$out"
