---
sidebar:
  order: 2
---

# Models

Every agent in the `rux` [harness](lead-and-sub-agents.md) runs on a model you name as `provider/model`: Claude, any OpenAI-compatible server, or the Claude Code and Codex CLIs. This page covers choosing models, the built-in providers and the config file.

## Models: Claude Code, Codex, Claude, or anything OpenAI-compatible

Name a model as `provider/model`. The lead uses `--model`; the sub-agents use `--agent-model`
(default: the lead's), and the lead can pick one per agent. Without `--model`, rux uses
`lead_model` from `~/.rux/harness.json`, else `anthropic/claude-opus-5-5` when there's an Anthropic API key, else
`claude-code` when `claude` is installed, else `codex` when `codex` is.

Change models during a session with `/model`: it picks the selected agent's provider (showing which
have keys), then its model from the provider's own list (type to filter, or type a model it doesn't
list). The agent carries on with the conversation so far. `/agent-model` sets the model for sub-agents
started from then on. Switching waits for the agent's turn to end; `esc` interrupts it.

### Codex

`codex` (or `codex/MODEL`) runs each agent on OpenAI's Codex CLI, headless (`codex exec --json`),
so your ChatGPT sign-in works. A Codex lead works in Codex's read-only sandbox here, so it can look but
not change anything; rux's tools reach it over MCP. Codex sub-agents run in their guests without
Codex's sandbox and sign in with what `rix auth codex` stored.

### Claude Code

`claude-code` (or `claude-code/MODEL`, such as `claude-code/opus`) runs each agent on Claude Code
itself, headless, so your Claude subscription works. Claude Code runs its own tools and rux shows its
work in the same window. The lead's Claude Code runs on your machine: Read, Grep and Glob run freely,
the rux tools come from rux over MCP, and anything else (Bash, edits) asks you in rux first, unless your
Claude Code settings already allow it or it's a command Claude Code knows only reads. A sub-agent's
Claude Code runs in its guest with permissions skipped, since the guest is its sandbox; rox installs it
and rix signs it in with what `rix auth claude` stored.

### Examples

```
rux --model anthropic/claude-opus-5-5
rux --model openai/gpt-5 --agent-model openrouter/qwen/qwen3-coder
rux --model ollama/qwen3-coder          # a local model, no key
rux --model claude-code                 # Claude Code, with your Claude subscription
```

### Claude and effort

Claude goes through the Anthropic Messages API, with adaptive thinking, prompt caching, server-side
compaction for long sessions, and the server-side refusal fallback on the models that take it.
`--effort` (or `effort` in the config) sets low, medium, high (default), xhigh or max. Each agent has its own,
which `/effort` or the picker's `←` `→` change (`auto` is the engine's default). Every other provider speaks the OpenAI chat
completions API with streaming tool calls.

### OpenAI-compatible

Built in: `anthropic`, `openai`, `openrouter`, `groq`, `deepseek`, `xai`, `together`, `gemini`,
`mistral`, `ollama` and `lmstudio`. Each key is a rix secret named for the provider
(`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `OPENROUTER_API_KEY`, …); the environment is the fallback, and
Anthropic also takes an `ant auth login` profile. rux tells you which secret to set when one is missing
(see [rix secrets](../rix/index.md)).

```
rix secret set ANTHROPIC_API_KEY     # create one at https://platform.claude.com/settings/keys
```

Add other OpenAI-compatible servers, and set defaults, in `~/.rux/harness.json` (or the file `RUX_HARNESS_CONFIG` names);
command-line flags override it. A provider's `kind` is `openai` (the default) or `anthropic`, `key` names the rix secret (then
environment variable) with its API key, and `headers` adds request headers:

```json
{
  "lead_model": "anthropic/claude-opus-5-5",
  "agent_model": "anthropic/claude-sonnet-5-5",
  "effort": "high",
  "providers": {
    "vllm": {"kind": "openai", "base_url": "http://gpu-box:8000/v1", "key": "VLLM_API_KEY"}
  }
}
```
