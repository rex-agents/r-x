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
| Claude Code in thread guests, Claude Code or Codex in `rux agent` guests | rox's hooks into rix, because rux starts guests with `rox up --agents claude` (or `codex`) | `rix auth claude`, `rix auth codex` |
| GitHub, for the repo picker and for git in guests | `rix credential github` | `rix auth github` (a token), or nothing to use `gh`'s login |
| Secrets that [actions](actions.md) list | `rix secret get` | `rix secret set NAME` |
| Model API keys for the [harness](models.md) | rix secrets named for the provider | `rix secret set ANTHROPIC_API_KEY`, … |

rux's own per-thread tokens only let a guest's runner talk to its own thread.

## GitHub access

This is for [`rux serve`](serve.md) threads:

- The repo picker lists what the account can see (`GET /user/repos` with rix's token).
- The runner clones the repo inside the guest. Git there, for the clone and for anything Claude runs,
  uses `rux git-credential`, which fetches the token from `rux serve` when git needs it. `rux serve`
  asks rix for it, at most every five minutes. The token isn't written to the guest's disk or git
  config. Commits use your GitHub name and noreply address.
- That token can reach everything the account can, so treat a guest like your own shell, or give rix
  a fine-grained token limited to the repositories agents work on. `rux serve --github=false` turns
  GitHub access off.
- Guests get no SSH keys: rux starts them with `rox up --no-git-agent`, and SSH remotes are rewritten
  to HTTPS.

The harness and `rux agent` guests are different: they clone over SSH with your key through rix's Git agent (see [sub-agent guests](rux-claude.md#sub-agent-guests)).
