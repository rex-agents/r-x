---
sidebar:
  order: 12
---

# The authority, serve and threads

The **authority** (`rux authority`, which grew out of `rux serve`) is the rux daemon on a host. It runs Claude Code threads in guests, serves a web app (a PWA) you drive from your phone over Tailscale, and serves the [model proxy](credentials.md#the-model-proxy) guests use instead of model keys. See the [design contract](authority.md) for how authorities form a pack.

## How threads work

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
- A thread's guest is stopped when it has been idle (`--sleep-after`) and started again by the next message.

Threads are grouped in [projects with managers](projects-and-managers.md). In a thread, agents can [share screenshots and files](screenshots-and-files.md), show [rich UI](rich-ui.md) and trigger [actions](actions.md). GitHub access is described under [credentials](credentials.md#github-access).

## The daemon

| Command | Does |
| --- | --- |
| `rux authority start [--foreground] [FLAGS]` | start the daemon, detached (pidfile, log and socket in `~/.rux/`), and wait until it answers. `--foreground` runs it in this terminal. The flags are the ones below. |
| `rux authority stop` | stop it |
| `rux authority status` | pid, URL, and the pack summary (exit status 1 when it isn't running) |
| `rux authority logs [-f]` | the last 200 lines of the log, and follow it with `-f` |
| `rux serve [FLAGS]` | a hidden alias of `rux authority start --foreground` |

| File | Holds |
| --- | --- |
| `~/.rux/authority.pid`, `authority.url` | the running daemon's pid and HTTP base URL |
| `~/.rux/authority.sock` | the same HTTP API on a unix socket (mode 0600, trusted like loopback, so `--allow-user` doesn't apply) |
| `~/.rux/authority.log` | output of a detached daemon |
| `~/.rux/authority.json` | optional: `{"name": "mac-studio", "vault": "local"}`. The name defaults to the host name; `vault` is the default [vault source](credentials.md#the-model-proxy) |

Go code (the TUI) calls `rux.EnsureAuthority()` to start the daemon if needed and get its base URL, or
`rux.AuthorityClient()` for an HTTP client that dials the socket.

### The pack API

`GET /api/pack` returns `authapi.Pack` (leader, term, vault host, and each authority with its host's
OS, CPUs, memory, load and guest slots, plus its threads). `GET /api/pack/stream` is the live feed
(server-sent events: a `pack` snapshot, then `pack.delta`, `thread.event` and `thread.status`; each
event's data is `{"type", "data"}`). Outside a quorum this is a pack of one: the leader is the
authority itself, term 0. `started` on each authority is its start time (for uptime), and an archived
thread has status `archived` (and `archived: true`). A thread's sub-agents are openable at
`/api/threads/{thread}/subagents/{id}/events` (SSE) and `/events/page`; both are answered only for a
sub-agent the thread started. A leader proxies all `/api/threads/{id}/...` requests for another
authority's threads to that authority. `GET /api/threads` also returns `authority`, `placement`, `subagents`
(a manager's workers) and `cost` for each thread; `?authority=NAME` or `?authority=all` lists another
authority's or the whole pack's threads. The Go types are in `internal/rux/authapi`.

### Placement

`POST /api/threads` takes two more fields:

```json
{"prompt": "...", "placement": {"local": true, "authority": "mac-studio"}, "vault": "local"}
```

By default a thread runs in its own rox guest. `"local": true` runs its runner as a process on the host
instead (unsandboxed, in `~/.rux/threads/ID/work`). `authority` pins to an authority; until the host is
in a quorum only its own name is accepted. `vault` (`local` or `leader`) picks where the thread's model
keys come from.

## Run it

```sh
rux authority start --allow-user you@example.com   # or: rux serve --allow-user ... in the foreground
tailscale serve --bg 7700       # HTTPS on your tailnet -> 127.0.0.1:7700
```

Open the `https://<machine>.<tailnet>.ts.net` URL on your phone, then Share → Add to Home Screen.

`rux serve` flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--listen` | `127.0.0.1:7700` | the web app's address |
| `--allow-user LOGIN` | | only this Tailscale login through `tailscale serve` (recommended) |
| `--runner-listen` | `0.0.0.0:7701` | where guests' runners connect |
| `--advertise HOST:PORT` | Apple container's gateway, runner port | how guests reach the runner listener |
| `--guest-cidrs` | the container network's subnet | networks allowed to reach the runner listener (loopback always allowed) |
| `--data DIR` | `~/.rux` | data directory |
| `--backend rox\|local` | `rox` | see [testing without Rox](#testing-without-rox) |
| `--rox BIN` | `rox` | the rox binary |
| `--claude BIN` | `claude` | only to list the models Claude Code offers |
| `--memory` | `4G` | guest memory |
| `--sleep-after DURATION` | `15m` | stop an idle thread's guest, and start it again on the next message; `0` keeps guests running |
| `--runner-bin PATH` | | the Linux `rux` served to guests |
| `--github` | on | GitHub access for guests and the repo picker |
| `--model-proxy` | on | serve `/proxy/anthropic` and `/proxy/openai` on the runner listener, and give guests those instead of model keys |
| `--vault local\|leader` | `authority.json`, else `local` | the default vault source for model keys |

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

## In a pack

The authority always runs a quorum node (`internal/rux/quorum`; see [quorum](quorum.md)) and listens for
other authorities on `--quorum-listen` (default `0.0.0.0:7710`, advertised as `--quorum-url`, default
the tailnet address). Without `~/.rux/quorum.json` it's a pack of one. `rux quorum create` and
`rux quorum join` bring it into a pack through the admin socket without a restart. In a pack:

- `/api/pack` and `/api/pack/stream` are the quorum's view: the leader's, or a member's copy of it, or,
  when there's no leader, this authority alone with `degraded: true`. Each authority pushes its host and
  thread summaries to the leader every 2s. The stream carries this authority's own `thread.event` and
  `thread.status` events too, and the status changes of other authorities' threads (their conversation events stay
  on their authority: open the thread, which is proxied).
- `POST /api/threads` without `placement.local` asks the leader where to run (pinned by
  `placement.authority`, else the authority with the most free guest slots). If another authority is
  chosen, the create is forwarded to it with the leader's term, which it checks (fencing), and its
  thread is returned.
- Every `/api/threads/{id}/...` request for a thread on another authority (events, events/page,
  messages, interrupt, sub-agent paths, archive, destroy, ...) is proxied over the quorum link
  (`/quorum/proxy/`, member credentials) to the owning authority, so any authority's API serves the whole pack.
- Model keys come from the `leader` vault source (the vault host, over the quorum link, with the policy
  and audit of `rux quorum policy`) unless the thread, `--vault` or `authority.json` says `local`. The
  audit names every thread that uses a key: a cached use is reported to the vault host once per thread per 10 minutes.

Sub-agents a thread starts through `/mcp` are placed like any thread: anywhere in the pack unless
`start_thread` is given an `authority`. The child records its parent and the parent's authority
(`parent`, `parent_authority` in `ThreadSummary`, and `depth`). A parent's `subagents` include children on
other authorities; `read_thread`, `send_message`, `interrupt_thread` and `remove_thread` reach them over
the quorum link (`/quorum/mcp`), their wake-ups go back to the parent's authority (`/quorum/wake`), and
`/api/threads/{parent}/subagents/{id}/events[/page]` is proxied to the child's authority.
`thread_stats` only covers local sub-agents.

## Threads are project agents

Every thread can start sub-agents, whatever its engine. The authority's guest `/mcp` endpoint accepts
any live thread's token and serves the same tools to all of them (`start_thread`, `list_threads`,
`send_message`, `read_thread`, `view_file`, `interrupt_thread`, `remove_thread`, `thread_stats`,
`get_project`, `update_notes`). A [project's manager](projects-and-managers.md) manages every worker in
its project; any other thread manages the sub-agents it started. Sub-agents are threads too (own guest,
repo checked out, `parent` set); they show up in the parent's `subagents` in `/api/pack`, the parent is
woken when one finishes a turn, and they can nest three deep. A sub-agent inherits its parent's engine,
model and vault.

A thread's **engine** is chosen with `"engine"` on `POST /api/threads`:

| Engine | Runs in the guest | Model |
| --- | --- | --- |
| `claude-code` (default) | `rux runner` → `claude -p`, with the authority's MCP server in its config | Claude Code's: aliases such as `opus`, subscription or API key through the proxy |
| `harness` | `rux runner` → `rux headless`, rux's own agent loop (`internal/harness`) without a UI | `anthropic/MODEL` or `openai/MODEL`, through the [model proxy](credentials.md#the-model-proxy) |

`rux headless` speaks claude's stream-json on stdin and stdout, so the runner, the thread store and the
app need no changes. It has the harness's coding tools in the thread's directory plus the authority's MCP
tools. It's what `rux agent --headless` should run too (the command is hidden for now).
Limits: a restarted harness agent replays only the text of its last 20 turns, not its tool history;
cost isn't tracked (`cost` stays 0); a model change on a running harness thread takes effect only on
restart; other providers (OpenRouter, Ollama, …) aren't served by the proxy yet.

Thread and sub-agent guests are launched with `rox up --credentials=false`, so rix pushes no Claude Code
or Codex sign-in into them (Git agent and setup sync still apply); their models go through the proxy.

## Not yet

Pinning to other authorities, the pack-wide view and `vault: leader` need the quorum.
For `rux serve` threads: push notifications, SQLite storage and a scheduler. Tools are pre-allowed
(`--allowedTools`) and anything else is denied (`--permission-prompts none`), so a thread has no approval prompts.
