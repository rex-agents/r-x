---
sidebar:
  order: 13
---

# Environment and files

The environment variables rux reads, and where it keeps its files.

## Environment and files

| Variable | Used for |
| --- | --- |
| `REX_SESSION`, `REX_BLOCK` | set by Rex; `rux claude`, `rux agent`, `/pane` and `/shell` need them (`rux agent --session` replaces the first) |
| `RUX_HARNESS_CONFIG` | the harness config's path, instead of `~/.rux/harness.json` |
| `RIX_BIN` | the `rix` binary rux runs for secrets and credentials |
| `RUX_HOME` | the authority's data directory instead of `~/.rux` (pidfile, socket, threads) |
| `NO_COLOR` | plain `rux help` |
| `RUX_UI_URL`, `RUX_ASSET_URL`, `RUX_CRED_URL`, `RUX_CRED_TOKEN` | set in a thread's guest for `rux ui`, `share`, `screenshot`, `action` and `git-credential` |
| `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`, `OPENAI_BASE_URL`, `OPENAI_API_KEY` | set for `claude` in a thread's guest: the authority's [model proxy](credentials.md#the-model-proxy) and the thread token |

| Path | Holds |
| --- | --- |
| `~/.rux/harness.json` | [config](models.md#openai-compatible) |
| `~/.rux/sessions/` | saved harness transcripts |
| `~/.rux/run/` | control sockets |
| `~/.rux/agents/SESSION/` | [`rux agent`](rux-agent.md) state |
| `~/.rux/threads/`, `~/.rux/actions/` | [authority](serve.md) threads (a thread placed on the host keeps its workspace in `threads/ID/work`) and [actions](actions.md) |
| `~/.rux/authority.{pid,url,sock,log,json}` | the [authority daemon](serve.md#the-daemon) |
| `~/.rux/runner/` | the cached guest binary |
