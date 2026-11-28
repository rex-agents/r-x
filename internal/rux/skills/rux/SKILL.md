---
name: rux
description: Orchestrate sub-agents with rux in Rex. Each sub-agent is Claude Code or Codex in its own disposable Rox guest, in a Rex pane beside you, which you start, watch, message and stop with `rux agent` commands. Use when the user asks to orchestrate, delegate, fan out, or parallelize work across sub-agents or guests, or mentions rux.
---

# Orchestrate sub-agents with rux

You are the orchestrator. You plan, delegate, check and report; the sub-agents do the work. Each sub-agent
runs an agent CLI (`claude` by default, or `codex`) in its own Rox guest, a sandboxed machine with the repo
cloned, in a Rex pane that rux splits beside your own pane. The user can watch every pane and type into it.

rux runs no daemon: every `rux agent` command reads the agents' state files and talks to Rex directly. It
needs `REX_SESSION` and `REX_BLOCK`, which Rex sets in your terminal, so run it from your own shell.

## Before you start

- Check `rux`, `rox` and `rex` are on PATH. Guests get the agent CLI's credentials from rix: if a
  sub-agent's pane says it isn't signed in, tell the user to run `rix auth claude` (or `rix auth codex`)
  instead of working around it.
- Run from the project's checkout. Sub-agents clone its `origin` by default. Agents can only see what's
  pushed, so if the task depends on local commits or changes, ask the user before you push them anywhere.

## Delegate

```sh
rux agent spawn --name api-tests "Add integration tests for ... Run them with ... "
rux agent spawn --name docs --branch release-2 --agent codex --prompt-file /tmp/docs-task.md
```

- One spawn per well-scoped, mostly independent task. Write each prompt so an agent with no other context
  can finish it: the goal, the files or areas involved, constraints, and how to check the work (the build or
  test command). Agents can't see you, each other, or this machine. They share work only through git.
- Each agent pushes its work to `rux/ID` (set another branch with `--push`). Tell agents that depend on
  another's work which branch to fetch. Agents never merge, and neither do you: the user merges.
- Options: `--repo owner/name` (or `none` for an empty guest), `--branch` (the branch to start from),
  `--guest-type container|linux|macos|fly|railway` (the runtime for a new guest; container is the
  default and quickest), `--guest NAME` (run in an existing guest from `rox ls --json` instead; the
  agent gets its own checkout there, and stopping the agent leaves the guest running), `--host LABEL`
  (run the guest on a Rex host from `rex hosts`), `--agent claude|codex`, `--model`, `--memory 8G`.
  Long prompts go in `--prompt-file FILE` (or `-` for stdin).
- `spawn` returns at once. The guest takes a minute or two to start. Each guest uses memory, so start only
  as many agents as the work needs, about 3 to 5 at a time unless the user says otherwise.

## Watch and steer

```sh
rux agent wait                      # until every running agent stops working (default timeout 10m)
rux agent wait api-tests --any      # until the given ones (or any one of them) stop
rux agent ls                        # states: provisioning, connecting, working, idle, blocked, done, failed, stopped, exited
rux agent read api-tests -n 120     # the end of its screen
rux agent send api-tests "Also cover the 404 case."
rux agent send api-tests --key enter   # or escape, down, tab, ctrl+c, or a digit for a numbered choice
```

- `wait` prints each agent's state and the last lines of the screen of each one that stopped. An agent ends
  its turn with a `DONE:` summary or a `BLOCKED:` question. Read more with `read` when you need it.
- In Claude Code, run `rux agent wait` with `run_in_background` so you get a notification when it returns,
  and keep working meanwhile. In Codex or other harnesses, run it in the foreground and run it again if it
  times out. Don't poll with `ls` in a loop.
- A new agent may stop at a confirmation dialog (such as a bypass-permissions warning or folder trust).
  Read its screen and answer it with `send --key`. A `blocked` state means it's waiting for input.
- If an agent goes wrong, redirect it with `send`, interrupt it with `--key escape`, or stop it and start a
  better-briefed one.
- `rux agent focus ID` shows its pane, when the user asks to see it.

## Finish

- Check each agent's result: read its summary, then fetch its branch and look at the diff or run the tests
  here if that's cheap: `git fetch origin rux/ID && git diff ...origin/rux/ID`.
- Stop each agent once its work is pushed or no longer needed: `rux agent stop ID` closes its pane and
  removes its guest (`--keep-guest` keeps the guest; `rox shell GUEST` gets back in).
- Report to the user briefly: what each agent did, its branch, anything unfinished, and what you checked.
  Stop any agents you started before you finish, unless the user wants them kept.
