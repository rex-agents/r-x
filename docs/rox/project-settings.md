---
sidebar:
  order: 8
---

# Project settings

A project configures its guests with files in the repository: `.rox.json`, `.rox.local.json` and
`.rox.sh`. Settings about *your machine* live in the [rox config](configuration.md) instead.

A project configures its guests with up to three files. All JSON files are checked strictly: an
unknown or misplaced key is an error that names the file.

- **`.rox.json`** on the branch rox clones. It is read from a cached mirror, so it needs no checkout
  (or, with `--local`, from your checkout).
- **`.rox.local.json`** in the root of your own checkout, never read from the remote. It overrides
  `.rox.json`. rox prints a line when it uses it.
- **`.rox.sh`** on the branch: a script run in the project's checkout in the guest.

## Keys

Keys in `.rox.json` and `.rox.local.json`:

```json
{
  "backend": "container",
  "agents": {"claude": true, "codex": "0.9.1"},
  "container": {"cpus": 4, "memory": "8G", "image": "rox:linux", "prebuild": true}
}
```

| Key | Meaning |
| --- | --- |
| `backend` | `container`, `linux`, `macos`, `fly` or `railway`; used when `rox up` gets no backend |
| `agents` | agents to install: `true` for the latest release, or a version such as `"1.2.3"`. See [Agents](agents.md) |
| `container`, `linux`, `macos` | `cpus`, `memory`, `image`, `prebuild` for that backend |
| `fly`, `railway` | no settings; a branch can't choose what your cloud account runs or spends |

Settings that describe the launching machine (`engine`, `user`, `identity`, `host_key`, `git_keys`,
`git_known_hosts`, `tailscale`, `git_agent`) and the cloud sections' keys can only be set in rox's
own config and are rejected in project files.

## Precedence

Lowest to highest: the launcher's rox config section, `.rox.json`, `.rox.local.json`,
command-line flags. For `backend`: flag, `.rox.local.json`, `.rox.json`.

## .rox.sh

`.rox.sh` runs with the checkout as its working directory and these variables:

| Variable | Value |
| --- | --- |
| `ROX_PROJECT_DIR` | the checkout |
| `ROX_NAME` | the guest |
| `ROX_BACKEND` | the backend |
| `ROX_PHASE` | `build` when it is baked into a [project image](images-and-caching.md) (no Git keys, no Rex server, no `SSH_AUTH_SOCK`); `boot` when it runs in a new guest (with `REX_SERVER` set) |

It never gets your Git keys. A missing or unreadable branch (for example, no network to the remote)
is a warning: rox then runs `.rox.sh` at boot and ignores `.rox.json`. A broken `.rox.json` stops the
launch. `--no-hook` skips `.rox.sh`.
