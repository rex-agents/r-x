---
sidebar:
  order: 17
---

# Troubleshooting

Common rox errors and what to do about them. Run any command with `-v` to see what rox runs.

### Backend and engine

- **`docker isn't running. Start it and try again`** — start the engine (for Podman on macOS,
  `podman machine start`). For Apple's `container`, rox starts its services itself; if that fails,
  run `container system start`.
- **`Install Apple's container tool … or choose --engine docker`** — `--engine container` is the default
  on Apple Silicon and needs the `container` CLI.
- **`Choose a backend with rox up container, linux, macos, fly or railway, or set "backend" in .rox.json`** —
  pass a backend or set `backend`.
- **`Configure user, identity and host_key for linux in …, or run rox template linux`** — prepare a
  [template](templates.md) first (Apple Silicon Mac with Tart).
- **`Railway sandboxes can't be stopped and started again`** — use `rox down`.

### Launching

- **`… is not inside a Git checkout. Run from a checkout, or pass --repo`** — or use `--no-project`.
- **`Could not read BRANCH here, so .rox.sh runs at boot and .rox.json is ignored`** — rox couldn't
  fetch the branch (network, or no key). The launch continues without images; check your SSH access
  or install rix.
- **`Couldn't find … default branch … Pass --branch`** — name the branch.
- **`--rebuild needs a project image`** — the launch has no image (see [Images and caching](images-and-caching.md)).
- **`Building rox:NAME failed while running .rox.sh. Launch with --no-prebuild`** — the setup script
  fails in the build; `--no-prebuild` runs it in the guest where you can look.
- **A guest is `failed`** — `rox down NAME` and launch again. The state folder keeps logs until then.

### Running guests

- **`NAME is stopped. Start it with rox start NAME`** — `rox stop` was used; `shell`, `cp`, `forward`
  need a running guest.
- **A guest's tunnel is gone after sleep or restart** — `rox reconnect NAME`, or `rox service install`
  (see [Keeping guests running](service.md)).
- **`The container for NAME no longer exists`** — it was deleted outside rox; `rox down NAME` cleans up.
- **`rox cp copies into guests on this machine`** — run it on the guest's host.

### Tailscale

- **`Tailscale still wants a browser sign-in, so the auth key was rejected`** — create a reusable key
  allowed to use the configured tags (`rox config tailscale`).
- **`… holds a Tailscale auth key, so only you may read it`** — `chmod 600` the key file.

### Hooks and anything else

- **A hook warning** — `rox hooks` shows the provider; run with `--verbose` for details. Without
  rix, guests clone without your Git keys and agents aren't signed in. See
  [The rix contract](rix-contract.md).
- **Anything else** — `rox -v COMMAND` streams the output of every tool rox runs.
