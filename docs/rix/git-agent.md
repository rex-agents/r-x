---
sidebar:
  order: 8
---

# The Git agent

For each guest rix runs one `ssh-agent -D -a SOCKET` on your machine, and loads only that guest's
[keys](git-keys.md) with `ssh-add`. rox forwards the socket into the guest. The keys, and their passphrases,
stay on this machine.

```sh
rix git-agent start NAME   # start it and load keys, asking for passphrases in this terminal
rix git-agent stop NAME
rix git-agent sock NAME    # print the socket path
```

## Socket and passphrases

The socket is `$XDG_RUNTIME_DIR/rix/ID/agent.sock`, or `/tmp/rix-UID/ID/agent.sock` when that variable is unset
(as on macOS), where `ID` is the last part of the guest name. Directories are mode 0700; keep paths short, since
unix socket paths have a length limit. Hooks run without a terminal, so an encrypted key that needs a
passphrase can't be loaded then: run `rix git-agent start NAME` in a terminal, then `rox reconnect NAME` to
forward it. `git-agent start` on a guest rix doesn't know yet uses the configured keys (or ssh's defaults) and
marks them to be kept for the guest's life. rix only signals an agent it can prove it started: the recorded PID
must still have the recorded start time and the exact command line `ssh-agent -D -a SOCKET`.

## Lifetime

By default a guest keeps its Git agent for its whole life so agents can push. Set `"git_agent": false` in the
[config](configuration.md), or use `rox up --no-git-agent`, to keep the keys for the clone only; rix then stops
the agent when provisioning finishes. See [the rox contract](rox-contract.md) for when rix starts and stops it.
