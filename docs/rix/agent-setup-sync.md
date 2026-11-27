---
sidebar:
  order: 3
---

# Agent setup sync

Besides [credentials](credentials.md), rix can copy parts of this machine's own Claude Code and Codex setup into
guests: skills, instructions, remote MCP servers and settings.

```sh
rix sync show                          # what each agent's guests would get now
rix sync set claude skills,instructions
rix sync set codex skills,instructions,mcp,settings
rix sync set claude none               # nothing
rix sync set claude skills --mcp linear,sentry   # only these remote MCP servers
```

The default, with no `sync` setting, is **skills and instructions**. `rix sync set` rewrites all four switches,
so list everything you want. Items:

| Item | Copies | From (this machine) | To (guest `~`) |
| --- | --- | --- | --- |
| `skills` | each skill folder holding a `SKILL.md` (not `.git`; links are followed). A skill named `rox` is skipped | Claude: `~/.claude/skills`; Codex: `~/.codex/skills` and `~/.agents/skills` | `.claude/skills`, `.codex/skills`, `.agents/skills` |
| `instructions` | `CLAUDE.md` / `AGENTS.md` | `~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md` | same paths |
| `mcp` | remote MCP servers only (Claude: `type` `http`/`sse` with a `url`; Codex: a `url` and no `command`) | `mcpServers` in `~/.claude.json`; `mcp_servers` in `~/.codex/config.toml` | `.claude.json`, `.codex/config.toml` |
| `settings` | settings, minus host-bound keys | `~/.claude/settings.json`; `~/.codex/config.toml` | `.claude/settings.json`, `.codex/config.toml` |

`CLAUDE_CONFIG_DIR` and `CODEX_HOME` relocate the source folders. `--mcp NAMES` (a comma list) replaces `mcp`
and copies only those servers; a named server that is missing or runs a local program is skipped with a warning
(install such servers in the guest image).

## What stays behind

Settings that name programs or paths on this machine or hold its secrets are not copied: Claude's `hooks`,
`statusLine`, `apiKeyHelper`, `awsAuthRefresh`, `awsCredentialExport`, `otelHeadersHelper`, `env`,
`enabledPlugins`, `extraKnownMarketplaces`; Codex's `notify`, `projects`, `mcp_servers`, `hooks`, `plugins`,
`marketplaces`, `desktop`. When it adds MCP servers, rix sets `hasCompletedOnboarding` in the guest's
`~/.claude.json` if it isn't set.

In the [config](configuration.md), `agents.AGENT.sync` is an object: `{"skills": true, "instructions": true,
"settings": false, "mcp": false}`, where `mcp` is `true` (all remote servers), `false`, or a list of names.

## How a sync lands in the guest

rix builds one tar archive and pipes it to `rix-guest.sh` through `rox exec`. It is merged, not overwritten:
keys and MCP servers rix put in `~/.claude.json` and `~/.codex/config.toml` last time are removed first and the
current ones added, so settings you made in the guest stay. A record, `~/.rox/synced.json` in the guest, lists
what rix synced, so what you stopped syncing is removed next time. On the guest:

- One sync at a time: `~/.rox/sync-lock` is a symlink holding `PID:START` of its owner. A lock is taken over only
  if its owner is gone, never for its age. A second sync fails with "Another sync is running". If
  `~/.rox/sync-lock` is a folder, rix refuses and leaves it. If a stuck `~/.rox/sync-reap` blocks things, remove it.
- The archive is unpacked into `~/.rox/sync-work/stage` and checked (paths must be relative, inside
  `.claude`, `.codex` or `.agents`, without `..`; the archive must end with its completeness marker) before
  anything changes.
- Each path is then swapped in, with the old one kept in `~/.rox/sync-work/undo`, and a journal written first.
  Any failure, or a later sync after a crash, restores the originals. If it can't, the backups stay in
  `~/.rox/sync-work` and the error says so; the next sync tries again.
- Stale paths from the previous sync are removed only after the new setup is in place.

Syncs run on launch (see [the rox contract](rox-contract.md)) and from [`rix apply`](pushing-to-guests.md).
