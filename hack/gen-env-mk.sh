#!/usr/bin/env bash
set -euo pipefail

src=${1:-env.sh}
out=${2:-env.mk}

if [[ ! -f "$src" ]]; then
  echo "Source env file '$src' not found" >&2
  exit 1
fi

tmp="$out.tmp"
{
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
