---
sidebar:
  order: 2
---

# Guests and backends

A guest is a disposable machine (container, VM, cloud machine or sandbox) that rox boots, reaches over
SSH and runs a Rex server in. The backend decides what kind of machine it is.

`rox up [container|linux|macos|fly|railway]` chooses a backend. Without the argument, rox uses
`"backend"` from `.rox.local.json`, then from `.rox.json` on the branch; with neither it stops and
asks you to choose one. See [Project settings](project-settings.md).

| Backend | What runs | User | Needs |
| --- | --- | --- | --- |
| `container` | a container from the `rox:linux` image (Debian, sshd, Rex, git, Docker) | `workspace` | an engine: Apple `container` (default on Apple Silicon Macs), `docker` (default elsewhere), or `podman` |
| `linux` | a Linux VM cloned from a Tart template | `admin` by default | an Apple Silicon Mac, Tart, and a template from `rox template linux` |
| `macos` | a macOS VM cloned from a Tart template | `admin` by default | the same, with `rox template macos` |
| `fly` | a Fly Machine with a volume for the home directory | `workspace` | the Fly CLI, logged in |
| `railway` | a Railway sandbox | `workspace` | the Railway CLI, logged in, and `railway.project` in the config |

## Details that differ

- **container.** `--engine container|docker|podman` (or `container.engine` in the config) picks the
  engine; the default is `container` on Apple Silicon macOS and `docker` everywhere else. The image
  is `rox:linux`, which rox builds with your engine the first time and again when its Containerfile
  changes. Docker and Podman containers get no sudo and are reached through a published loopback
  port (`127.0.0.1:PORT`), and restart with the runtime (`--restart unless-stopped`). Apple's
  containers each run in their own VM with their own IP, give `workspace` sudo, and run Docker inside
  the guest. `--image` uses another image; rox skips the pull for `rox:` images.
- **linux / macos.** Guests are Tart clones of a prepared template. [`rox template linux|macos`](templates.md)
  prepares the template and writes `linux.image`/`macos.image`, `user`, `identity` and `host_key`
  into the config; `rox up linux` fails and names `rox template linux` when they aren't set.
  `--image NAME` launches another template. Tart runs with `--no-graphics --no-clipboard`; its log
  is `tart.log` in the guest's state folder.
- **fly.** rox creates (once) a Fly app named `rox-` plus a hash of your account (or uses `fly.app`),
  builds rox's guest image on Fly's remote builder when the Containerfile changed, and runs a
  machine (default 2 CPUs, 4G memory, region `iad`, org `personal`, 10 GB volume mounted at
  `/home/workspace`). rox reaches it through `fly proxy`. `--cpus`, `--memory` and `--image` apply.
- **railway.** Sandboxes are sized by your Railway plan, so `--cpus`, `--memory`, `--image` and the
  corresponding config keys are refused. rox builds a sandbox template on first use. A Railway
  sandbox can't be stopped and started: `rox stop`/`rox start` tell you to use `rox down`.

## Guest phases

A guest has a *phase* (`starting`, `ready`, `failed`, `removing`; shown by [`rox ls`](managing-guests.md#rox-ls)) and is `stopped`
after [`rox stop`](managing-guests.md#rox-start-stop-reconnect).
