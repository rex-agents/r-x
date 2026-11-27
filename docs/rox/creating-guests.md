---
sidebar:
  order: 3
---

# Creating guests

`rox up` starts and provisions a new guest; `rox add` clones another project into a guest that is
already running. Both take the same project flags.

## rox up

```
rox up [container|linux|macos|fly|railway] [flags] [-- COMMAND...]
```

Starts and provisions a new guest. Inside a Git checkout it also clones the project; use
`--no-project` for a bare guest. Everything after `--` runs as the command of the project's Rex
session (kept open when it exits), which needs the [rox-rex hook](rex-hook.md); without it rox says so and
the command doesn't run.

| Flag | Meaning |
| --- | --- |
| `--repo URL`, `--branch NAME`, `--remote NAME` | what to clone instead of the checkout's current branch and remote. `--repo` and `--remote` are mutually exclusive. HTTPS and SSH (`ssh://` or `user@host:path`) remotes only. |
| `--project NAME` | name the project when launching from `--repo` without a checkout (needs `--repo`). Without `--branch`, rox uses the remote's default branch. |
| `--cwd DIR` | the directory in the project to start from (default: current directory) |
| `--local` | also copy this checkout's unpushed commits and uncommitted files (not gitignored ones) into the guest, and use the checkout's own `.rox.json`. Needs a commit; can't be combined with `--host`, `--project` or `--no-project`. `.rox.sh` runs on the copied checkout, and no project image is used. |
| `--no-project` | start a guest with no project, even inside a checkout |
| `--image IMAGE` | container image, Fly image, or Tart template to launch |
| `--engine ENGINE` | `container`, `docker` or `podman` (container backend only) |
| `--cpus N`, `--memory SIZE` | CPUs and memory (`8G`, `512M`) for a container or VM; `--cpus` must be positive |
| `--agents LIST` | agents to install for this launch: `claude,codex` or `claude=1.2.3` (see [Agents](agents.md)) |
| `--no-agents` | install no agents, whatever the project lists |
| `--no-hook` | skip the project's `.rox.sh` (also disables the project image) |
| `--no-prebuild` | clone and run `.rox.sh` at boot instead of using a project image |
| `--rebuild` | rebuild the project image before launching (an error when no image would be used) |
| `--no-tailscale` | keep this guest off the tailnet even when tailscale is configured |
| `--no-git-agent` | give the guest Git keys only while cloning, not for its whole life |
| `--git-key PATH` (repeatable), `--git-known-hosts FILE` | Git SSH identities and `known_hosts` for rix to use for this launch, replacing rix's configured ones |
| `--boot-timeout SECONDS` | per readiness check (default 120) |
| `--config FILE` | use this rox config instead of `~/.config/rox/config.json` (must exist) |
| `--open-shell` | open a shell for the guest through the `open-shell` hook (rox-rex: the Rex app, or a Guest tab in this Rex terminal), or `rox shell` here when no provider does |
| `-S, --host LABEL`, `--vault host\|here` | launch on another host; see [Remote hosts](remote-hosts.md) |

```sh
rox up                                  # backend from .rox.json
rox up container --cpus 4 --memory 8G
rox up fly --agents claude
rox up container --no-project -- htop
rox up container --repo https://github.com/owner/web.git --branch dev
rox up container --local                # include my uncommitted work
rox up linux -S studio                  # on the host "studio", over SSH
```

### What a launch does

In order:

1. Read the branch for `.rox.sh` and `.rox.json` through a cached mirror (rix lends an SSH agent for
   this: `pre-fetch`).
2. Resolve backend, settings and agents.
3. Make sure the backend's runtime is up; prepare images.
4. Start the guest and wait for SSH.
5. Install agents, then the `post-boot` hook (rix signs them in; rox-rex starts Rex in the guest and joins the tailnet if configured).
6. Clone (the `pre-clone` hook gives the guest its Git agent).
7. Run `.rox.sh`.
8. Run the `post-provision` hook (rix syncs agent setup; rox-rex starts the project's Rex session and registers the Rex host).
9. With `--open-shell`, the `open-shell` hook.

Each step prints one line. The hooks are described in [The hook contract](rix-contract.md); Rex is [a hook](rex-hook.md).

If a launch fails after the guest exists, rox keeps it (phase `failed`); remove it with
`rox down NAME`. A failed Git clone or `.rox.sh` leaves the guest available so you can look around with `rox shell`.

## rox add

```
rox add NAME [flags] [-- COMMAND...]
```

Clones another project into a running guest of this machine (or, found automatically, on another
Rex host). Takes the project flags of `rox up` (`--repo`, `--branch`, `--remote`, `--project`, `--cwd`,
`--local`, `--no-hook`, `--agents`, `--no-agents`, `--git-key`, `--git-known-hosts`, `--config`,
`--open-shell`, `--vault`, `-S/--host`) and `--no-git-agent`. NAME must be a guest name. Unlike
`up`, it requires a checkout or `--repo`. The guest must be running; a stopped one asks you to
[`rox start`](managing-guests.md#rox-start-stop-reconnect) it first, and one that didn't finish setup asks you to `rox reconnect` it. Agents the added
project lists are installed if the guest lacks them.
