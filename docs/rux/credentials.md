---
sidebar:
  order: 7
---

# Credentials

rux stores no credentials. Everything it needs comes from [rix](../rix/index.md), and guests only get what they need for their own work.

## Credentials come from rix

rux stores no credentials. Everything it needs comes from rix, on the machine running rux (for `rux serve`, the Mac):

| What | From | Set it with |
| --- | --- | --- |
| Claude Code or Codex in thread guests, Claude Code or Codex in `rux agent` guests | rox's hooks into rix, because rux starts guests with `rox up --agents claude` (or `codex`) | `rix auth claude`, `rix auth codex` |
| GitHub, for the repo picker and for git in guests | `rix credential github` | `rix auth github` (a token), or nothing to use `gh`'s login |
| Secrets that [actions](actions.md) list | `rix secret get` | `rix secret set NAME` |
| Model API keys for the [harness](models.md) | rix secrets named for the provider | `rix secret set ANTHROPIC_API_KEY`, … |

rux's own per-thread tokens only let a guest's runner talk to its own thread.

## The model proxy

Thread guests never hold model keys. The [authority](serve.md) serves a reverse proxy on the guest
listener, and the thread's `claude` runs with:

| Variable | Value |
| --- | --- |
| `ANTHROPIC_BASE_URL` | `http://AUTHORITY:7701/proxy/anthropic` |
| `ANTHROPIC_AUTH_TOKEN` | the thread token |
| `OPENAI_BASE_URL` | `http://AUTHORITY:7701/proxy/openai/v1` |
| `OPENAI_API_KEY` | the thread token |

The runner removes `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN` and the like from the environment of
the processes it starts. For each request the proxy checks that the token belongs to a live thread
(401 otherwise), takes the real key from the thread's **vault source**, replaces the client's
credentials and forwards to `api.anthropic.com` or `api.openai.com`, streaming responses. A Claude Code
subscription token (`sk-ant-oat…`) is sent as `Authorization: Bearer` with `anthropic-beta: oauth-2025-04-20`;
an API key as `x-api-key`. Cookies are dropped, and the real key is never sent to the guest.

The `local` vault source reads this host's rix, first match wins: `rix secret get` of
`ANTHROPIC_API_KEY` or `CLAUDE_CODE_OAUTH_TOKEN` (`OPENAI_API_KEY` for OpenAI), then the credential files
`rix auth claude` / `rix auth codex` saved, then the authority's own environment. Keys are held in
memory for 10 minutes and never written to disk. A `leader` vault source (the vault host's authority
over the quorum link) plugs in through the `VaultSource` interface; until then a thread with
`"vault": "leader"` gets a 502 explaining that the source isn't available. Turn the proxy off with
`--model-proxy=false`.

Thread and sub-agent guests are launched with `rox up --credentials=false`, which sets
`launch.credentials: false` in the hook payload ([contract](../rox/rix-contract.md)). rix then pushes
no Claude Code or Codex sign-in into them, and still sets up the Git agent and syncs agent setup. The
harness's own `rux agent` guests aren't launched that way and still get their sign-in from rix.

## GitHub access

This is for [`rux serve`](serve.md) threads:

- The repo picker lists what the account can see (`GET /user/repos` with rix's token).
- The runner clones the repo inside the guest. Git there, for the clone and for anything Claude runs,
  uses `rux git-credential`, which fetches the token from `rux serve` when git needs it. `rux serve`
  asks rix for it, at most every five minutes. The token isn't written to the guest's disk or git
  config. Commits use your host `git config --global` name and email when both are set, else your GitHub name and noreply address. Guests started by `rox up` get the same identity from rix as their global Git config.
- That token can reach everything the account can, so treat a guest like your own shell, or give rix
  a fine-grained token limited to the repositories agents work on. `rux serve --github=false` turns
  GitHub access off.
- Guests get no SSH keys: rux starts them with `rox up --no-git-agent`, and SSH remotes are rewritten
  to HTTPS.

The harness and `rux agent` guests are different: they clone over SSH with your key through rix's Git agent (see [sub-agent guests](rux-claude.md#sub-agent-guests)).
