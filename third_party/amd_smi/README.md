# AMD SMI (vendored)

The DRA driver links against AMD SMI (`libamd_smi`) via cgo (see `pkg/amdsmi`). This directory
vendors the prebuilt library, its runtime dependencies, and the public header.

## Contents

| Path | Description |
|---|---|
| `include/amdsmi.h` | Public AMD SMI C header (MIT licensed). Used by the cgo `#include`. |
| `lib/libamd_smi.so*` | Prebuilt AMD SMI shared library (+ soname symlinks). |
| `lib/librocm_sysdeps_{nl_3,nl_genl_3,mnl}.so*` | The rocm_sysdeps netlink libraries that `libamd_smi.so` `DT_NEEDED`s (verified with `readelf -d`). Only these are vendored; the upstream tarball ships ~40 sysdeps libs but AMD SMI links just this netlink trio. |
| `lib/.version` | The vendored tarball version (first line), used by `scripts/update-amdsmi.sh` to decide whether a refresh is needed, plus the amdsmi build it was cut from as provenance. Written by the script; not hand-edited. |

## Provenance

These files are extracted from the ROCm **theRock** (rockrel) multi-arch distribution tarball.
The source URL and version are pinned by `ROCM_TARBALL_URL` in the repository `env.sh`.

Current version: **ROCm 10.0.0 GA** (`libamd_smi.so.27`, amdsmi `27.0.0+6b0e43f3`).

Note that ROCm 10.0 removed `amdsmi_gpu_driver_reload()`. The driver reload that
must follow a memory-partition change is done with `modprobe` instead; see
`kmm.ReloadInboxDriver`.

## Refreshing

To update to a new ROCm release, bump `ROCM_TARBALL_URL` in `env.sh`, then run:

```sh
make rocm-tarball-fetch        # or: make docker-rocm-tarball-fetch
```

The refresh is version-guarded: it is a no-op unless the version in the tarball filename differs
from the first line of `lib/.version`. The filename is what distinguishes builds the library
itself cannot — `10.0.0rc2` and `10.0.0` GA both ship soname `.27` — so an rc to GA bump is
picked up simply by changing the URL.

To re-pull the *same* version (upstream respun the tarball under an unchanged URL, say):

```sh
ROCM_TARBALL_FORCE=1 make rocm-tarball-fetch
```

The upstream tarball is large (~8 GB), though it is streamed and never stored whole; only
maintainers changing the version need to run this. Commit the refreshed `lib/`,
`include/amdsmi.h`, and `lib/.version`.
