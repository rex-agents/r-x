---
sidebar:
  order: 6
---

# Tailscale and funnels

Put guests on your tailnet, and publish a guest's port on the public internet with Tailscale Funnel.

Without Tailscale, a guest is a Rex host reachable only from this machine. `rox config tailscale`
saves an auth key (create a reusable, ephemeral one tagged for guests in the Tailscale admin console)
so new guests join your tailnet as `NAME`, with the Rex server's own node. Then other devices, and
the Rex app on them, reach the guest, and guests on other hosts can be shell'd into and forwarded
from here. rox waits up to a minute for the node; a key that's invalid, not reusable or not allowed to
use the tags leaves it waiting for a browser sign-in, which rox reports as a rejected key.
`--no-tailscale` skips it for one launch. `start`, `reconnect` and the service rejoin a guest that was
on the tailnet using the config's settings.

Setting up the key is covered in [Configuration](configuration.md#rox-config). The key file must be
readable only by you (`chmod 600`).

Funnels use **this machine's** Tailscale (Rex's own node can't funnel, so install Tailscale):

## rox funnel

```
rox funnel add NAME PORT [--https 443|8443|10000] [--path /]
rox funnel ls [NAME]
rox funnel remove NAME [PORT | --all]
```

`rox funnel add NAME PORT` serves a guest's port at a public `https://` URL using Tailscale Funnel.
`--https` chooses the public port (443, 8443 or 10000); `--path` the path (default `/`). rox reports a
conflict with another guest's funnel or something Tailscale already serves. Funnels stop on
`rox stop` and `rox down` and are restored on `start`/`reconnect`. `rox funnel ls [NAME]` lists
them; `rox funnel remove NAME PORT` or `--all` takes them down. Funneling may first need enabling in
the Tailscale admin console. All three accept `-S, --host`.
