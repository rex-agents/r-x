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

Managers also have a `thread_stats` tool, and answer "how's it going?" with a [dashboard](rich-ui.md). Project settings is also where you turn [actions](actions.md) on.
