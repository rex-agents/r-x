---
sidebar:
  order: 1
---

# Installation

r-x is three static Go binaries: **rox** (guests), **rix** (credentials and agent setup) and **rux** (the multi-agent harness). One release installs all three.

## Homebrew

```sh
brew install rex-agents/tap/r-x
rox --version
```

## Release archives

Without Homebrew, download the archive for your OS and architecture from the [releases page](https://github.com/rex-agents/r-x/releases), unpack it, and put `rox`, `rix` and `rux` in a folder on your `PATH`. Each is a single binary with no runtime to install. A release build of `rux` also downloads the matching Linux `rux` that guests run, the first time a guest asks, and checks it against the release's `checksums.txt`.

Building from source needs Go: `make install` puts the three binaries in `~/.local/bin`, plus the Linux `rux` that guests run.

## Optional steps

```sh
rox rex install     # every rox and rix command as a Rex action
rox skill install   # the rox skill for Claude Code and Codex
```

`rox rex install` writes `~/.config/rex/rox.lua` and adds `require("rox")` to Rex's `init.lua`; `rox rex uninstall` removes it. `rox skill install` accepts `--agent claude|codex` (repeatable) to choose where the skill goes.

## Coming from Python rox

If you used the Python `rox` script, move its Git keys, credentials and secret files into rix:

```sh
rix migrate --dry-run   # say what would move
rix migrate             # move it (--rox-config PATH if your config isn't ~/.config/rox/config.json)
```

Existing guest records and configs are read as they are. `rix migrate` prints a `.rox.local.json` snippet that keeps the agents rox used to install in every guest, since a project now lists the agents it wants. See [what changed from Python rox](https://github.com/rex-agents/r-x#what-changed-from-python-rox).

## Updates

Released binaries update themselves. Run `rox self-update` or `rix self-update` to install the latest release now, or add `--check` to only say whether one is out. Otherwise each checks at most every six hours, as `"updates": "auto" | "notify" | "off"` in its config says (or `ROX_UPDATES` / `RIX_UPDATES`). `rox self-update` also refreshes the rox skill and the Rex actions. Development builds don't update.

## Prerequisites

rox needs these programs on `PATH`, depending on what you launch, and checks the ones a launch needs before it starts:

| You use | You need |
| --- | --- |
| Always | `rex`, `ssh`, `ssh-keygen` (plus `git` when a project is cloned from this machine) |
| `container` backend | an engine: Apple's `container` (the default on Apple Silicon Macs), `docker` (the default elsewhere), or `podman` |
| `linux`, `macos` backends | `tart`, on an Apple Silicon Mac, and a template from `rox template linux` or `rox template macos` |
| `fly` backend | `fly` or `flyctl`, logged in (`fly auth login`) |
| `railway` backend | `railway`, logged in (`railway login`), and `railway.project` in rox's config |
| Funnels | `tailscale` on this machine |

rux needs rox and rix on the same machine, plus a model: an API key stored with `rix secret set ANTHROPIC_API_KEY` (or `OPENAI_API_KEY`, `OPENROUTER_API_KEY`, …), or Claude Code or Codex installed on this machine. `rux serve`, which you drive from a phone, also needs Tailscale.

Next: the [quick start](quick-start.md).
