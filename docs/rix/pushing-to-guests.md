---
sidebar:
  order: 6
---

# Pushing to running guests

New guests get credentials and agent setup from rox automatically. `rix apply` sends changes in the vault, such
as a rotated token, to guests that are already running.

```sh
rix apply rv-web-3f2a9c1b7d4e
rix apply --all                   # every running guest that has an agent CLI
```

`apply` reads the guest with `rox inspect`, pushes [credentials](credentials.md) for the agents it has, then
[syncs agent setup](agent-setup-sync.md), and prints what it did and any warnings. Name a guest or pass `--all`,
not both. With `--all`, a failing guest is reported and the others still run; the command then exits non-zero.
A guest with no agent CLIs is skipped. New shells in the guest get the credentials immediately; agents started
by `rex run` get them after `rox stop NAME` and `rox start NAME`.

## What a credential push does

| Action | Effect |
| --- | --- |
| env | rewrites `~/.rox/agents.env` (mode 0600) with `export CLAUDE_CODE_OAUTH_TOKEN=…` or `ANTHROPIC_API_KEY=…` for installed agents; shells and the guest's Rex server read it |
| codex-auth | writes `~/.codex/auth.json` (0600) from your login file |
| codex-login | runs `codex login --with-api-key` |
| codex-forget | removes Codex's login, only if rix signed it in (`~/.rox/rix-codex-login` marker, or rix's record); a login you made in the guest stays |

If the vault has no credential for an installed agent, rix warns that it starts signed out.
