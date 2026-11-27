---
sidebar:
  order: 10
---

# The rox contract

[rox](../rox/index.md) calls rix at fixed points in a guest's life. This page describes that interface, for
anyone debugging a launch or writing another hook provider. You never run it by hand.

rox runs `rix hook EVENT NAME` (hidden, not for people) with the guest record on stdin, one JSON line (the output
of `rox inspect NAME --json`, `"schema": 1`), and reads one JSON line on stdout:
`{"schema":1,"agent_socket","forward","known_hosts","done":[…],"warnings":[…]}`. A schema mismatch is an error
that tells you to update both. rox's `launch` object in the record carries this launch's `--git-key`,
`--git-known-hosts`, `--no-git-agent`, the repository URL and the newly installed agents.

## Events

| Event | rix does |
| --- | --- |
| `pre-fetch` (NAME is `-`) | for an SSH URL, starts a throwaway agent in a temp dir, loads the keys without a terminal, replies with its socket and known hosts, and keeps it until rox closes stdin. If keys can't be loaded it warns and replies without a socket |
| `post-boot` | pushes credentials for the agents the guest has |
| `pre-clone` | for SSH URLs: starts the guest's Git agent, saves its keys in state, replies with `agent_socket`, `forward` and `known_hosts`. Warns if there are no keys. `https://` URLs do nothing |
| `post-provision` | pushes credentials if agents were newly installed, syncs agent setup (a failure is a warning), and stops the agent if the guest keeps keys only for the clone |
| `resume` | restarts the guest's agent if it died and replies with its socket, when keys are kept for life |
| `down` | stops the agent, deletes the guest's state and runtime folder |

Errors in `pre-clone` fail the launch; the rest are reported as warnings by rox.

## Finding each other

rix finds rox through `ROX_BIN` (set by rox when it calls rix) or `PATH`. `rox hooks` shows the active hook
provider; `"hooks": "off"` in rox's config turns rix off for rox.

On a remote host (`rox up --host`) the host's rix is used by default; with `--vault here` this machine's rix
pushes credentials over the guest's tailnet Rex server (rox calls the hidden `rix apply NAME --quiet`).
