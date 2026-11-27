---
sidebar:
  order: 1
---

# Install

How to install rox, what it needs on `PATH`, and how it updates itself.

```sh
brew install rex-agents/tap/r-x        # or download an archive from the releases page
rox rex install                        # optional: every rox and rix command as a Rex action
rox skill install                      # optional: the rox skill for Claude Code and Codex
```

`rox --version` prints the version. A released binary updates itself: `rox self-update` installs the
latest release (and refreshes the skill and the Rex actions), `rox self-update --check` only says
whether one is out. Automatic checks happen at most every six hours and follow `"updates"` in the
[config](configuration.md) (`"auto"` the default, `"notify"`, `"off"`) or `ROX_UPDATES`.

## Required programs

rox needs these programs on `PATH`, depending on what you launch:

| When | Programs |
| --- | --- |
| Always | `rex`, `ssh`, `ssh-keygen` (plus `git` when a project is cloned from this machine) |
| `container` backend | the engine: `docker`, `podman`, or Apple's `container` |
| `linux`, `macos` backends | `tart` (Apple Silicon Mac only) |
| `fly` backend | `fly` or `flyctl`, logged in (`fly auth login`) |
| `railway` backend | `railway`, logged in (`railway login`) |
| Funnels | `tailscale` on this machine |
| `rox template` | `tart`, `ssh`, `ssh-keygen`, and `ssh-keyscan` (linux); `ditto` and the Rex app in `/Applications/Rex.app` (macos) |

rox checks the ones a launch needs before it starts and says which is missing.

## rox skill, self-update

```
rox skill install [--agent claude|codex]... [--dir DIR] [--force]
rox self-update [--check]
```

`skill install` copies the bundled rox skill into `~/.claude/skills` and/or the Codex skills folder
(each agent set up on this machine, or the ones named by `--agent`), or into `--dir` (such as a project's
`.claude/skills`). `--agent` and `--dir` can't be combined. `--force` replaces a folder named `rox`
that isn't a rox skill. `self-update` also refreshes the skill.
