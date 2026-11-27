# rux

rux runs agents in disposable rox guests. It has three modes:

- **`rux`**: a multi-agent harness in one terminal window: a lead agent, sub-agents in guests, every conversation live
  (see [rux: the multi-agent harness](#rux-the-multi-agent-harness)).
- **`rux claude`** and **`rux agent`**: Claude Code (or Codex) as the lead in a Rex pane, with sub-agents in Rex panes
  (see [rux claude](#rux-claude-claude-code-as-the-lead) and [`rux agent`](#orchestrating-from-claude-code-or-codex-rux-agent)).
- **`rux serve`**: Claude Code threads in guests, driven from a phone over Tailscale (described first, below).

rux ships with rox and rix in the r-x release, and stores no credentials: see [Credentials come from rix](#credentials-come-from-rix).
`rux help` lists every command.

```
phone (PWA) ──HTTPS (tailscale serve)──▶ rux serve        (Mac: web app, thread store, runs `rox up`)
                                          ▲ WebSocket, dialed by the guest
                                     rux runner           (in each guest: owns `claude -p` stream-json)
```

- The runner owns the `claude` process and keeps a numbered event log, so restarting `rux serve`
  doesn't kill a thread. On reconnect it replays whatever the server is missing.
- If `claude` exits, the next message restarts it with `--resume` on the same session.
- Messages to a thread (the first prompt, what you type, a manager's wake-ups) wait in
  `outbox.jsonl` until the runner acks them. The server sends every unacked message each time a
  runner connects, under a stable `id`; the runner stores a message in `~/.rux/THREAD/inbox/` before
  it acks, and ignores an id it already dispatched. A dropped connection, a restart or a sleeping
  guest therefore doesn't lose a message (durable delivery). Delivery to claude has three steps: the
  runner announces the turn with a `rux_user` event (id as `mid`), writes the whole message to
  claude's stdin pipe, and then records a `rux_delivered` event with the id. That record means the
  pipe write completed (dispatch), not that claude has started, finished or even read the turn. If
  claude can't start or the write fails, the runner keeps the message and retries with a backoff,
  without announcing it again. A crash after the write but before the `rux_delivered` record replays
  that one message on restart: at-least-once, not exactly-once. Runners from
  before acks (no `acks` in hello) get each message once, best effort, as before, and a new runner
  still takes messages with no id.
- If the repo can't be cloned, the thread is `failed` with a `rux_setup_failed` event (the manager is
  told too) and claude doesn't start in an empty directory. The next message tries the clone again,
  into a fresh directory beside the workspace; a directory that already has files is never replaced.
  A thread with no repo still starts in a new empty git repo.
- Threads are stored in `~/.rux/threads/ID/` (`meta.json` and `events.jsonl`).
- A thread starts from a repo (picked from your GitHub list, `owner/name`, or an HTTPS URL) or from a
  blank workspace (an empty git repo, for starting something new).

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

The window is quiet: the conversation, the agents in a strip above the prompt once there are
sub-agents, and the prompt, with the selected agent's model and effort on its top border and where it
works on its bottom border. Most things can be clicked: an agent in the strip, the model (opens the
picker), a tool call (opens its output), the buttons on approvals and on quitting, and the selected
sub-agent's pane, shell and stop buttons.

| Key | Does |
| --- | --- |
| `enter` | send to the selected agent (`alt+enter` for a new line) |
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

### The lead's tools

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

### A sub-agent's life

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

### Remote hosts

Every model loop runs where rux runs; only tool calls go to the guests. The lead can start a
sub-agent's guest on one of your Rex remote hosts (`spawn_agent` with `host`; `list_hosts` lists them),
which rox puts on the tailnet so rux reaches it with `rox exec`. An existing guest on another host works
the same way. Run rux itself on a remote host (in a Rex pane there) to use that host's rix.

### Models: Claude Code, Codex, Claude, or anything OpenAI-compatible

Name a model as `provider/model`. The lead uses `--model`; the sub-agents use `--agent-model`
(default: the lead's), and the lead can pick one per agent. Without `--model`, rux uses
`lead_model` from `~/.rux/harness.json`, else `anthropic/claude-opus-5-5` when there's an Anthropic API key, else
`claude-code` when `claude` is installed, else `codex` when `codex` is.

Change models during a session with `/model`: it picks the selected agent's provider (showing which
have keys), then its model from the provider's own list (type to filter, or type a model it doesn't
list). The agent carries on with the conversation so far. `/agent-model` sets the model for sub-agents
started from then on. Switching waits for the agent's turn to end; `esc` interrupts it.

**`codex`** (or `codex/MODEL`) runs each agent on OpenAI's Codex CLI, headless (`codex exec --json`),
so your ChatGPT sign-in works. A Codex lead works in Codex's read-only sandbox here, so it can look but
not change anything; rux's tools reach it over MCP. Codex sub-agents run in their guests without
Codex's sandbox and sign in with what `rix auth codex` stored.

**`claude-code`** (or `claude-code/MODEL`, such as `claude-code/opus`) runs each agent on Claude Code
itself, headless, so your Claude subscription works. Claude Code runs its own tools and rux shows its
work in the same window. The lead's Claude Code runs on your machine: Read, Grep and Glob run freely,
the rux tools come from rux over MCP, and anything else (Bash, edits) asks you in rux first, unless your
Claude Code settings already allow it or it's a command Claude Code knows only reads. A sub-agent's
Claude Code runs in its guest with permissions skipped, since the guest is its sandbox; rox installs it
and rix signs it in with what `rix auth claude` stored.

```
rux --model anthropic/claude-opus-5-5
rux --model openai/gpt-5 --agent-model openrouter/qwen/qwen3-coder
rux --model ollama/qwen3-coder          # a local model, no key
rux --model claude-code                 # Claude Code, with your Claude subscription
```

Claude goes through the Anthropic Messages API, with adaptive thinking, prompt caching, server-side
compaction for long sessions, and the server-side refusal fallback on the models that take it.
`--effort` (or `effort` in the config) sets low, medium, high (default), xhigh or max. Each agent has its own,
which `/effort` or the picker's `←` `→` change (`auto` is the engine's default). Every other provider speaks the OpenAI chat
completions API with streaming tool calls.

Built in: `anthropic`, `openai`, `openrouter`, `groq`, `deepseek`, `xai`, `together`, `gemini`,
`mistral`, `ollama` and `lmstudio`. Each key is a rix secret named for the provider
(`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `OPENROUTER_API_KEY`, …); the environment is the fallback, and
Anthropic also takes an `ant auth login` profile. rux tells you which secret to set when one is missing.

```
rix secret set ANTHROPIC_API_KEY     # create one at https://platform.claude.com/settings/keys
```

Add other OpenAI-compatible servers, and set defaults, in `~/.rux/harness.json` (or the file `RUX_HARNESS_CONFIG` names);
command-line flags override it. A provider's `kind` is `openai` (the default) or `anthropic`, `key` names the rix secret (then
environment variable) with its API key, and `headers` adds request headers:

```json
{
  "lead_model": "anthropic/claude-opus-5-5",
  "agent_model": "anthropic/claude-sonnet-5-5",
  "effort": "high",
  "providers": {
    "vllm": {"kind": "openai", "base_url": "http://gpu-box:8000/v1", "key": "VLLM_API_KEY"}
  }
}
```

## rux claude: Claude Code as the lead

`rux claude [--model M] [--no-monitor] [PROMPT]` (alias `rux lead`) runs in a Rex terminal, and needs `claude` installed.
It makes your pane Claude Code itself, as the lead agent, with sub-agents that are Claude Code or Codex in their own guests and Rex panes. Use it for your
Claude subscription and Claude Code's own UI. The lead delegates with `rux agent spawn`, follows the
sub-agents with `rux agent wait`, `read` and `send`, and reports back.

Before starting sub-agents, the lead settles anything you didn't say, in one question with its defaults: the repo (this checkout's origin), the branch (the repo's default), where they run (a new Apple container guest; or a Linux VM, macOS VM, Fly.io or Railway guest; or an existing guest from `rox ls`), and the agent (Claude Code or Codex).

`rux` also:
- opens `rux monitor` in a short pane below the lead (`--no-monitor` to skip it),
- shows the sub-agents' states in Claude Code's status line (`rux statusline`: working, waiting for you, starting, done, failed),
- lets the lead run `rux agent`, `rux monitor`, `rox ls`, `rox inspect`, `rix status`, `rex hosts`, `git remote`, `git status`
  and `git log` without asking, and
- keeps the lead's rux skill current with this rux.

The lead runs on your machine with Claude Code's normal permissions; only the sub-agents run in sandboxes.

### rux monitor

The monitor follows every sub-agent in this Rex session, as Rex reports it: working, waiting for you, needs you, or done. It also shows the last lines of the selected agent's screen, every rox guest with its CPU and memory, and the host's load.

| Key | Does |
| --- | --- |
| `enter` | go to the agent's pane |
| `m` | type a line into the agent's pane |
| `x` | stop the agent: `y` removes its guest, `k` keeps it (a guest that was there before is always kept) |
| `o` | open a shell in the guest |
| `n` | go back to the lead |
| `tab` | move between agents and guests |
| `r` | refresh |
| `q` / `esc` | quit the monitor |

Move with `↑` `↓` (or `j` `k`). `rux agent ls --watch` is the plain-text version.

### Sub-agent guests

- GitHub repos are cloned over SSH, with your key from rix's Git agent, so private repos work and agents can push.
- Without a branch, rox uses the repo's default branch.
- New container guests get 4 GB of memory (`rux agent spawn --memory` changes it; the harness always uses 4 GB): an agent and a project's installs need more than Apple container's 1 GB default.

## Credentials come from rix

rux stores no credentials. Everything it needs comes from rix, on the Mac running `rux serve`:

| What | From | Set it with |
| --- | --- | --- |
| Claude Code in thread guests, Claude Code or Codex in `rux agent` guests | rox's hooks into rix, because rux starts guests with `rox up --agents claude` (or `codex`) | `rix auth claude`, `rix auth codex` |
| GitHub, for the repo picker and for git in guests | `rix credential github` | `rix auth github` (a token), or nothing to use `gh`'s login |
| Secrets that actions list | `rix secret get` | `rix secret set NAME` |

rux's own per-thread tokens only let a guest's runner talk to its own thread.

### GitHub access

- The repo picker lists what the account can see (`GET /user/repos` with rix's token).
- The runner clones the repo inside the guest. Git there, for the clone and for anything Claude runs,
  uses `rux git-credential`, which fetches the token from `rux serve` when git needs it. `rux serve`
  asks rix for it, at most every five minutes. The token isn't written to the guest's disk or git
  config. Commits use your GitHub name and noreply address.
- That token can reach everything the account can, so treat a guest like your own shell, or give rix
  a fine-grained token limited to the repositories agents work on. `rux serve --github=false` turns
  GitHub access off.
- Guests get no SSH keys: rux starts them with `rox up --no-git-agent`, and SSH remotes are rewritten
  to HTTPS.

## Screenshots and files

Claude in a guest is told (via `--append-system-prompt`) that you can't see its screen, and has two
commands for showing you things. Both upload over the runner listener with the thread's token, and
files are stored in `~/.rux/threads/ID/assets/` and shown inline in the thread:

- `rux screenshot URL [--mobile] [--height N] [--caption TEXT]`: headless Chromium (installed with
  `sudo apt-get` on first use), then shared.
- `rux share FILE... [--caption TEXT]`: any file up to 25 MB. Images and videos show inline, and
  anything else is a download.

Images Claude opens with its Read tool also show up in the thread.

## Rich UI, diffs and dashboards

Agents can show UI in their thread, not just text: `rux ui spec.json` renders a
[json-render](https://json-render.dev) style spec with the app's own components (`web/ui.js`):
layout, Stat tiles, Meter, Table, List, Timeline, Chart (line, area, bar, stacked bar, sparkline),
Graph, Code, Diff and Image, plus inputs (Choice, Select, TextInput, Toggle, Button).

- `rux ui --ask spec.json` asks a question. Your answer (the form values and which button) goes back
  to the agent as a message, so you can answer whenever.
- `rux diff [git diff args]` shows a diff with a file browser, unified or split view, and full
  screen. By default it shows uncommitted changes, including new files.
- `rux ui --catalog` prints the format and components; agents read it before first use.
- Managers have a `thread_stats` tool and are told to answer "how's it going?" with a dashboard.

## Projects and managers

Threads are grouped into projects, one per repo; blank threads go in `scratch`. A project has defaults
for new threads (branch, model, effort) and notes that every thread in it gets in its system prompt.

The project page is a conversation with the project's **manager**: a long-lived thread, started by
your first message, with the repo checked out. It runs worker threads through an MCP server that
`rux serve` provides on the runner listener (`/mcp`), scoped to its own project: it can start,
message, read, interrupt and remove workers, view their shared files, and edit the project notes.
When one of its workers finishes a turn or fails, it gets a `[rux] ...` message automatically.
Workers push branches; only you merge.

## Actions

Actions are jobs that run on the rux host (your Mac) and that agents in guests can trigger: building a Mac app, publishing to itch.io, anything that needs the Mac's tooling or secrets the guests mustn't see. Think of it as CI/CD on your own machine that your agents drive.

An action is a folder `~/.rux/actions/NAME/` with a `tool.json` and a script. `examples/rux-actions/` in this repo has examples (`cp -R examples/rux-actions/itch-publish ~/.rux/actions/`).

```json
{
  "description": "what it does, for the agent",
  "args": [{"name": "source", "type": "file", "description": "..."}],
  "settings": [{"name": "target", "description": "set per project in the app"}],
  "secrets": ["BUTLER_API_KEY"],
  "approval": "ask",
  "timeout": "30m",
  "run": "./run.sh"
}
```

- Actions are off until you turn them on in a project's settings (⋯ → settings → actions), where you also fill in their settings.
- In a guest, `rux action` lists the enabled actions and `rux action NAME --source . ...` runs one. `file` arguments (a file or folder) are uploaded to the host. For a git checkout, only the files git keeps are sent.
- `approval`: `ask` (the default) shows a card in the thread with Run it, Always allow here, and Deny. `every-run` always asks, and `none` never asks.
- Secrets come from rix: `rix secret set NAME` stores one, and `rix secret import ~/.rux/secrets.env` moves an old secrets file in (and deletes it). Only the secrets an action lists are fetched and passed to it, and their values are scrubbed from its output.
- The script runs in the action's folder with `RUX_ARG_<NAME>`, `RUX_SETTING_<NAME>`, `RUX_GIT_BRANCH`, `RUX_GIT_COMMIT`, `RUX_PROJECT`, `RUX_REPO` and `RUX_BIN` (the rux binary), plus these paths:
  - `RUX_OUT`: files written here are shared into the thread.
  - `RUX_RESULT`: a short summary written here goes back to the agent.
  - `RUX_STATE`: a folder that persists between runs.
- Output streams into the thread. A run that's waiting for approval or takes longer than `--wait` (default 90s) reports back to the agent as a `[rux]` message when it ends.
- Actions run as you. Anything that runs code from a guest, like a build, must go through `rux sandbox DIR -- CMD`. That's a macOS sandbox with no network, no Apple Events or system services, and nothing in your home folder readable or writable except DIR.

`itch-publish` builds the game on the Mac with the project's build command (default `make dist`, sandboxed) and pushes the output (default `build/itch`) with butler. Each branch goes to its own channel (`mac-BRANCH`), and the main branch goes to `mac`. It can also push a finished build with `--build`. Set `target` (`user/game`) in the project's settings, and store `BUTLER_API_KEY` with `rix secret set BUTLER_API_KEY`; `butler login` saves one in `~/Library/Application Support/itch/butler_creds`.

## Orchestrating from Claude Code or Codex (`rux agent`)

In a Rex terminal, your own Claude Code or Codex session can be the orchestrator, and rux gives it sub-agents in Rex
panes (`rux claude` starts Claude Code that way and keeps the skill current). For any other session, install the skill
once, then ask your agent to fan work out.

```sh
rux skill install          # into ~/.claude/skills/rux and ~/.codex/skills/rux
rux skill show             # print the skill
```

`install` takes `--agent claude|codex` (repeatable; default: each one with a `~/.claude` or `~/.codex`), `--dir SKILLS_DIR`
and `--force` (replace a `rux` folder that isn't this skill). The skill teaches the agent these commands. There's no
daemon: state is one JSON file per agent in `~/.rux/agents/<rex session>/`. Every command takes `--session S`
(default `$REX_SESSION`).

| Command | Does |
| --- | --- |
| `rux agent spawn --name NAME [flags] PROMPT` | start a sub-agent in a new pane (give the prompt as words, or with `--prompt-file FILE`, `-` for stdin) |
| `rux agent ls [--json] [--watch]` | ID, agent, state, host, guest, age, push branch and activity; `--watch` refreshes every 2 seconds |
| `rux agent wait [ID...] [--any] [--timeout D] [-n LINES]` | wait until they stop working (default: every running agent; `--timeout` 10m, then run it again), then print the table and the end of each screen |
| `rux agent read ID [-n 80]` | the end of the agent's screen |
| `rux agent send ID [TEXT] [--key KEY] [--no-submit]` | type TEXT and press Enter (`-` reads stdin; several lines go in as one paste); `--key` presses a key instead: enter, escape, up, down, tab, ctrl+c, 1… |
| `rux agent focus ID` | focus its pane |
| `rux agent stop ID [--keep-guest]` | close its pane and remove its guest |

`spawn` flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--name` | required | the pane's label and the ID (a taken ID gets `-2`, …) |
| `--agent claude\|codex` | `claude` | the CLI in the guest |
| `--repo` | this checkout's origin | `owner/name`, an https URL, or `none` for an empty guest |
| `--branch` | the repo's default | the branch to start from |
| `--push` | `rux/ID` | the branch the agent pushes to |
| `--guest-type` | `container` | `container`, `linux`, `macos`, `fly` or `railway` |
| `--guest NAME` | | work in an existing guest, in its own checkout; `stop` leaves the guest running |
| `--host` | this machine | a Rex host to run the guest on |
| `--memory` | 4G for containers | guest memory, such as `8G` |
| `--model` | the CLI's | the CLI's model |
| `--permission-mode` | skipped | `claude --permission-mode`, instead of skipping permission prompts |
| `--beside` | `$REX_BLOCK` | the pane to open beside |

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

## Run it

```sh
rux serve --allow-user you@example.com
tailscale serve --bg 7700       # HTTPS on your tailnet -> 127.0.0.1:7700
```

Open the `https://<machine>.<tailnet>.ts.net` URL on your phone, then Share → Add to Home Screen.

`rux serve` flags: `--listen` (default `127.0.0.1:7700`, the web app), `--allow-user LOGIN` (only this Tailscale login through
`tailscale serve`; recommended), `--runner-listen` (default `0.0.0.0:7701`), `--advertise HOST:PORT`, `--guest-cidrs`,
`--data DIR` (default `~/.rux`), `--backend rox|local`, `--rox BIN`, `--claude BIN` (only to list the models Claude Code
offers), `--memory` (default `4G`), `--sleep-after DURATION` (default 15m: stop an idle thread's guest, and start it again on
the next message; `0` keeps guests running), `--runner-bin PATH` and `--github` (default on).

Guests run the Linux `rux` for their architecture. A release build downloads it from its own release
the first time a guest asks, checks it against `checksums.txt`, and caches it in `~/.rux/runner/`.
A development build serves `rux-linux-arm64` or `rux-linux-amd64` from beside `rux` (`make install`
builds them).

### One-time host setup

```sh
brew install container && container system start
claude setup-token && rix auth claude    # Claude Code credentials for guests
rix auth github                          # optional: a GitHub token, instead of gh's login
```

Guests reach the runner listener at `--advertise`. By default that's the gateway of Apple
`container`'s default network (`container network inspect default`) on port 7701. Only addresses in
`--guest-cidrs` (default: that network's subnet) and loopback may connect, and each thread has its
own token.

### Testing without Rox

`rux serve --backend local` clones the repo on the host and runs the runner there. It is
**unsandboxed** and only for exercising the app.

## Not yet

For `rux serve` threads: push notifications, SQLite storage, multiple hosts and a scheduler. Tools are pre-allowed
(`--allowedTools`) and anything else is denied (`--permission-prompts none`), so a thread has no approval prompts.

## Environment and files

| Variable | Used for |
| --- | --- |
| `REX_SESSION`, `REX_BLOCK` | set by Rex; `rux claude`, `rux agent`, `/pane` and `/shell` need them (`rux agent --session` replaces the first) |
| `RUX_HARNESS_CONFIG` | the harness config's path, instead of `~/.rux/harness.json` |
| `RIX_BIN` | the `rix` binary rux runs for secrets and credentials |
| `NO_COLOR` | plain `rux help` |
| `RUX_UI_URL`, `RUX_ASSET_URL`, `RUX_CRED_URL`, `RUX_CRED_TOKEN` | set in a thread's guest for `rux ui`, `share`, `screenshot`, `action` and `git-credential` |

`~/.rux/harness.json` (config), `~/.rux/sessions/` (saved harness transcripts), `~/.rux/run/` (control sockets),
`~/.rux/agents/SESSION/` (`rux agent` state), `~/.rux/threads/` and `~/.rux/actions/` (`rux serve`), and `~/.rux/runner/`
(the cached guest binary).

## Troubleshooting

- **rox isn't installed, or `rox up` fails**: the sub-agent is `failed` with rox's last line. Fix the cause and start a new agent.
  A guest rox had already named is kept; `stop_agent` or `rox down NAME` removes it.
- **A model needs a key**: rux names the secret, such as `rix secret set OPENAI_API_KEY`.
- **Codex in a guest isn't signed in**: `rix auth codex`, then `rix apply GUEST`. For Claude Code, `rix auth claude`.
- **"GUEST has no claude" (or codex)** in an existing guest: `rox add GUEST --agents claude` (or `codex`).
- **Panes don't open**: `/pane`, `/shell`, `rux claude` and `rux agent` need a Rex terminal. `/pane` also needs the control
  socket; if rux printed "no control socket", `~/.rux/run` isn't writable.
- **A sub-agent ignores messages**: if it's `stopped` or `failed` that's final, and `message_agent` says so. Start a new one.
- **A guest is left over** (after a crash, or `n` on quit): `rox ls`, then `rox down NAME`.
- **`rux agent wait` returns at once**: nothing was working.
- **A thread's guest can't reach the server**: check `--advertise` and `--guest-cidrs`.
