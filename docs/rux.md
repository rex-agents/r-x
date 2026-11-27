# rux

Run Claude Code threads in disposable rox guests, driven from a phone over Tailscale, or give your
own Claude Code or Codex session sub-agents in Rex panes. rux ships with rox and rix in the r-x release.

```
phone (PWA) ──HTTPS (tailscale serve)──▶ rux serve        (Mac: web app, thread store, runs `rox up`)
                                          ▲ WebSocket, dialed by the guest
                                     rux runner           (in each guest: owns `claude -p` stream-json)
```

- The runner owns the `claude` process and keeps a numbered event log, so restarting `rux serve`
  doesn't kill a thread. On reconnect it replays whatever the server is missing.
- If `claude` exits, the next message restarts it with `--resume` on the same session.
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
  `read_agent`, `stop_agent` and `list_guests`.
- **Each sub-agent** works in its own rox guest: a new Apple container (4 GB) by default, or a Linux
  or macOS VM, Fly.io or Railway, or an existing guest. rox clones the repo (this directory's origin
  by default, over SSH) on a new branch `rux/NAME`. The sub-agent has `bash`, `read_file`,
  `write_file`, `edit_file`, `grep` and `list_files`, all running in the guest, and pushes its branch
  with your key through rix's Git agent. Only you merge.
- **Reports flow back by themselves.** When a sub-agent ends its turn, its report arrives in the lead's
  conversation as a message from it, and the lead picks it up.
- **You can talk to any agent.** `tab` selects one; what you type goes to it. A working agent reads
  your message after its current tool calls. `esc` interrupts the selected agent.
- **Every model loop runs on your machine**, so model keys never enter a guest; only tool calls do.
- When you quit, rux offers to remove the guests it started, and saves every transcript to
  `~/.rux/sessions/TIME/`.

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
| `ctrl+o` | commands: every action below, to pick, search or click |
| `ctrl+l` | model and effort for the selected agent |
| `?` | shortcuts (on an empty prompt) |
| `ctrl+e` | open every tool call |
| `pgup` / `pgdn`, mouse wheel | scroll |
| `ctrl+c` | clear the prompt; on an empty prompt, quit |

| Command | Does |
| --- | --- |
| `/model [PROVIDER/MODEL]` | the selected agent's model (no argument: the picker) |
| `/agent-model [PROVIDER/MODEL]` | the model new sub-agents start with |
| `/effort LEVEL` | the selected agent's effort: low, medium, high, xhigh, max or auto |
| `/pane [NAME]` | open an agent in a Rex pane beside rux (`rux view`), live, where you can message and interrupt it |
| `/shell [NAME]` | open a shell in a sub-agent's guest, in a Rex pane |
| `/stop [NAME]` | stop a sub-agent and remove its guest |

The model picker lists a few picks with what each is good for, every provider's models under
"Other models" (or type to search them all), and the effort, which `←` `→` change.

### Remote hosts

Every model loop runs where rux runs; only tool calls go to the guests. The lead can start a
sub-agent's guest on one of your Rex remote hosts (`spawn_agent` with `host`; `list_hosts` lists them),
which rox puts on the tailnet so rux reaches it with `rox exec`. An existing guest on another host works
the same way. Run rux itself on a remote host (in a Rex pane there) to use that host's rix.

### Models: Claude Code, Codex, Claude, or anything OpenAI-compatible

Name a model as `provider/model`. The lead uses `--model`; the sub-agents use `--agent-model`
(default: the lead's), and the lead can pick one per agent. Without `--model`, rux uses
`anthropic/claude-opus-5-5` when there's an Anthropic API key, and otherwise `claude-code`.

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
`--effort` sets low, medium, high (default), xhigh or max. Every other provider speaks the OpenAI chat
completions API with streaming tool calls.

Built in: `anthropic`, `openai`, `openrouter`, `groq`, `deepseek`, `xai`, `together`, `gemini`,
`mistral`, `ollama` and `lmstudio`. Each key is a rix secret named for the provider
(`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `OPENROUTER_API_KEY`, …); the environment is the fallback, and
Anthropic also takes an `ant auth login` profile. rux tells you which secret to set when one is missing.

```
rix secret set ANTHROPIC_API_KEY     # create one at https://platform.claude.com/settings/keys
```

Add other OpenAI-compatible servers, and set defaults, in `~/.rux/harness.json`:

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

`rux claude` (alias `rux lead`) runs in a Rex terminal and makes your pane Claude Code itself, as the lead
agent, with sub-agents that are Claude Code or Codex in their own guests and Rex panes. Use it for your
Claude subscription and Claude Code's own UI. The lead delegates with `rux agent spawn`, follows the
sub-agents with `rux agent wait`, `read` and `send`, and reports back.

Before starting sub-agents, the lead settles anything you didn't say, in one question with its defaults: the repo (this checkout's origin), the branch (the repo's default), where they run (a new Apple container guest; or a Linux VM, macOS VM, Fly.io or Railway guest; or an existing guest from `rox ls`), and the agent (Claude Code or Codex).

`rux` also:
- opens `rux monitor` in a short pane below the lead (`--no-monitor` to skip it),
- shows the sub-agents' states in Claude Code's status line,
- lets the lead run `rux agent`, `rox ls`, `rox inspect` and `rix status` without asking, and
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

### Sub-agent guests

- GitHub repos are cloned over SSH, with your key from rix's Git agent, so private repos work and agents can push.
- Without a branch, rox uses the repo's default branch.
- New container guests get 4 GB of memory (`--memory` to change it): an agent and a project's installs need more than Apple container's 1 GB default.

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

In a Rex terminal, rux isn't a harness: your own Claude Code or Codex session is the orchestrator, and rux
gives it sub-agents. Install the skill once, then ask your agent to fan work out.

```sh
rux skill install          # into ~/.claude/skills/rux and ~/.codex/skills/rux (--agent claude|codex, --dir DIR)
```

The skill teaches the agent these commands (there's no daemon; state is in `~/.rux/agents/<rex session>/`):

```sh
rux agent spawn --name api-tests [--repo R] [--branch B] [--host H] [--agent claude|codex] "task..."
rux agent ls [--watch]          # states from each pane's Rex program status; --watch is the monitor tab
rux agent wait [ID...] [--any]  # until they stop working; prints the end of each screen
rux agent read ID [-n 120]
rux agent send ID "text" | --key enter|escape|down|1
rux agent focus ID
rux agent stop ID [--keep-guest]
```

- `spawn` splits a pane beside yours (then stacks under the newest agent). It runs `rox up` (on `--host` if
  given), then `rox shell` into the guest, and types in `claude --dangerously-skip-permissions` (or `codex
  --dangerously-bypass-approvals-and-sandbox`) with the task. It answers the CLI's first-run folder-trust
  and permissions dialogs, since the guest is a fresh sandbox.
- Agents clone the current checkout's `origin` by default and push `rux/<id>`; nobody merges but you.
- rux drives panes only through Rex (send, capture, program status) and never parses the agent's output.

## Run it

```sh
rux serve --allow-user you@example.com
tailscale serve --bg 7700       # HTTPS on your tailnet -> 127.0.0.1:7700
```

Open the `https://<machine>.<tailnet>.ts.net` URL on your phone, then Share → Add to Home Screen.

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

Approvals and push notifications, screenshots, SQLite, multiple hosts, a scheduler, and an MCP facade.
For now, tools are pre-allowed (`--allowedTools`) and anything else is denied (`--permission-prompts none`).
