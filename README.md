# rox + rix + rux

Three static Go binaries. rox and rix replace the Python `rox` script; rux runs agents on them.

- **rox** owns guests: it boots them (Apple container, Docker, Podman, Tart, Fly.io, Railway), wires them to Rex and Tailscale, manages their lifecycle, and installs agent CLIs when a project asks for them.
- **rix** owns the secrets and settings that agents and Git use inside a guest: Git SSH keys, Claude Code and Codex credentials, and synced skills, instructions, MCP servers and settings. rix installs nothing. See [docs/rix.md](docs/rix.md).

- **rux** is a multi-agent harness on rox and rix. `rux` opens it: you talk to a lead agent that starts sub-agents, each in its own rox guest, and every agent's conversation is live in one window. Agents run on Claude (Anthropic API) or any OpenAI-compatible model, and model keys stay with rix on your machine. `rux claude` makes Claude Code itself the lead, with sub-agents in Rex panes. `rux serve` runs threads you drive from your phone; a thread's guest is stopped when it's been idle and started again by the next message. rux stores no credentials: it gets every one from rix. See [docs/rux.md](docs/rux.md).

rox works without rix. With rix on `PATH`, rox calls it at fixed points in a guest's life, and rix reaches guests only through `rox exec`. The full rox reference is [docs/rox.md](docs/rox.md).

## Install

```sh
brew install rex-agents/tap/r-x        # or download an archive from the releases page
rox rex install                        # optional: every rox and rix command as a Rex action
rix migrate                            # coming from Python rox: move keys and credentials
```

Both binaries update themselves (`rox self-update`, `rix self-update`, or automatically, per `"updates": "auto" | "notify" | "off"` in each config).

## What changed from Python rox

| Before | Now |
| --- | --- |
| Every guest got Claude Code and Codex | A project lists the agents it wants in `.rox.json` or `.rox.local.json`: `{"agents": {"claude": true, "codex": "0.9.1"}}`. `true` means the latest release (checked at most every six hours); a version pins one. `rox up --agents claude,codex` asks for one launch. |
| `rox config agents` | `rix auth claude\|codex` stores the credential; `rix sync set AGENT ITEMS` chooses what's copied |
| `rox key add\|remove` | `rix key add\|remove\|ls` |
| `git_keys`, `git_known_hosts`, `git_agent` in rox's config | the same keys in `~/.config/rix/config.json` |
| `rox update-agents`, `rox agent` | removed |
| `guest/relay.py` | `rox __relay`, built in |
| Python 3.9+, a tarball, a self-updating script | one binary per OS and architecture |

`rox key` and `rox config agents` still work for one release and say what to run instead. Existing guest records and configs are read as they are.

## Output

rox and rix never show the raw output of the tools they drive. Each step shows a spinner and then one line saying what happened:

```
◆ rox up  web · main → container
  ✓ Read main (found .rox.sh and .rox.json at 3f2a9c1)
  ✓ rox:linux is up to date
  ✓ Project image rox:web is current (built from 3f2a9c1)
  ✓ Started rv-web-3f2a9c1b7d4e · container container          4.2s
  ✓ Installed Claude Code 2.1.3
  ✓ Signed in Claude Code rix
  ✓ Rex is running in the guest
  ✓ Registered Rex host rv-web-3f2a9c1b7d4e
  ✓ Git agent holds your configured key id_ed25519 rix
  ✓ Cloned web (main) → /home/workspace/web                    3.1s
  ✓ Set up web

● rv-web-3f2a9c1b7d4e  ready
  Workspace  rv-web-3f2a9c1b7d4e
  Agents     Claude Code 2.1.3
  Project    web
  Checkout   /home/workspace/web
  Session    session:01…
```

A failed step shows the last lines of what it captured. `--verbose` (or `ROX_VERBOSE=1`) streams everything, dimmed. Without a terminal, or with `NO_COLOR`, output is plain; `rox ls` prints tab-separated rows when piped.

## rox ↔ rix contract

The full rix reference is in [docs/rix.md](docs/rix.md).

rox runs `rix hook EVENT NAME` with the guest record (`rox inspect NAME --json`, schema 1) on stdin, and reads one JSON reply on stdout:

| Event | When | rix does |
| --- | --- | --- |
| `pre-fetch` | the launcher reads the branch | lends an SSH agent until rox closes stdin |
| `post-boot` | guest reachable, agents installed | pushes credentials for those agents |
| `pre-clone` | before cloning | starts the guest's Git agent and returns its socket, which rox forwards |
| `post-provision` | projects set up | syncs agent setup, pushes credentials for newly installed agents |
| `resume` | after `start`, `reconnect` or a service restore | restores the Git agent |
| `down` | the guest is removed | stops the Git agent and forgets the guest |

A failed `pre-clone` fails the launch; other failures are warnings. The reply is `{"schema": 1, "agent_socket", "forward", "known_hosts", "done": [...], "warnings": [...]}`; rox shows `done` and `warnings` as its own steps. Secrets reach a guest only on the stdin of `rox exec NAME --stdin -- …`, never in argv, an image, or a host process's environment.

`rox hooks` shows the active provider. Set `"hooks": "off"` (or another program) in rox's config, or `ROX_HOOKS`, to change it.

### Remote hosts

`rox up --host LABEL` runs rox on that host, whose own rix vault signs the guest in (the default, `--vault host`). With `--vault here`, the host's vault gives the clone its Git keys, and this machine's rix pushes credentials and agent setup over the guest's tailnet Rex server afterwards.

## rux and credentials

Everything rux needs comes from rix on the machine running it:

```sh
rix secret set ANTHROPIC_API_KEY  # the harness's Claude models (or OPENAI_API_KEY, OPENROUTER_API_KEY, …)
rix auth claude                   # Claude Code in guests (rox up --agents claude, signed in by rix's hooks)
rix auth github                   # GitHub token for the repo picker and git in guests; without one, gh's login
rix secret set BUTLER_API_KEY     # secrets that rux actions list in tool.json
rix secret import ~/.rux/secrets.env   # move secrets from an older rux
```

`rix credential github` and `rix secret get NAME... --json` are how rux asks; any tool on this machine can do the same.

## Rex actions

`rox rex install` writes `~/.config/rex/rox.lua` and adds `require("rox")` to Rex's `init.lua`. Every rox and rix command becomes an action named `PROGRAM_COMMAND`:

```sh
rex do rox_ls                                  # JSON
rex do rox_up backend=container no_prebuild=true
rex do rox_shell name=rv-web-3f2a9c1b7d4e
rex do rix_status
```

In the palette, under **Rox** and **Rix**, a missing guest opens a picker. Choices such as the backend or agent open a picker too. Slow or interactive commands open a terminal split beside the pane, so you see rox's own output and type any secret there. Quick ones return their output. `Rox: Add a Guest to the Rex App…` copies the guest's address and opens Add Remote Host in the app.

Updates rewrite the actions when they're installed. `rox rex uninstall` removes them.

## Development

```sh
make install                                    # rox, rix and rux into ~/.local/bin, plus the Linux rux guests run
go test ./...                                   # unit and golden tests, against a fake command runner
go test ./... -update                           # rewrite golden files
go test -tags integration ./integration/...     # boots a real Docker guest
goreleaser release --snapshot --clean           # local release build
```

Layout: `cmd/rox`, `cmd/rix`, `cmd/rux`, `internal/rox` (backends, lifecycle, CLI), `internal/rix` (vault, hooks, sync), `internal/rux` (server, runner, agent mode, web app), `internal/contract` (the JSON between them), `internal/ui` (all terminal output), `internal/sys` (the command runner and file helpers), `internal/rexbridge` (the Rex actions), `internal/assets` (embedded Containerfile, guest scripts, skill).
