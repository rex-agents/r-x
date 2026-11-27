---
sidebar:
  order: 14
---

# Troubleshooting

Common rux problems and what to do about them.

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

See also [rox](../rox/index.md) and [rix](../rix/index.md) for their own troubleshooting.
