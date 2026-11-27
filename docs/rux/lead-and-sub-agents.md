---
sidebar:
  order: 1
---

# Lead and sub-agents

Run `rux` to talk to a lead agent that starts sub-agents, each in its own rox guest, with every conversation live in one window. This page covers the window, its keys and commands, the lead's tools and a sub-agent's life.

## rux: the multi-agent harness

Run `rux` (or `rux "what to do"`) in any terminal. rux is its own agent harness: you talk to a lead
agent, the lead starts sub-agents, and every agent's conversation is live in one window, with the
agents in a sidebar and the selected one's transcript beside it.

```
 ◆ rux  lead · anthropic/claude-opus-5-5 · this machine        2 sub-agents · 1 working
 AGENTS                 │ ❯ fix the flaky auth tests and update the docs
                        │
 ● lead                 │ ⏺ I'll split this between two agents.
   ready                │
                        │ ⏺ spawn_agent(fix-auth-tests)
▌⠙ fix-auth-tests       │   ⎿  Started fix-auth-tests in a new container guest…
   bash go test ./...   │
   claude-opus-5-5 · …  │ ◂ docs
                        │   Ended its turn:
 ● docs                 │   Updated docs/auth.md; pushed rux/docs.
   done · waiting       │
╭───────────────────────────────────────────────────────────────────────────────────╮
│ ❯ Message the lead…                                                               │
╰───────────────────────────────────────────────────────────────────────────────────╯
 enter send · tab switch agent · esc interrupt · ctrl+o details · ctrl+c quit
```

- **The lead** runs on your machine, in the current directory. It reads code freely (`read_file`,
  `grep`, `list_files`); its `bash` asks you first (`y` allow, `a` allow for the session, `n` deny).
  Its other tools run sub-agents: `spawn_agent`, `message_agent`, `wait_for_agents`, `agent_status`,
  `read_agent`, `stop_agent`, `list_guests` and `list_hosts` (see [the lead's tools](#the-leads-tools)). A lead
  on Claude Code or Codex has its own tools instead, and gets the same rux tools over MCP.
- **Each sub-agent** works in its own rox guest: a new Apple container (4 GB) by default, or a Linux
  or macOS VM, Fly.io or Railway, or an existing guest. rox clones the repo (this directory's origin
  by default, over SSH) and rux switches to a new branch `rux/NAME`. A sub-agent on a model API has `bash`,
  `read_file`, `write_file`, `edit_file`, `grep` and `list_files`, all running in the guest; one on
  `claude-code` or `codex` runs that CLI in the guest instead. It pushes its branch
  with your key through rix's Git agent. Only you merge.
- **Reports flow back by themselves.** When a sub-agent ends its turn, its report arrives in the lead's
  conversation as a message from it, and the lead picks it up. (If the lead is inside `wait_for_agents`
  for that agent, the report comes back as the tool's result instead.) An interrupt or an error is reported
  the same way.
- **You can talk to any agent.** `tab` selects one; what you type goes to it. A working agent reads
  your message after its current tool calls. `esc` interrupts the selected agent.
- **Every model loop runs on your machine**, so model keys never enter a guest; only tool calls do.
- When you quit (`ctrl+c` on an empty prompt, `ctrl+d`, or `/quit`) and rux started guests, it asks whether to remove them
  (`y` removes, `n` keeps, `esc` cancels; a guest that was there before is never removed). It prints the kept guests'
  names (remove them later with `rox down NAME`) and saves every transcript, as `NAME.json` and `NAME.md`, to
  `~/.rux/sessions/TIME/`.
- Run it with `rux [--model M] [--agent-model M] [--effort LEVEL] [PROMPT...]`; the words after the flags are the lead's
  first message. A lone lowercase word is taken for a mistyped command, so add a word or quote a one-word prompt with a space.
  See [models](models.md) for what `--model`, `--agent-model` and `--effort` take.

The window is quiet: the conversation, the agents in a strip above the prompt once there are
sub-agents, and the prompt, with the selected agent's model and effort on its top border and where it
works on its bottom border. Most things can be clicked: an agent in the strip, the model (opens the
picker), a tool call (opens its output), the buttons on approvals and on quitting, and the selected
sub-agent's pane, shell and stop buttons.

### Keys

| Key | Does |
| --- | --- |
| `enter` | send to the selected agent (`shift+enter` for a new line in terminals that report it, such as kitty, Ghostty, WezTerm, foot and iTerm2; `alt+enter` or `ctrl+j` everywhere) |
| `tab` / `shift+tab` | next / previous agent |
| `esc` | interrupt the selected agent |
| `ctrl+o` (or `ctrl+k`) | commands: every action below, to pick, search or click |
| `ctrl+l` | model and effort for the selected agent |
| `?` | shortcuts (on an empty prompt) |
| `ctrl+e` | open every tool call |
| `pgup` / `pgdn` (or `ctrl+u` / `ctrl+f`), `home` / `end`, mouse wheel | scroll |
| `ctrl+c` | clear the prompt; on an empty prompt, quit |
| `ctrl+d` | quit |

`ctrl+n` and `ctrl+p` also switch agents. While a tool call waits for your approval, `y` allows it, `a` allows that tool
for the rest of the session, and `n` or `esc` denies it.

### Commands

| Command | Does |
| --- | --- |
| `/model [PROVIDER/MODEL]` | the selected agent's model (no argument: the picker) |
| `/agent-model [PROVIDER/MODEL]` | the model new sub-agents start with |
| `/effort LEVEL` | the selected agent's effort: low, medium, high, xhigh, max or auto |
| `/pane [NAME]` | open an agent in a Rex pane beside rux (`rux view`), live, where you can message and interrupt it |
| `/shell [NAME]` | open a shell in a sub-agent's guest, in a Rex pane |
| `/stop [NAME]` | stop a sub-agent and remove its guest (an existing guest it was given is kept) |
| `/quit` (`/exit`, `/q`) | quit |
| `/help` | the shortcuts |

`/view` is another name for `/pane`. With no `NAME`, `/pane`, `/shell` and `/stop` use the selected agent (`/shell` and
`/stop` need a sub-agent; `/pane lead` opens the lead). `/pane` and `/shell` need a Rex terminal.
`rux view [--sock PATH] [NAME]` is what `/pane` runs: it shows one agent of a running rux, live, and what you type
there goes to that agent (`esc` interrupts it, `ctrl+e` opens tool calls). It finds rux through its control socket,
`~/.rux/run/PID.sock` (owner-only), and by default takes the newest.

The model picker lists a few picks with what each is good for, every provider's models under
"Other models" (or type to search them all), and the effort, which `←` `→` change.

## The lead's tools

A Claude Code or Codex lead gets these as an MCP server named `rux`, on `127.0.0.1` with a random token, for that session
only. `wait_for_agents` can run for as long as the sub-agents do.

| Tool | Parameters (* required) | Does |
| --- | --- | --- |
| `spawn_agent` | `name`*, `task`*, `repo`, `branch`, `guest_type`, `guest`, `host`, `model` | start a sub-agent; returns at once while it sets up. `repo` is `owner/name`, a git URL, or `none` (default: this directory's origin, over SSH). `guest_type` is `container` (default), `linux`, `macos`, `fly` or `railway`; `guest` reuses an existing guest instead; `host` is a Rex remote host; `model` defaults to `--agent-model`. A taken name gets a numeric suffix. |
| `message_agent` | `agent`*, `message`* | a working agent reads it after its current tool calls. It fails for a stopped or failed agent: start a new one. |
| `wait_for_agents` | `agents` (names; default: every agent that's starting or working), `timeout_seconds` (default 1800) | wait until they end their turns, and return their reports. It returns early if you message the lead. Stopped or failed agents are never waited on: they come back as errors, with any messages they never read. |
| `agent_status` | none | each sub-agent's status, guest, model and the start of its last report |
| `read_agent` | `agent`*, `entries` (default 30) | the end of a sub-agent's transcript |
| `stop_agent` | `agent`*, `keep_guest` | stop it and remove its guest (see below) |
| `list_guests` | none | your rox guests (`rox ls --all-projects`), which `spawn_agent` can reuse with `guest` |
| `list_hosts` | none | this machine and your Rex remote hosts, which `spawn_agent` can start guests on with `host` |

## A sub-agent's life

Its status, in the sidebar and in `agent_status`, is `starting` (its guest is being set up; the detail line shows rox's latest
output), `working`, `waiting` (its turn is over; it waits for a message), `failed` or `stopped`.

1. `spawn_agent` creates the agent and sends it the task. For a new guest rux runs `rox up RUNTIME --repo … --project …
   [--branch B] --agents claude|codex` (`--memory 4G` for containers, `--no-project` with no repo, `--no-tailscale`
   unless the guest or host needs the tailnet); for an existing guest it runs `rox add GUEST --repo …`, which gives the
   agent its own checkout. It then reads the guest with `rox inspect`, switches to `rux/NAME`, and starts the engine.
2. If setup fails the agent is `failed`, the lead is told, and a guest rox had already named is kept (`stop_agent`
   removes it). A missing CLI or sign-in says how to fix it, such as `rox add GUEST --agents codex` or `rix auth codex`.
3. When a turn ends, its report goes to the lead, and the agent waits for a message from the lead or you.
4. Stopping (`stop_agent`, `/stop`, the command menu) is final: the agent stays `stopped` and reads no more messages.
   It works while the guest is still being set up, too: a `rox up` that has started is allowed to finish, so the guest
   it made is known, and rux removes that guest as soon as rox returns. If removal fails, the guest's name is kept and
   stopping again retries it. An existing guest is never removed, and `keep_guest` keeps one rux started.
5. Quitting rux stops every agent and offers to remove the guests.
