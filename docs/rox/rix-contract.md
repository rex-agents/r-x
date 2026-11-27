---
sidebar:
  order: 15
---

# The rix contract

rox works without [rix](../rix/index.md). With rix on `PATH`, rox calls it at fixed points in a guest's life, and rix
reaches guests only through `rox exec`. This page is the protocol, for rix and for any other tool
that wants to play either role.

rox runs `rix hook EVENT NAME` (`ROX_BIN` is set to rox's path) with the guest's record
(`rox inspect NAME --json`, schema 1, plus a `launch` object with this launch's choices) on stdin,
and reads one JSON reply from the last JSON line of stdout.

| Event | When | rix does |
| --- | --- | --- |
| `pre-fetch` | rox reads the branch (name is `-`) | lends an SSH agent until rox closes stdin |
| `post-boot` | guest reachable, agents installed | pushes credentials for those agents |
| `pre-clone` | before cloning | starts the guest's Git agent and returns its socket, which rox forwards |
| `post-provision` | projects set up | syncs agent setup, pushes credentials for newly installed agents |
| `resume` | after `start`, `reconnect` or a service restore | restores the Git agent |
| `down` | the guest is removed | stops the Git agent and forgets the guest |

The reply is `{"schema": 1, "agent_socket", "forward", "known_hosts", "done": [...], "warnings": [...]}`.
rox shows `done` and `warnings` as its own steps. A failed `pre-clone` fails the launch; every other
hook failure is a warning. Hooks run for up to ten minutes. `post-boot` runs only when agents were
requested. With no provider, a clone over SSH proceeds with a warning that the guest has no Git keys
(your `~/.ssh/known_hosts` entries for the server are still used).

Secrets reach a guest only on stdin of `rox exec NAME --stdin -- …`, never in argv, an image, or a
host process's environment. `rox hooks` shows the provider: rix on `PATH`, unless `ROX_HOOKS` or
`"hooks"` says `off`/`none` or names another program that speaks the same protocol.

## For rix and other tools

```
rox exec NAME [--stdin] [--agent-sock PATH] -- COMMAND...
rox inspect NAME --json
rox hooks
```

- `exec` runs a command in a guest over rox's SSH, passing stdout, stderr and the exit status
  through (and stdin with `--stdin`; `--agent-sock` forwards an SSH agent socket for the command). It
  never takes the guest's lock, so hooks can use it during `rox up`. For guests on other hosts it
  goes through the guest's tailnet Rex server.
- `inspect --json` prints one guest's record (schema 1). Without `--json` it prints a summary card.
- `hooks` shows the active hook provider, its version and the events it receives.

[`rox ls --json`](managing-guests.md#rox-ls) prints every guest's record in the same schema.

Hidden compatibility commands `rox key …` and `rox config agents …` forward to `rix key` and
`rix auth`/`rix sync` and print what to run instead.
