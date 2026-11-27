---
sidebar:
  order: 9
---

# Agents

rox can install agent CLIs (Claude Code and Codex) in a guest. It installs them only when a project
asks.

- `agents` in `.rox.json` or `.rox.local.json` ([Project settings](project-settings.md)), or `--agents`
  for one launch (`claude`, `codex`, `claude=1.2.3,codex`). `--no-agents` installs none. Later
  sources win: `.rox.json`, `.rox.local.json`, then the flag.
- `true`/no version means the latest release, looked up at most every six hours (cached in
  `~/.local/state/rox/agent-versions.json`); a failed check warns and uses the cached version. A
  version (`1.2.3`, `1.2.3-beta.1`) pins one.
- On container guests, agents are baked into a last image layer (`rox:NAME-agents`), so a new release
  doesn't rerun `.rox.sh`; if that build fails, they're installed at boot. rox records the installed
  versions in the guest record (`rox inspect`).
- Signing the agents in is [rix](../rix/index.md)'s job (`rix auth claude|codex`); rox calls the
  `post-boot` hook with the agents that are new to the guest. See
  [The rix contract](rix-contract.md#the-rix-contract).
