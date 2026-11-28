`esc` | leave the conversation (interrupt it first, if it is working) or a dialog |---
sidebar:
  order: 2
---

# The authority TUI

Bare `rux` opens the authority TUI: a node graph of your pack, **hosts → projects → threads → sub-agents**, with a preview of whatever is selected. It starts the local [authority](authority.md) if it isn't running, then attaches to it (the pack leader when you're in a pack). The web app has the same view at **pack**, from the same data.

For the single-window lead-and-sub-agents harness, use [`rux agent`](lead-and-sub-agents.md).

## The window

```
rux  PACK  THREAD  MONITOR                     ● live  leader mac  term 3  ● vault ok  up 4m  http://127.0.0.1:7700
<T-rex>                                    PROJECTS            THREADS             SUB-AGENTS         ╭ preview ─────────╮
HOSTS                                      ╭────────────────╮  ╭────────────────╮  ╭───────────────╮ │ ● fix the login… │
╭───────────────────────────╮              │ ● web app      │  │ ● fix the login│  │ ● tests       │ │ status  running  │
│ ● mac                     │──────────────│ acme/web       │──│ running · vm   │──│ g-s1          │ │ on      mac · vm │
│ darwin · 10 cpu · leader  │              │ 2 threads ▁▃▅▂ │  │ 2 sub-agents ▂▅│  │ running       │ │ stats   12s · 3… │
╰───────────────────────────╯              ╰────────────────╯  ╰────────────────╯  ╰───────────────╯ │ FILES / RECENT   │
pack › mac › web app › fix the login bug     ←↑↓→ move  enter open  n new thread  tab monitor  a archived  ? help  q quit
```

- **Cards** show a status dot, the name, a one-line summary, a count ("3 threads", "2 sub-agents") and a small activity sparkline (events in the last ~40 seconds). The selected card has a bright border, and a line connects the selected path from the root to its threads.
- **Preview** (far right) describes the selected node: for a host its OS, load, VMs and role; for a project its repo, thread counts and cost; for a thread its placement, guest, `duration · turns · cost`, shared files and the last few events; for a sub-agent its status and guest. `enter` on a sub-agent opens its conversation, read-only.
- **Header**: the tabs, then a live/degraded/offline dot, the pack leader, its term, whether the vault is reachable, how long the authority has been up and the URL. A yellow banner says *degraded* when the authority can't reach a leader and is showing only its own view; a red one says *vault unreachable* when the vault host is down (cached keys still work until their TTL; new credentialed work fails).
- **Small terminals** collapse columns: the columns shown always include the focused one, the preview drops below 90 columns, and below ~60 columns one column is shown at a time. The T-rex appears when there's room.

## Keys

| Key | Does |
| --- | --- |
| `←` `→` `h` `l` | move between columns |
| `↑` `↓` `j` `k` | move within a column (the columns to the right follow) |
| `enter` | on a thread: open its conversation; elsewhere: step into the next column |
| `n` | new thread in the selected project |
| `tab` | PACK ↔ MONITOR (`1` `2` `3` jump to PACK, THREAD, MONITOR) |
| `a` | show or hide archived threads |
| `?` | help |
| `esc` | leave the conversation or a dialog |
| `q`, `ctrl+c` | quit (`ctrl+c` anywhere; `q` outside text fields) |

### Conversation (THREAD)

`enter` on a thread opens it. It looks and works like a thread in `rux agent`, with the same renderer: markdown text, tool calls with their results under `└`, a spinner and a "working… esc to interrupt" line, messages from other agents as `◂ name`, and errors in red. Type in the box at the bottom and press `enter` to send a message (`POST /api/threads/{id}/messages`). Click a tool call (or press `ctrl+e` for all of them) to open it. `↑`/`↓`/`pgup`/`pgdown`/`home`/`end` and the mouse wheel scroll. `esc` interrupts a working thread (`POST /api/threads/{id}/interrupt`) and, otherwise, returns to the graph; `tab` goes to the monitor. A sub-agent's conversation is read-only.

### New thread

`n` opens a small form for the selected project: the **prompt**, **placement** (a VM by default, or `local` for the host itself; `ctrl+l` toggles) and **authority** (`auto`, where the pack leader places it, or a specific authority). `enter` creates it with `POST /api/threads`:

```json
{"project": "web", "prompt": "write docs", "placement": {"authority": "nuc", "local": true}}
```

and opens the new thread.

### Monitor

MONITOR is a table of the pack's authorities (status, OS, CPUs, load, VMs, working threads) and the running threads, from the same feed.

## One API, two clients

The TUI keeps no state of its own and reads nothing from `~/.rux`: it is only a client of the authority's HTTP API, the same one the web app uses.

| What | API |
| --- | --- |
| The pack, and live updates | `GET /api/pack`, SSE `GET /api/pack/stream` (`pack`, `pack.delta`, `thread.status`, `thread.event`) |
| Project names | `GET /api/projects` |
| A thread's conversation | `GET /api/threads/{id}/events/page` (a sub-agent's, read-only: `/api/threads/{id}/subagents/{sub}/events/page`) |
| Send a message | `POST /api/threads/{id}/messages` |
| New thread, with `placement` | `POST /api/threads` |

The web app's **pack** tab (`#/pack`) shows the same hierarchy from the same feed, with the same statuses, the degraded and vault banners, the archived toggle, and a thread's activity sparkline. Its new-thread form has the same **run in** (VM or this host) and **authority** choices. On a phone it shows one column at a time, with an *up* button. Anything the TUI can do is a call the web app can make, and the wire types live in `internal/rux/authapi`.

## Environment

| Variable | Does |
| --- | --- |
| `RUX_AUTHORITY_URL` | attach to an already running authority at this URL instead of starting the local one |
