---
sidebar:
  order: 16
---

# Rex, as a hook

rox doesn't need Rex. Guests are plain SSH hosts: `rox up`, `shell`, `exec`, `cp`, `forward`,
`start`, `stop`, `reconnect` and `down` work on a machine with no `rex` binary. Everything Rex lives in
**rox-rex**, a [hook provider](rix-contract.md) that ships beside rox.

With rox-rex on `PATH` (and not turned off with `ROX_HOOKS` or `"hooks"`), rox calls it and each
guest gets what Rex users had before:

| Event | rox-rex does |
| --- | --- |
| `post-boot` | installs Rex in the guest if it isn't there (the release channel matches your local Rex), starts the guest's Rex server, opens a socket on this machine that reaches it, and joins the tailnet when [`tailscale`](funnels-and-tailscale.md) is configured (reply `endpoint` is the guest's tailnet name) |
| `post-provision` | creates the project's Rex session (reply `session`; with `rox up -- COMMAND` it runs the command, kept open), registers the guest as a Rex host on this machine, and shares this machine's Rex hosts with the guest |
| `resume` | restarts the guest's Rex server if needed, reopens the socket, and has the guest rejoin the tailnet |
| `down` | signs the guest out of the tailnet, removes the Rex host, and closes the Guest tabs it opened |
| `open-shell` | shows the guest's session in the Rex app on this machine, or opens a Guest tab in this Rex terminal's session; with neither, rox runs `rox shell` here |

rox-rex reaches guests only through `rox exec` and reads them from the hook's record, like rix. The
socket is a relay: each connection to `rex.sock` in the guest's runtime folder runs `rex dial-stdio`
in the guest through `rox exec`. Its state is `rex.json` in the guest's state folder.

Without rox-rex: `rox up -- COMMAND` warns that the command didn't run (use `rox exec`), the
`tailscale` setting is ignored with a warning, and `rox open NAME` and `--open-shell` run
`rox shell`.

## Installing Rex in guests

rox-rex installs Rex in a Linux guest on first boot, for the user's account when the guest has no
sudo (containers). It doesn't install the Rex app in a macOS guest: prepare the template with
`rox template macos --with-rex`. `rox template linux --with-rex` (and `--rex-channel`) puts Rex in the
Linux template so first boot is faster. The `rox:linux` image no longer contains Rex.

## Commands

```
rox-rex install [--no-reload]    # make every rox and rix command a Rex action
rox-rex uninstall | lua
rox-rex add-host NAME            # add a guest to the Rex app, which keeps its own host list
rox-rex self-update [--check]
```

`install`, `uninstall` and `lua` are the old `rox rex …` commands, see [Rex actions](rex-actions.md).
`add-host` is the old `rox add-remote-host`. `rox rex …` and `rox add-remote-host` still exist as
hidden commands that print what to run.

## Hosts that are Rex hosts

rox reaches other hosts over SSH by default. With `"host_transport": "rox-rex"` (or
`ROX_HOST_TRANSPORT=rox-rex`) it uses Rex hosts instead, as it used to: `rox up --host LABEL` runs in a
session on that Rex host, and `rox ls --all` asks every Rex host. See
[Remote hosts](remote-hosts.md#host-transports).
