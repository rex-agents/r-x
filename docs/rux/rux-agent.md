---
sidebar:
  order: 6
---

# rux agent

`rux agent spawn`, `ls`, `read`, `send`, `wait`, `focus` and `stop` let your own Claude Code or Codex session orchestrate sub-agents in Rex panes. They have no daemon: state is one JSON file per agent. A first argument that is not one of these is a prompt: `rux agent "fix the bug"` opens the [harness](lead-and-sub-agents.md) in the current context (on the host, or inside a VM), and `rux agent` alone opens it empty. Bare `rux` is the [authority TUI](authority-tui.md).

## Orchestrating from Claude Code or Codex (`rux agent`)

In a Rex terminal, your own Claude Code or Codex session can be the orchestrator, and rux gives it sub-agents in Rex
panes ([`rux claude`](rux-claude.md) starts Claude Code that way and keeps the skill current). For any other session, install the skill
once, then ask your agent to fan work out.

```sh
rux skill install          # into ~/.claude/skills/rux and ~/.codex/skills/rux
rux skill show             # print the skill
```

`install` takes `--agent claude|codex` (repeatable; default: each one with a `~/.claude` or `~/.codex`), `--dir SKILLS_DIR`
and `--force` (replace a `rux` folder that isn't this skill). The skill teaches the agent these commands. There's no
daemon: state is one JSON file per agent in `~/.rux/agents/<rex session>/`. Every command takes `--session S`
(default `$REX_SESSION`).

**Without Rex** (`REX_SESSION` unset, no `--session`), `rux agent spawn` runs the sub-agent headless: no pane, the agent
CLI runs non-interactively in the guest, and its output goes to a log next to its state file. `rux agent read ID`
prints the log, `wait` and `stop` work as usual, and `send` and `focus` need a pane. Nothing here calls `rex` then.

### Commands

| Command | Does |
| --- | --- |
| `rux agent spawn --name NAME [flags] PROMPT` | start a sub-agent in a new pane (give the prompt as words, or with `--prompt-file FILE`, `-` for stdin) |
| `rux agent ls [--json] [--watch]` | ID, agent, state, host, guest, age, push branch and activity; `--watch` refreshes every 2 seconds |
| `rux agent wait [ID...] [--any] [--timeout D] [-n LINES]` | wait until they stop working (default: every running agent; `--timeout` 10m, then run it again), then print the table and the end of each screen |
| `rux agent read ID [-n 80]` | the end of the agent's screen |
| `rux agent send ID [TEXT] [--key KEY] [--no-submit]` | type TEXT and press Enter (`-` reads stdin; several lines go in as one paste); `--key` presses a key instead: enter, escape, up, down, tab, ctrl+c, 1… |
| `rux agent focus ID` | focus its pane |
| `rux agent stop ID [--keep-guest]` | close its pane and remove its guest |

### Spawn flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--name` | required | the pane's label and the ID (a taken ID gets `-2`, …) |
| `--agent claude\|codex` | `claude` | the CLI in the guest |
| `--repo` | this checkout's origin | `owner/name`, an https URL, or `none` for an empty guest |
| `--branch` | the repo's default | the branch to start from |
| `--push` | `rux/ID` | the branch the agent pushes to |
| `--guest-type` | `container` | `container`, `linux`, `macos`, `fly` or `railway` |
| `--guest NAME` | | work in an existing guest, in its own checkout; `stop` leaves the guest running |
| `--host` | this machine | a Rex host to run the guest on (see [remote hosts](remote-hosts.md)) |
| `--memory` | 4G for containers | guest memory, such as `8G` |
| `--model` | the CLI's | the CLI's model |
| `--permission-mode` | skipped | `claude --permission-mode`, instead of skipping permission prompts |
| `--beside` | `$REX_BLOCK` | the pane to open beside |

### How it behaves

- `spawn` splits a pane beside yours (then stacks under the newest agent). It runs `rox up` (on `--host` if
  given), then `rox shell` into the guest, and types in `claude --dangerously-skip-permissions` (or `codex
  --dangerously-bypass-approvals-and-sandbox`) with the task. It answers the CLI's first-run folder-trust
  and permissions dialogs, since the guest is a fresh sandbox.
- Agents clone the current checkout's `origin` by default and push `rux/<id>`; nobody merges but you.
- rux drives panes only through Rex (send, capture, program status) and never parses the agent's output.
- The state is `provisioning`, `connecting`, `failed`, `stopped` or `exited` (its pane closed) until the agent runs. Then it's
  what the CLI reports to Rex: `working`, `idle`, `blocked:KIND` (it needs you), `done` or `error`, or `quiet` when the pane
  reports nothing and its screen has stopped changing.
- `wait` counts `idle`, `done`, `blocked` and `error` as stopped working, but gives an agent 20 seconds after it starts or
  gets a `send` to take its prompt. It returns at once when nothing is working.
- `stop` marks the agent stopped first, so a pane that's still setting up can't move it on. If `rox up` is still running, the
  pane lets it finish, learns the guest's name and removes the guest itself (`stop` waits up to ten minutes, then leaves
  it to the pane). If removal fails, run `stop` again. `--keep-guest`, or an existing guest, keeps the guest.

To watch the agents, use [`rux monitor`](monitor.md).
