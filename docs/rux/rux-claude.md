---
sidebar:
  order: 4
---

# rux claude

`rux claude` makes your Rex pane Claude Code itself, as the lead agent, with sub-agents in their own guests and Rex panes. Use it for your Claude subscription and Claude Code's own UI.

## rux claude: Claude Code as the lead

`rux claude [--model M] [PROMPT]` (alias `rux lead`) runs in a Rex terminal, and needs `claude` installed.
The sub-agents are Claude Code or Codex, each in its own guest and Rex pane. The lead delegates with [`rux agent spawn`](rux-agent.md), follows the
sub-agents with `rux agent wait`, `read` and `send`, and reports back.

Before starting sub-agents, the lead settles anything you didn't say, in one question with its defaults: the repo (this checkout's origin), the branch (the repo's default), where they run (a new Apple container guest; or a Linux VM, macOS VM, Fly.io or Railway guest; or an existing guest from `rox ls`), and the agent (Claude Code or Codex).

`rux claude` also:

- shows the sub-agents' states in Claude Code's status line (`rux statusline`: working, waiting for you, starting, done, failed),
- lets the lead run `rux agent`, `rox ls`, `rox inspect`, `rix status`, `rex hosts`, `git remote`, `git status`
  and `git log` without asking, and
- keeps the lead's rux skill current with this rux.

The lead runs on your machine with Claude Code's normal permissions; only the sub-agents run in sandboxes.

## Sub-agent guests

These apply to guests started by `rux agent spawn` (and so by `rux claude`):

- GitHub repos are cloned over SSH, with your key from rix's Git agent, so private repos work and agents can push.
- Without a branch, rox uses the repo's default branch.
- New container guests get 4 GB of memory (`rux agent spawn --memory` changes it; the harness always uses 4 GB): an agent and a project's installs need more than Apple container's 1 GB default.

See [rox](../rox/index.md) for guest types and [rix](../rix/index.md) for the Git agent.
