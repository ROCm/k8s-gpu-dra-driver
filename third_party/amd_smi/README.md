# AMD SMI (vendored)

The DRA driver links against AMD SMI (`libamd_smi`) via cgo (see `pkg/amdsmi`). This directory
vendors the prebuilt library, its runtime dependencies, and the public header.

## Contents

| Path | Description |
|---|---|
| `include/amdsmi.h` | Public AMD SMI C header (MIT licensed). Used by the cgo `#include`. |
| `lib/libamd_smi.so*` | Prebuilt AMD SMI shared library (+ soname symlinks). |
| `lib/librocm_sysdeps_{nl_3,nl_genl_3,mnl}.so*` | The rocm_sysdeps netlink libraries that `libamd_smi.so` `DT_NEEDED`s (verified with `readelf -d`). Only these are vendored; the upstream tarball ships ~40 sysdeps libs but AMD SMI links just this netlink trio. |
| `lib/.version` | ROCm version marker, used by `scripts/update-amdsmi.sh` to decide whether a refresh is needed. |

## Provenance

These files are extracted from the ROCm **theRock** (rockrel) multi-arch distribution tarball.
The source URL and version are pinned by `ROCM_TARBALL_URL` in the repository `env.sh`.

Current version: **ROCm 10.0.0 RC2** (`libamd_smi.so.27`).

## Refreshing

To update to a new ROCm release, bump `ROCM_TARBALL_URL` in `env.sh`, then run:

```sh
make rocm-tarball-fetch        # or: make docker-rocm-tarball-fetch
```

The refresh is version-guarded: it is a no-op unless the version parsed from `ROCM_TARBALL_URL`
differs from `lib/.version`. Note the upstream tarball is large (~8 GB); only maintainers bumping
the version need to run it. Commit the refreshed `lib/`, `include/amdsmi.h`, and `lib/.version`.
