---
sidebar:
  order: 5
---

# GitHub access

rix can hand a GitHub token to tools on this machine, such as [rux](../rux/index.md), which asks for it on
demand to list repositories and to answer the Git credential helper in its guests.

```sh
rix auth github          # prompts for a personal access token; stored as github-token (0600)
rix auth github --gh     # use gh's login on this machine; forgets any stored token
rix auth github --off    # tools get no GitHub access from rix
rix credential github          # prints the token
rix credential github --json   # {"username":"x-access-token","password":"…","source":"vault"|"gh"}
```

A blank answer to the prompt is the same as `--gh`. Without a stored token, `rix credential github` runs
`gh auth token --hostname github.com`. The token never goes into a guest from rix itself; rux fetches it
on demand (see the [rux docs](../rux/index.md)). Use a fine-grained token limited to the repositories agents work
on. `--api-key` and `--auth-file` are rejected for `github`, and `--gh` for the agents.

The setting is `github` in the [config](configuration.md).
