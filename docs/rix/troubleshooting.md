---
sidebar:
  order: 13
---

# Troubleshooting

Messages you may see from rix, their cause and the fix. Add `-v` to any command to see the output of the tools
rix runs.

| Message | Cause and fix |
| --- | --- |
| `Install rox: rix reaches guests only through rox exec` | `rox` not on `PATH`; set `ROX_BIN` |
| `Could not load the Git keys for NAME … run rix git-agent start NAME in a terminal` | a key needs a passphrase; run that command, then `rox reconnect NAME` ([Git agent](git-agent.md)) |
| `No Git SSH key is configured and ~/.ssh has none…` | `rix key add PATH` ([Git keys](git-keys.md)) |
| `X holds a credential for …, so only you may read it` | `chmod 600` the file |
| `Expected a single token with no spaces` / `That doesn't look like a Claude Code token` | paste only the `sk-ant-…` value ([credentials](credentials.md#claude)) |
| `Claude Code has no credential in rix, so it starts signed out` | `rix auth claude`, then `rix apply NAME` |
| `Another sync is running` | wait; if a guest sync died, the next sync recovers a dead lock by itself |
| `~/.rox/sync-lock is a folder` | move that folder away in the guest |
| `GitHub access is off` / `No GitHub token` | `rix auth github` (or `--gh`, then `gh auth login`) ([GitHub access](github-access.md)) |
| `rox speaks guest record schema N and this rix speaks M` | update rox and rix together |
| agent socket path too long | set a shorter `XDG_RUNTIME_DIR` (or `TMPDIR` for tests) |
| `git agent down` in `rix status` | `rix git-agent start NAME`, then `rox reconnect NAME` (rox's `resume` also restarts it) |
