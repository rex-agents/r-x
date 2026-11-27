---
sidebar:
  order: 13
---

# Keeping guests running

Container runtimes and VMs stop when you log out or reboot, and tunnels break when a laptop sleeps.
The rox service restores them.

## Keeping guests running: the service

`rox service install` installs a user service (a launchd agent on macOS, a systemd user unit
`rox.service` elsewhere) that runs `rox service run`. Every 15 seconds (`--interval`) it finds `ready`
guests that aren't `stopped` and restores any whose runtime stopped or whose tunnel was lost, backing
off from 30 seconds to ten minutes for a guest that keeps failing. It exits to be restarted when the
rox binary on disk changes. [`rox stop`](managing-guests.md#rox-start-stop-reconnect) marks a guest so the service leaves it alone until
`rox start`.

`rox service status` shows whether it runs; `rox service uninstall` removes it (guests keep running,
nothing restarts them). On Linux, to keep it running while you're logged out, run
`loginctl enable-linger $USER`. The log is in the state folder.

## rox service

```
rox service install | uninstall | status | run [--interval SECONDS]
```

`run` is what the installed service executes; you rarely call it yourself. The service file locations
are listed in [Files, environment variables and output](environment-and-files.md).
