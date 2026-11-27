---
sidebar:
  order: 12
---

# Serve and threads

`rux serve` runs Claude Code threads in guests and serves a web app (a PWA) you drive from your phone over Tailscale.

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

## Run it

```sh
rux serve --allow-user you@example.com
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
