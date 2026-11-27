---
sidebar:
  order: 1
---

# Install

What rox needs on `PATH`, the rox skill, and `rox self-update`. To install rox, rix and rux (Homebrew or release archives), or to migrate from Python rox, see the [installation guide](../getting-started/installation.md).

## Required programs

rox needs these programs on `PATH`, depending on what you launch, and checks the ones a launch needs before it starts:

| When | Programs |
| --- | --- |
| Always | `ssh`, `ssh-keygen` (plus `git` when a project is cloned from this machine) |
| `container` backend | the engine: `docker`, `podman`, or Apple's `container` |
| `linux`, `macos` backends | `tart` (Apple Silicon Mac only) |
| `fly` backend | `fly` or `flyctl`, logged in (`fly auth login`) |
| `railway` backend | `railway`, logged in (`railway login`) |
| Funnels | `tailscale` on this machine |
| `rox template` | `tart`, `ssh`, `ssh-keygen`, and `ssh-keyscan` (linux); `ditto` and the Rex app in `/Applications/Rex.app` (macos, only with `--with-rex`) |
| Rex features | the optional [rox-rex](rex-hook.md) hook and `rex`; rox itself never needs them |

## The rox skill

```sh
rox skill install [--agent claude|codex]... [--dir DIR] [--force]
```

Copies the bundled rox skill into `~/.claude/skills` and/or the Codex skills folder (each agent set up on this machine, or the ones named by `--agent`), or into `--dir` (such as a project's `.claude/skills`). `--agent` and `--dir` can't be combined. `--force` replaces a folder named `rox` that isn't a rox skill.

## Updates

```sh
rox --version
rox self-update [--check]
```

`rox self-update` installs the latest release and refreshes the skill (and, with rox-rex installed, the Rex actions); `--check` only says whether one is out. Automatic checks happen at most every six hours and follow `"updates"` in the [config](configuration.md) (`"auto"` the default, `"notify"`, `"off"`) or `ROX_UPDATES`.
