`esc` | leave the conversation (interrupt it first, if it is working) or a dialog |---
sidebar:
  order: 2
---

# The authority TUI

Bare `rux` opens the authority TUI: a node graph of your pack, **leader → project agents → threads**, with a preview of whatever is selected, and a monitor of who is connected and what is running where. It starts the local [authority](authority.md) if it isn't running, then attaches to it (the pack leader when you're in a pack). The web app has the same view at **pack**, from the same data.

For the single-window lead-and-sub-agents harness, use [`rux agent`](lead-and-sub-agents.md).

## The window

```
rux  PACK  MONITOR                                         ● live  leader mac  term 3  ● vault ok  up 2h00m  http://127.0.0.1:7700
LEADER                                       PROJECT AGENTS              THREADS                   ╭ preview ───────────────╮
╭────────────────────────────────────────╮   ╭───────────────────────╮   ╭───────────────────────╮ │ ● fix the login bug    │
│ ● mac                                  │   │ ● api                 │   │ ● fix the login bug   │ │ status running         │
│ term 3 · vault ok                      │─┐ │ no manager yet        │ ┌─│ running               │ │ on     mac · vm        │
│ 2/2 authorities up                     │ │ │ enter starts one      │ │ │ mac · g-t1            │ │ stats  12s · 3 turns…  │
│ 3/7 threads working           ▁▂▄▃▆▂▁▅ │ │ ╰───────────────────────╯ │ │ 2 sub-agents ▁▂▄▃▆▂▁▅ │ │ FILES / RECENT         │
╰────────────────────────────────────────╯ │ ╭───────────────────────╮ │ ╰───────────────────────╯ ╰────────────────────────╯
<T-rex>                                    └─│ ● web app             │─┘ ╭───────────────────────╮
                                             │ manager running       │   │ ● hand started        │
                                             │ 2/3 workers working   │   │ idle                  │
                                             │ $2.68 · 4 threads     │   │ mac · g-t6            │
                                             ╰───────────────────────╯   ╰───────────────────────╯
pack › mac › web app › fix the login bug     ←↑↓→ move  enter open  n new thread  tab monitor  a archived  ? help  q quit
```

The columns:

- **LEADER**: one card for the quorum leader: its name, term and vault status, authorities up out of total, and threads working out of total across the pack. The other authorities are in the monitor. The T-rex sits under it.
- **PROJECT AGENTS**: one card per project, showing its **manager**, the agent that runs all of the project's work: its status, how many of its workers are working out of the total, and the project's cost. A project that has threads but no manager yet says *no manager yet*; `enter` on it (or just typing) opens an empty conversation with the project's manager (typing seeds the input); the first message you send starts it (`POST /api/projects/{id}/manager`). Threads with no project go under a **scratch** card, which never has a manager. `enter` on a card with a manager opens the manager's conversation.
- **THREADS**: the selected project's threads, newest first: the manager's workers (threads whose `parent` is the manager) and the threads you started by hand in that project. Each card has its status, authority and guest, and a count of its own sub-agents. `→` steps into a **SUB-AGENTS** column (shown only then) and `enter` there opens a sub-agent's conversation, read-only. Archived threads stay hidden until you press `a`.

- **Cards** show a status dot, the name, three short lines and, on most, a small activity sparkline (events in the last ~40 seconds). The selected card has a bright border, and a line connects the selected path from the leader to its threads.
- **Preview** (far right) describes the selected node: for the leader the term, vault host and each authority's status; for a project its repo, cost, workers and its manager's conversation; for a thread its placement, guest, `duration · turns · cost`, shared files and the last few events; for a sub-agent its status and guest. `enter` on a sub-agent opens its conversation, read-only.
- **Header**: the tabs, then a live/degraded/offline dot, the pack leader, its term, whether the vault is reachable, how long the authority has been up and the URL. A yellow banner says *degraded* when the authority can't reach a leader and is showing only its own view; a red one says *vault unreachable* when the vault host is down (cached keys still work until their TTL; new credentialed work fails).
- **Small terminals** collapse columns: the columns shown always include the focused one, the preview drops below 90 columns, and below ~60 columns one column is shown at a time. The T-rex appears when there's room.

## Keys

| Key | Does |
| --- | --- |
| `←` `→` `h` `l` | move between columns |
| `↑` `↓` `j` `k` | move within a column (the columns to the right follow) |
| `enter` | on a thread or a project's manager: open its conversation; on a project with no manager: open its empty conversation, where `enter` starts one; elsewhere: step into the next column |
| `n` | new thread in the selected project |
| `p` | new project, from GitHub or a local directory |
| `tab` | PACK → MONITOR → SETTINGS (`1` `2` `3` jump to each) |
| `a` | show or hide archived threads |
| `?` | help |
| `esc` | leave the conversation or a dialog |
| `q`, `ctrl+c` | quit (`ctrl+c` anywhere; `q` outside text fields) |

### Conversation

`enter` on a thread opens it. It looks and works like a thread in `rux agent`, with the same renderer: markdown text, tool calls with their results under `└`, a spinner and a "working… esc to interrupt" line, messages from other agents as `◂ name`, and errors in red. Type in the box at the bottom and press `enter` to send a message (`POST /api/threads/{id}/messages`). Click a tool call (or press `ctrl+e` for all of them) to open it. `↑`/`↓`/`pgup`/`pgdown`/`home`/`end` and the mouse wheel scroll. The header shows the path as `PACK › project › thread` (click `PACK` to go back). `esc` interrupts a working thread (`POST /api/threads/{id}/interrupt`) and, otherwise, returns to the graph; `tab` goes to the monitor. A sub-agent's conversation is read-only.

A project with no manager opens the same view, titled *manager · project · not started*, with a hint and the box "Tell the manager what to do". `enter` sends the first message: it shows as your message with the starting spinner, and when the API returns the manager's thread the view switches to it and its events stream in. If starting fails, the text goes back into the box and the error shows in the transcript. `esc` returns to the graph.

### New thread

`n` opens a small form for the selected project: the **prompt**, **placement** (a VM by default, or `local` for the host itself; `ctrl+l` toggles) and **authority** (`auto`, where the pack leader places it, or a specific authority). `enter` creates it with `POST /api/threads`:

```json
{"project": "web", "prompt": "write docs", "placement": {"authority": "nuc", "local": true}}
```

and opens the new thread.

### New project

`p` opens a dialog with two tabs (`tab` switches). `esc` cancels, and errors show inside the dialog.

- **GitHub**: your repositories (`GET /api/repos`), filtered as you type. A name you type that looks like a
  repo (`owner/name`, a URL, `git@github.com:owner/name`) gets a "use as typed" row on top. `enter` adds the
  highlighted row.
- **Local directory**: a browser for the authority's machine, starting at your home directory
  (`GET /api/fs/dirs`; only folders inside home). Git checkouts are marked `git`. `enter` or `→` opens the
  highlighted folder, `←` or backspace (with an empty filter) goes up, and typing filters (start with `.` to
  see dot-folders). `enter` on the first row, "this directory", adds the folder you're in, and `ctrl+a` adds
  the highlighted one without opening it. To enter a path by hand, type it starting with `/` or `~` and press
  `enter` on the first row. A folder that isn't a usable checkout comes back with the server's reason in the
  dialog, for example *not a git repository; run git init and commit first*.

Both end in `POST /api/projects` (`{"repo": ...}` or `{"dir": ...}`; see
[Adding a project](projects-and-managers.md#adding-a-project)), and the new project is selected in the
PROJECT AGENTS column. A directory project's card and preview show its path, with `~` for home. Browsing and
adding directories only work from the machine itself, so the TUI there talks to the authority's unix socket.

### Monitor

MONITOR has three columns in the same card style, from the same feed. `←` `→` move between them, `↑` `↓` within one. `enter` opens a shell on the selected guest or host (nothing on a client), and `o` opens a guest's thread.

- **CLIENTS**: every UI connected to the pack right now (`clients` in the pack view): TUI or web, the device, the authority it connected through, when it connected and when it was last seen. Selecting one selects the host it came through.
- **HOSTS**: every authority: up, down or joining, a ★ for the leader, OS, CPUs, load, guest slots used and maximum, working threads, uptime.
- **GUESTS**: the rox VMs (`guests`) on the selected host, or on every host while CLIENTS is focused: phase, backend, CPU %, memory used and limit, and the thread using it.

**Shell.** On a guest, `enter` runs `rox shell GUEST` (with `-S HOST` for a guest on another authority; rux's guests have no project checkout, so no directory is added). On the host this machine runs, it opens your `$SHELL`; on another host of the pack it runs `ssh <the authority URL's hostname>`, or says there's no address when the URL is only loopback. Inside Rex (`REX_SESSION` and `REX_BLOCK` set) the shell opens in a new pane to the right of the TUI with `rex split`, so the TUI stays live; otherwise the TUI suspends until the shell exits.

With a guest selected, `s` sleeps it and `d` downs it (after a `down NAME (thread ID)? y/n` prompt in the footer). *Sleep* stops the guest and keeps its disk, like `rox stop` and the server's idle sleep; on a stopped guest `s` wakes it (`rox start`). *Down* removes the guest, like `rox down`. A guest that a thread uses goes through the thread's own sleep, wake and destroy path, so the thread's status stays right; a spare guest with no thread goes straight to rox. Downing a guest whose thread is mid-turn says so in the prompt. The web app's monitor has the same buttons on each guest card.

The TUI identifies itself to the authority with `X-Rux-Client: tui` and `X-Rux-Device: <host name>` on its pack stream, which is how it shows up as a client.

### Settings

The **SETTINGS** tab renames the authority, draws a new mascot (or goes back to the T-rex) and picks
the theme: `↑` `↓` to move, `enter` to change. See [settings and themes](theming.md).

## One API, two clients

The TUI keeps no state of its own and reads nothing from `~/.rux`: it is only a client of the authority's HTTP API, the same one the web app uses.

| What | API |
| --- | --- |
| The pack, and live updates | `GET /api/pack`, SSE `GET /api/pack/stream` (`pack`, `pack.delta`, `pack.clients`, `thread.status`, `thread.event`) |
| Project names | `GET /api/projects` |
| Sleep, wake or remove a guest | `POST /api/guests/{name}/sleep`, `/wake`, `/down` (`?authority=NAME` for a guest on another member of the pack, which forwards it; `?force=1` to down a guest whose thread is mid-turn, else 409 `busy`) |
| A thread's conversation | `GET /api/threads/{id}/events/page` (a sub-agent's, read-only: `/api/threads/{id}/subagents/{sub}/events/page`) |
| Send a message | `POST /api/threads/{id}/messages` |
| New thread, with `placement` | `POST /api/threads` |
| Start a project's manager | `POST /api/projects/{id}/manager` |
| New project | `POST /api/projects` (`{"repo"}` or `{"dir"}`), picked with `GET /api/repos` and `GET /api/fs/dirs?path=&hidden=1` |

The web app's **pack** tab (`#/pack`) has three columns (stacked on a phone), from the same feed: **LEADER** (name, term, vault, authorities up/total, threads working/total), **PROJECT AGENTS** (one card per project with its manager thread, `kind: "manager"`: status, workers working/total, cost; tapping it opens the manager's conversation; a project with threads but no manager says so and opens the manager composer; threads with no project are "scratch"), and **THREADS** (the selected project's workers and hand-started threads, newest first, each with status, authority, guest and a sub-agent count that opens the sub-agent's conversation; archived ones behind the *archived* toggle). The **monitor** tab (`#/monitor`) has **CLIENTS** (`pack.clients`: kind, device, authority, last seen), **HOSTS** (status, leader mark, OS, CPUs, load, guest slots, uptime; tap one to filter the guests) and **GUESTS** (`authority.guests`: phase, backend, CPU %, memory, and the thread using it, which opens when tapped). It no longer reads `/api/overview`. `kind`, `guests` and `clients` are optional (an older authority omits them), and `pack.clients` carries the full client list. Open `/?fixture` to render `web/fixture.json` with no authority. Anything the TUI can do is a call the web app can make, and the wire types live in `internal/rux/authapi`.

## Environment

| Variable | Does |
| --- | --- |
| `RUX_AUTHORITY_URL` | attach to an already running authority at this URL instead of starting the local one |
