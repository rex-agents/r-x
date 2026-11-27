---
sidebar:
  order: 8
---

# Screenshots and files

In a [`rux serve`](serve.md) thread, Claude can't show you its screen, so it has two commands to show you things.

## Screenshots and files

Claude in a guest is told (via `--append-system-prompt`) that you can't see its screen, and has two
commands for showing you things. Both upload over the runner listener with the thread's token, and
files are stored in `~/.rux/threads/ID/assets/` and shown inline in the thread:

- `rux screenshot URL [--mobile] [--height N] [--caption TEXT]`: headless Chromium (installed with
  `sudo apt-get` on first use), then shared.
- `rux share FILE... [--caption TEXT]`: any file up to 25 MB. Images and videos show inline, and
  anything else is a download.

Images Claude opens with its Read tool also show up in the thread.
