---
sidebar:
  order: 10
---

# Projects and managers

In [`rux serve`](serve.md), threads are grouped into projects, and each project has a manager thread that runs workers for you.

## Projects and managers

Threads are grouped into projects, one per repo; blank threads go in `scratch`. A project has defaults
for new threads (branch, model, effort) and notes that every thread in it gets in its system prompt.

The project page is a conversation with the project's **manager**: a long-lived thread, started by
your first message, with the repo checked out. It runs worker threads through an MCP server that
`rux serve` provides on the runner listener (`/mcp`), scoped to its own project: it can start,
message, read, interrupt and remove workers, view their shared files, and edit the project notes.
When one of its workers finishes a turn or fails, it gets a `[rux] ...` message automatically.
Workers push branches; only you merge.

### Adding a project

There are two ways, both in the [authority TUI](authority-tui.md#new-project) (`p` on the PACK tab):

- **From GitHub**: pick one of your repositories, or type owner/name or a URL. `POST /api/projects {"repo": "acme/web"}`
  makes the project (or returns the existing one). Every thread clones the repo in its own guest.
- **From a local directory** on the authority's machine: `POST /api/projects {"dir": "/Users/you/code/app"}`.
  The directory must be inside your home directory and the root of a git checkout, with at least one commit,
  a branch checked out and a remote. rux never runs `git init` or changes the directory; the error says what
  to do instead. The project's ID and name come from the directory name (made unique with `-2`, `-3`... if
  taken). If the remote is a GitHub repo it's kept as the project's `repo` for display and links; threads
  still use the local copy. The project records the authority that owns the directory (`authority`),
  because the directory only exists on that machine.

A local-directory project's threads (workers, the manager, and sub-agents started with `start_thread`) are
started with `rox up RUNTIME --local`, run inside the directory. That means:

- **Each new thread gets a snapshot of the directory as it is at that moment**: its commits (including
  unpushed ones) and its uncommitted, non-ignored files. The guest first clones the checkout's remote (the
  branch's upstream remote, else `origin`) and rox puts your work on top, so the guest has to be able to reach
  that remote, with the keys rox lends it for the clone. The copy lands in `~/<directory-name>` in the guest
  and the runner works there instead of cloning.
- **Changes in the guest don't flow back** unless the thread pushes them. Later edits on the host don't reach
  threads that already exist either.
- The checkout's own `.rox.json` and `.rox.sh` apply, and the project's branch setting is ignored (`--local`
  copies whatever is checked out).
- Only local runtimes work (`container`, `linux`, `macos`). `fly` and `railway` guests, an existing guest
  (`guest`) and unsandboxed `placement.local` threads are refused with an error.
- Threads for the project always run on the authority that owns the directory. In a pack, a request for the
  project is placed there whatever the leader would choose, and one pinned to another authority is refused.

Adding a local directory, and the directory browser behind the picker (`GET /api/fs/dirs`), is only allowed
from the machine itself: over the authority's unix socket or from loopback. A request that came through
`tailscale serve` (or any proxy that sets `Tailscale-User-Login`, `X-Forwarded-For` or `Forwarded`), or from
another host, gets 403. The web app on your phone can add GitHub projects but not directories.

Managers also have a `thread_stats` tool, and answer "how's it going?" with a [dashboard](rich-ui.md). Project settings is also where you turn [actions](actions.md) on.
