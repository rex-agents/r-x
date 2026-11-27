---
sidebar:
  order: 15
---

# The hook contract

rox works without [rix](../rix/index.md) and without Rex. With hook providers on `PATH`, rox calls
them at fixed points in a guest's life, and they reach guests only through `rox exec`. The two
providers rox knows are **rix** (secrets) and **rox-rex** ([Rex](rex-hook.md)). This page is the
protocol, for them and for any other tool that wants to play the same role. (The file is still
called `rix-contract.md`, so old links keep working.)

rox runs `PROVIDER hook EVENT NAME` (`ROX_BIN` is set to rox's path) with the guest's record
(`rox inspect NAME --json`, schema 1, plus a `launch` object with this launch's choices) on stdin,
and reads one JSON reply from the last JSON line of stdout.
`launch.credentials: false` (from `rox up --credentials=false`) tells the hook to push no agent credentials; the Git agent and setup sync still happen.

| Event | When | Providers do |
| --- | --- | --- |
| `pre-fetch` | rox reads the branch (name is `-`) | rix lends an SSH agent until rox closes stdin |
| `post-boot` | guest reachable, agents installed (always sent to rox-rex; to rix only when agents were requested) | rix pushes credentials for those agents; rox-rex installs and starts Rex in the guest and joins the tailnet |
| `pre-clone` | before cloning | rix starts the guest's Git agent and returns its socket, which rox forwards |
| `post-provision` | projects set up | rix syncs agent setup and pushes credentials for newly installed agents; rox-rex creates the Rex session and registers the Rex host |
| `resume` | after `start`, `reconnect` or a service restore | rix restores the Git agent; rox-rex restores Rex and the tailnet |
| `down` | the guest is removed | rix stops the Git agent and forgets the guest; rox-rex removes the Rex host and signs the guest out of the tailnet |
| `open-shell` | `--open-shell` and `rox open` | rox-rex shows the guest in the Rex app or opens a Guest tab; reply `done` when a shell was opened |

The reply is `{"schema": 1, "agent_socket", "forward", "known_hosts", "session", "endpoint", "done": [...], "warnings": [...]}`.
Every field is optional.

- `done` and `warnings`: rox shows them as its own steps.
- `agent_socket`, `forward`, `known_hosts`: Git access for the clone (pre-fetch, pre-clone, resume).
- `session`: the id of a session the provider created for the project (or, with no project, the
  guest) that `post-provision` is about. rox keeps it in the guest's record.
- `endpoint`: an address other devices reach the guest at, such as its tailnet name. rox keeps it as
  the guest's `tailnet`.
- For `open-shell`, a non-empty `done` means a shell was opened. Otherwise rox runs `rox shell` in
  the current terminal.

A failed `pre-clone` fails the launch; every other hook failure is a warning. Hooks run for up to ten
minutes. With no provider, a clone over SSH proceeds with a warning that the guest has no Git keys
(your `~/.ssh/known_hosts` entries for the server are still used).

## Providers

rox can call several providers. The list comes from, in order of precedence:

1. `ROX_HOOKS`, such as `ROX_HOOKS=rix,rox-rex`
2. `"hooks"` in the [config](configuration.md): a string (`"rix,rox-rex"`) or a list (`["rix", "rox-rex"]`)
3. each of `rix` and `rox-rex` found on `PATH`

`off` or `none` means no providers. A provider that isn't installed is skipped. Providers are called
in the listed order for each event, and their replies are merged: `done` and `warnings` are
concatenated; the first non-empty `agent_socket` (with its `forward`), `known_hosts`, `session` and
`endpoint` win. Put rix before rox-rex, so credentials are in place before Rex starts in the guest.

rox sends rix only the six events above `open-shell`, so rix works unchanged. rox-rex gets
`post-boot`, `post-provision`, `resume`, `down` and `open-shell`. A provider rox doesn't know gets
every event and must answer `{}` to the ones it doesn't know.

`rox hooks` lists every provider with its version and events.

The record's `dir` and `runtime` are the guest's state and socket folders on the launcher, where a
provider can keep its own per-guest state (rox-rex writes `rex.json` in `dir`). They're empty for a
record of a guest on another host. `launch` carries `project` (the project this event is about),
`command` (what `rox up` got after `--`), `config` and `no_tailscale`.

Secrets reach a guest only on stdin of `rox exec NAME --stdin -- …`, never in argv, an image, or a
host process's environment.

## For providers and other tools

```
rox exec NAME [--stdin] [--agent-sock PATH] -- COMMAND...
rox inspect NAME --json
rox hooks
```

- `exec` runs a command in a guest over rox's SSH, passing stdout, stderr and the exit status
  through (and stdin with `--stdin`; `--agent-sock` forwards an SSH agent socket for the command). It
  never takes the guest's lock, so hooks can use it during `rox up`. For guests on other hosts it
  runs `rox exec` on the host that runs the guest, over the [host transport](remote-hosts.md#host-transports).
- `inspect --json` prints one guest's record (schema 1). Without `--json` it prints a summary card.
- `hooks` shows the active providers, their versions and the events they receive.

[`rox ls --json`](managing-guests.md#rox-ls) prints every guest's record in the same schema.

Hidden compatibility commands `rox key …`, `rox config agents …`, `rox rex …` and
`rox add-remote-host` forward to `rix key`, `rix auth`/`rix sync`, `rox-rex …` and `rox-rex add-host`
and print what to run instead.
