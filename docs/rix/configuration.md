---
sidebar:
  order: 9
---

# Configuration

rix keeps its settings in one JSON file, with secrets in separate `0600` files beside it. `rix config` opens the
config in `VISUAL`, `EDITOR` or `vi` (creating `{}` if absent) and checks that it is valid JSON afterwards.

## Files

| Path | Holds |
| --- | --- |
| `$XDG_CONFIG_HOME/rix/config.json` (default `~/.config/rix/`) | settings; the folder is mode 0700 |
| same folder: `claude-token`, `anthropic-api-key`, `openai-api-key`, `github-token` | secrets saved by `rix auth` (0600) |
| same folder: `secrets.json` | [named secrets](named-secrets.md) (0600) |
| same folder: `*.lock` | lock files (`config.json.lock`, `secrets.json.lock`) taken while a file is rewritten; harmless to leave |
| `$XDG_STATE_HOME/rix/GUEST/state.json` (default `~/.local/state/rix/`) | per guest: keys held, whether to keep them, agent PID and start, agents signed in, last sync time and summary |
| `$XDG_RUNTIME_DIR/rix/ID/agent.sock` or `/tmp/rix-UID/ID/agent.sock` | a guest's [Git agent](git-agent.md) socket |
| in the guest: `~/.rox/agents.env`, `~/.codex/auth.json`, `~/.rox/rix-codex-login`, `~/.rox/synced.json`, `~/.rox/sync-lock`, `~/.rox/sync-work/` | see [pushing](pushing-to-guests.md) and [agent setup sync](agent-setup-sync.md) |

## Config keys

Unknown keys are preserved when rix rewrites the file.

```json
{
  "git_keys": ["~/.ssh/id_ed25519"],
  "git_known_hosts": "~/.ssh/pinned_known_hosts",
  "git_agent": true,
  "agents": {
    "claude": { "token_file": "~/.config/rix/claude-token",
                "sync": { "skills": true, "instructions": true, "settings": false, "mcp": ["linear"] } },
    "codex":  { "auth_file": "~/.codex/auth.json" }
  },
  "github": { "token_file": "~/.config/rix/github-token" },
  "updates": "auto"
}
```

- `git_keys`, `git_known_hosts`: see [Git keys](git-keys.md). `git_agent`: see [the Git agent](git-agent.md).
- `agents.claude` takes one of `token_file` or `api_key_file`; `agents.codex` one of `auth_file` or
  `api_key_file`; both also `sync` and `version`. `version` (such as `1.2.3`) is the release rox installs when a project asks for the agent without pinning one; a version set in `.rox.json`, `.rox.local.json` or `--agents` wins, and unset means the latest release. Anything else, or two credentials
  for one agent, is an error. Credential files are read at push time, so rotating a token needs only `rix apply`.
- `github` is `{"token_file": PATH}` or `{"off": true}`; absent means gh's login.
- `updates`: `auto`, `notify` or `off`; the `RIX_UPDATES` environment variable overrides it
  (see [updates](updates-and-migration.md#updates)).

## Environment variables

| Variable | Effect |
| --- | --- |
| `ROX_BIN` | the `rox` binary rix uses (else `rox` on `PATH`) |
| `RIX_VERBOSE=1` | same as `-v`: show the output of the tools rix runs |
| `RIX_UPDATES` | `auto`, `notify` or `off`; overrides `updates` |
| `XDG_CONFIG_HOME`, `XDG_STATE_HOME`, `XDG_RUNTIME_DIR` | move the config, state and socket folders |
| `VISUAL`, `EDITOR` | editor for `rix config` |
| `CLAUDE_CONFIG_DIR`, `CODEX_HOME` | relocate the source folders for [sync](agent-setup-sync.md) and the Codex login file |
