---
sidebar:
  order: 5
---

# Shell, files and ports

Get into a guest, show it in the Rex app, forward its ports and copy files to it. Guests must be running
(see [Managing guests](managing-guests.md)).

## rox shell, open, add-remote-host

```
rox shell NAME [PROJECT]       # interactive shell in a guest, attached to its Rex session
rox open  NAME [PROJECT]       # show the guest's Rex session in the Rex app
rox add-remote-host NAME       # add the guest to the Rex app's host list
```

All take `-S, --host`. `shell` opens in the guest's only checkout; with several, give the project
id or name, or you get the guest's home. `open` and `add-remote-host` need the Rex app on this
machine, and for a guest on another host that guest must be on the [tailnet](funnels-and-tailscale.md)
(otherwise use `rox shell`, which works through the host). After a launch inside the Rex app,
`--open-shell` does the same as `open`.

## rox forward

```
rox forward NAME PORT|LOCAL:GUEST... [-S HOST]
```

Forwards `localhost:LOCAL` on this machine to `localhost:GUEST` in the guest until Ctrl-C (`PORT` means
the same number on both sides). Ports are 1–65535. For a guest on another host, the guest must be on
the tailnet. To expose a port publicly, use [`rox funnel`](funnels-and-tailscale.md#rox-funnel).

## rox cp

```
rox cp NAME SOURCE[:DEST]... [--project ID] [--cwd DIR]
```

Copies files (not directories) from this machine into a guest of this machine. A relative `SOURCE`
starts from `--cwd`; `DEST` is relative to the project's checkout (the only ready project, or the one
named by `--project`), or absolute, and defaults to `SOURCE` (an absolute source defaults to its file
name). Modes are preserved and writes are atomic. It works only for guests on this machine; run it on
the guest's host otherwise.

```sh
rox cp rv-web-3f2a9c1b7d4e .env .env.local:config/.env
```
