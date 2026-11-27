---
sidebar:
  order: 2
---

# Quick start

From nothing to a lead agent that starts a sub-agent in its own guest.

## 1. Install

```sh
brew install rex-agents/tap/r-x
```

You also need a container engine for the default backend (Apple's `container`, Docker or Podman) and `rex`; see [installation](installation.md#prerequisites).

## 2. Store a credential with rix

Guests start signed out. rix keeps the credentials on this machine and pushes them only into guests that have the agent:

```sh
claude setup-token    # makes a Claude subscription token (an Anthropic API key works too)
rix auth claude       # paste it; stored mode 0600
rix status            # each guest's sign-in, Git agent and last sync
```

Add `rix key add ~/.ssh/id_ed25519` if you want a specific Git key; without one, guests get the keys ssh would use from `~/.ssh`. For rux's own model, either store an API key with `rix secret set ANTHROPIC_API_KEY`, or rely on Claude Code or Codex on this machine.

## 3. Boot a guest with rox

In a Git checkout:

```sh
cd ~/code/web
rox up container --agents claude   # a new guest with this project cloned in and Claude Code signed in
rox ls                             # the guests with this project
rox shell NAME                     # a shell in the checkout, inside the guest
rox down NAME                      # delete it when you're done
```

rox clones the remote branch you're on; pass `--local` to carry unpushed commits and uncommitted files. Outside a checkout, name the repository with `--repo git@github.com:owner/web.git`. To have a project always get Claude Code, put `{"agents": {"claude": true}}` in `.rox.json`.

## 4. Run rux and spawn a sub-agent

You don't need to boot a guest for rux: it starts its own.

```sh
cd ~/code/web
rux agent "fix the flaky auth tests"
```

You talk to a lead agent on your machine. Ask it to hand work off, for example *"spawn an agent to update the docs while you fix the tests"*. The lead calls `spawn_agent`, rox starts a new container guest, and the sub-agent works on a branch `rux/NAME` and pushes it with your key through rix's Git agent. Its report arrives in the lead's conversation when its turn ends, and you merge.

In the window, `tab` switches agents, `esc` interrupts the selected one, `ctrl+o` lists every command, and `ctrl+c` on an empty prompt quits. When you quit, rux asks whether to remove the guests it started. Choose the lead's and sub-agents' models with `--model` and `--agent-model` (for example `rux agent --model claude-code`).

Next: the [rux overview](../rux/index.md), [rix](../rix/index.md) and [rox](../rox/index.md).
