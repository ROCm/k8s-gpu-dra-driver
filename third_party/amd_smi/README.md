# AMD SMI (vendored)

The DRA driver links against AMD SMI (`libamd_smi`) via cgo (see `pkg/amdsmi`). This directory
vendors the prebuilt library, its runtime dependencies, and the public header.

## Contents

| Path | Description |
|---|---|
| `include/amdsmi.h` | Public AMD SMI C header (MIT licensed). Used by the cgo `#include`. |
| `lib/libamd_smi.so*` | Prebuilt AMD SMI shared library (+ soname symlinks). |
| `lib/librocm_sysdeps_{nl_3,nl_genl_3,mnl}.so*` | The rocm_sysdeps netlink libraries that `libamd_smi.so` `DT_NEEDED`s (verified with `readelf -d`). Only these are vendored; the upstream tarball ships ~40 sysdeps libs but AMD SMI links just this netlink trio. |

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

There is no version file in the tree: `ROCM_TARBALL_URL` already names the version, and a second
copy would only invite the two to disagree. The refresh is a no-op whenever a library is already
vendored, so it never downloads by accident.

That check is coarse — it does not notice a version change within the same soname — so bumping
`ROCM_TARBALL_URL` (an rc to GA move, say) needs an explicit re-pull:

```sh
ROCM_TARBALL_FORCE=1 make rocm-tarball-fetch
```

The upstream tarball is large (~8 GB), though it is streamed and never stored whole; only
maintainers changing the version need to run this. The refresh prints the amdsmi build it
extracted (e.g. `27.0.0+6b0e43f3`) so the exact artifact is recorded in the commit that ships it.
Commit the refreshed `lib/` and `include/amdsmi.h`.
