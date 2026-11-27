---
sidebar:
  order: 16
---

# Files, environment variables and output

Where rox keeps its files, which environment variables it reads, and how it prints.

| What | Where |
| --- | --- |
| Config | `~/.config/rox/config.json` (`XDG_CONFIG_HOME`) |
| State | `~/.local/state/rox/` (`XDG_STATE_HOME`): `NAME/guest.json` (record), `NAME/known_hosts`, `NAME/tart.log` and other logs, `images/`, `agent-versions.json`, `remote/login` (key for guests on other hosts), `templates/` |
| Cache | `~/.cache/rox/repos/` (`XDG_CACHE_HOME`): branch mirrors |
| Sockets | `/tmp/rox-UID/ID/` per guest (SSH control, relay and Rex sockets) |
| In the guest | `$HOME/.rox/NAME/` (Rex socket, state), checkouts in `$HOME/PROJECT` (`/home/workspace/PROJECT`) |
| Service | `~/Library/LaunchAgents/io.rox.service.plist`, or `~/.config/systemd/user/rox.service` |
| Rex actions | `~/.config/rex/rox.lua` |

Guest records are the same JSON the Python rox wrote and are read as they are.

## Environment variables

| Variable | Effect |
| --- | --- |
| `ROX_VERBOSE=1` | same as `--verbose` |
| `ROX_UPDATES` | `auto`, `notify` or `off`; overrides the config's `updates` |
| `ROX_HOOKS` | hook provider program, or `off`/`none`; overrides the config |
| `ROX_THEME` | `light` or `dark` terminal theme (otherwise guessed from `COLORFGBG`) |
| `NO_COLOR`, `TERM=dumb` | plain output |
| `XDG_CONFIG_HOME`, `XDG_STATE_HOME`, `XDG_CACHE_HOME` | move the folders above |
| `REX_CONFIG` | Rex's `init.lua`, for `rox rex install` |
| `REX_SESSION` | set by Rex terminals; required by `--open-shell` |

## Output

Each step is a spinner, then one line. A failed step shows the last lines of what it
captured. Without a terminal (or with `NO_COLOR`) output is plain, and `rox ls` prints tab-separated
rows.
