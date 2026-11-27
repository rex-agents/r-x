---
sidebar:
  order: 11
---

# Security model

What rix keeps on your machine, what it puts in guests, and how.

## Stays on your machine

Git private keys and their passphrases (only the agent socket is forwarded; guests can sign with the keys while
the forward is up but cannot read them); [named secrets](named-secrets.md); the GitHub token (served to tools on
stdout on request); every config and secret file, all mode 0600 in 0700 folders, and rix refuses credential
files that others can read.

## Goes into guests

The Claude Code token or API key (`~/.rox/agents.env`, 0600), Codex's login file or API key
(`~/.codex/auth.json`, 0600), and the synced skills, instructions, MCP server definitions and settings.
These are only sent to guests that have that agent installed. Remote MCP server definitions may include
headers or tokens, so sync them only to guests you trust; a guest is as trusted as your own shell. Treat anyone
who can run commands in a guest as holding those credentials. Prefer limited tokens (API keys with a spend limit,
fine-grained GitHub tokens).

## In transit

Secrets reach guests only on stdin of `rox exec`, so they do not show in process lists.
