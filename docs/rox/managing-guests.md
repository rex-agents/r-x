---
sidebar:
  order: 4
---

# Managing guests

List guests, stop and start them, repair their connection, and delete them.

## rox ls

```
rox ls [--json] [-a|--all] [-S HOST] [--all-projects] [--cwd DIR]
```

Lists the guests on this machine: name, backend, phase, IP, projects. Inside a checkout it lists only
guests that have a checkout of this project; `--all-projects` lists every guest. `--all` also asks
every Rex host this machine can reach; `-S HOST` lists only that host's guests (the two are
mutually exclusive); other hosts add a `host` column. Hosts that can't be asked are reported and
skipped. Piped, the table has no header and is tab-separated. `ip` is `-` for Docker, Podman and
cloud guests, which aren't reached by an address.

`--json` prints `{"schema": 1, "guests": [...]}` (the contract [rix](../rix/index.md) and [rux](../rux/index.md) read); each guest has
`name`, `backend`, `engine`, `phase`, `ip`, `tailnet`, `projects`, `socket`, `home`, `user`, `agents`,
`checkouts`, `funnels`, `sessions` and, for other hosts, `host`.

## rox start, stop, reconnect

```
rox start NAME [-S HOST]       # start a stopped guest, restore access
rox stop NAME [-S HOST]        # stop it and keep it stopped
rox reconnect NAME [-S HOST]   # like start, and repairs tunnel, Rex server and tailnet node even when running
```

`stop` stops the container/VM/machine, closes tunnels, funnels and the Git forward, and marks the guest
stopped so the [service](service.md) doesn't revive it. Files are kept.
`start` and `reconnect` bring back the runtime, SSH, the Rex server, the tailnet node, the Rex host
entry, funnels, and the Git agent (`resume` hook). `start` leaves a healthy guest alone; `reconnect`
always rebuilds the connection. Both refuse a guest whose first launch never finished (phase
other than `ready`): remove it with `rox down`. A guest whose container, VM or machine was deleted
outside rox reports so.

A Railway sandbox can't be stopped and started again; use `rox down`.

## rox down

```
rox down [NAME] [--cwd DIR] [-S HOST]
```

Deletes the guest (container/VM/machine, Fly volume, Railway sandbox), its files, state, funnels,
tailnet node and Rex host entry, after the `down` hook stops its Git agent. Without NAME, rox
chooses from the current project's guests (and asks which if several). For a guest on another host,
rox runs the removal there and removes its Rex host entry here.
