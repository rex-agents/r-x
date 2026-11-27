---
sidebar:
  order: 7
---

# Status

`rix status` shows, for each guest, which agents are signed in, the state of its Git agent and when agent setup
was last synced.

```sh
rix status            # table: guest, agents (signed in/out), git agent, last sync
rix status NAME
rix status --json     # {"schema":1,"guests":[{name, agents, credentials, git_agent, git_keys, synced}]}
```

`git agent` is `none` (no keys recorded), `up` (the agent socket answers `ssh-add -l` with keys) or `down`
(see [the Git agent](git-agent.md)). Guests come from `rox ls --json --all-projects`.
