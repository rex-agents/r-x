---
sidebar:
  order: 7
---

# Templates

The `linux` and `macos` backends launch Tart VMs cloned from a prepared template. `rox template`
creates one. It needs an Apple Silicon Mac with Tart (see [Install](install.md#required-programs)).

## rox template

```
rox template macos|linux [--name N] [--base IMAGE] [--distro D] [--default] [--rex-channel C]
                         [--base-password P] [--boot-timeout S] [--config FILE] [-S HOST]
```

Prepares a Tart template for the `macos` or `linux` backend (Apple Silicon only) and points the
[config](configuration.md) at it. It clones a base image (`ghcr.io/cirruslabs/macos-tahoe-base:latest`, or for Linux an
Ubuntu 24.04, Debian trixie or Fedora 42 image), boots it, creates a VM login key (`~/.ssh/rox_vm`),
replaces the base image's password (default `admin`) with a random one saved beside the config, installs Rex (and Git for
Linux), shuts it down, and writes `user`, `identity`, `host_key` and `image` into `macos` or `linux`
in the config. Run it again after upgrading Rex.

| Flag | Meaning |
| --- | --- |
| `--name` | template name (default `rox-macos`, or `rox-DISTRO`) |
| `--distro ubuntu\|debian\|fedora\|arch` | linux only; default ubuntu. `arch` has no published image, so pass `--base` |
| `--default` | make this the `linux.image` even if another is set (linux only) |
| `--rex-channel stable\|preview\|snapshot` | Rex channel (linux only) |
| `--base` | the Tart image to clone from |
| `--base-password` | the base image's login password |
| `--boot-timeout` | seconds per readiness check (default 600) |
| `--config FILE` | the rox config to update |
| `-S, --host` | run it on another Rex host |

`--distro`, `--default` and `--rex-channel` are refused for macos. A first run downloads a large image.
Then launch with [`rox up linux`](creating-guests.md#rox-up) or `rox up macos`.
