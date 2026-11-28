# Using rux

rux runs agents in disposable [rox](../rox/index.md) guests. It has four modes, and stores no credentials: it gets every one from [rix](../rix/index.md) (see [credentials](credentials.md)). `rux help` lists every command.

| Mode | What it is | Pages |
| --- | --- | --- |
| **`rux`** | The authority TUI: the pack's hosts, projects, threads and sub-agents as a node graph, and the same view in the web app. | [authority TUI](authority-tui.md), [authorities and the pack](authority.md) |
| **`rux agent [PROMPT]`** | A multi-agent harness in one terminal window: a lead agent, sub-agents in guests, every conversation live. Runs in the current context, on the host or inside a VM. | [lead and sub-agents](lead-and-sub-agents.md), [models](models.md), [remote hosts](remote-hosts.md) |
| **`rux claude`** and **`rux agent spawn`** | Claude Code (or Codex) as the lead in a Rex pane, with sub-agents in Rex panes. | [rux claude](rux-claude.md), [rux agent](rux-agent.md) |
| **`rux serve`** | Claude Code threads in guests, driven from a phone over Tailscale. | [serve and threads](serve.md), [projects and managers](projects-and-managers.md), [actions](actions.md) |

## How the pieces fit

- In every mode, each sub-agent works in its own rox guest, clones the repo over SSH with your key from rix's Git agent, and pushes its own branch `rux/NAME`. Only you merge.
- `rux agent` (the harness) runs every model loop on your machine, so model keys never enter a guest; only tool calls do.
- `rux claude` and `rux agent spawn` leave the lead to Claude Code or Codex, and use Rex panes instead of the harness window.
- `rux serve` is a web app with a runner in each guest:

```
phone (PWA) ──HTTPS (tailscale serve)──▶ rux serve        (Mac: web app, thread store, runs `rox up`)
                                          ▲ WebSocket, dialed by the guest
                                     rux runner           (in each guest: owns `claude -p` stream-json)
```

## Pages

| Page | What it covers |
| --- | --- |
| [Authority TUI](authority-tui.md) | Bare `rux`: keys, views, creating threads, and the web app's matching pack view. |
| [Lead and sub-agents](lead-and-sub-agents.md) | The `rux agent` window, keys and commands, the lead's tools, a sub-agent's life. |
| [Models](models.md) | `provider/model` names, Claude Code, Codex, built-in providers, `harness.json`. |
| [Remote hosts](remote-hosts.md) | Starting guests on Rex remote hosts. |
| [rux claude](rux-claude.md) | Claude Code as the lead, and how sub-agent guests are set up. |
| [rux agent](rux-agent.md) | Orchestrating from your own Claude Code or Codex session; `rux skill`. |
| [Credentials](credentials.md) | What rux gets from rix, and GitHub access in guests. |
| [Screenshots and files](screenshots-and-files.md) | `rux screenshot` and `rux share` in threads. |
| [Rich UI, diffs and dashboards](rich-ui.md) | `rux ui` and `rux diff` in threads. |
| [Serve and threads](serve.md) | `rux serve`: how threads work, running it, host setup, flags. |
| [Projects and managers](projects-and-managers.md) | Projects, defaults, and the manager thread. |
| [Actions](actions.md) | Jobs on the host that agents in guests can trigger. |
| [Environment and files](environment-and-files.md) | Environment variables and files under `~/.rux`. |
| [Troubleshooting](troubleshooting.md) | Common problems and fixes. |
