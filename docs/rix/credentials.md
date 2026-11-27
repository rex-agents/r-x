---
sidebar:
  order: 2
---

# Credentials

`rix auth` stores what an agent signs in with (Claude Code, Codex) and points the config at it. Secret files are
written with mode `0600`, in the same folder as the [config](configuration.md), and are named by the config.
For the GitHub token see [GitHub access](github-access.md).

```sh
rix auth claude                  # prompts (no echo) for a token or API key
rix auth claude --off            # forget it; guests start Claude Code signed out
rix auth codex                   # use your Codex login file, or paste an OpenAI API key
rix auth codex --auth-file ~/other/auth.json
rix auth codex --api-key         # an OpenAI API key instead of a login file
rix auth codex --off
rix auth github                  # see "GitHub access"
```

## Claude

Accepts a single token starting `sk-ant-`: a subscription token (`sk-ant-oat01-…`, made by
`claude setup-token`, for Claude Pro or Max) or an Anthropic API key (`sk-ant-api03-…`). At a terminal, with
`claude` installed, rix offers to run `claude setup-token` for you. An empty answer keeps guests signed out.
The token is saved as `claude-token` (setting `token_file`) or the key as `anthropic-api-key` (`api_key_file`).
In a guest they become `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY`.

## Codex

By default rix copies your Codex login file, `~/.codex/auth.json` (or `$CODEX_HOME/auth.json`, or
`--auth-file`), into guests' `~/.codex/auth.json`. It is referenced in place, not copied into the vault, and
must be readable only by you (`chmod 600`). At a terminal rix asks before using it. With `--api-key`, or when
there is no login file, rix prompts for an OpenAI API key, saves it as `openai-api-key`, and signs the guest in
with `codex login --with-api-key`. If guests sign each other out because they share one ChatGPT login, use
`--api-key`.

## One credential per agent

Only one credential per agent is kept: running `rix auth AGENT` again replaces the old one, and the old secret
file rix wrote is deleted once the config no longer names it. Existing `sync` settings survive. A credential
file named in the config that is group- or world-readable is refused with a `chmod 600` hint.

A new credential reaches running guests with [`rix apply`](pushing-to-guests.md); new guests get it from rox
automatically.
