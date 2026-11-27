---
sidebar:
  order: 12
---

# Remote hosts

A Rex host is another machine running a Rex server where rox is installed. `-S/--host LABEL` runs a
command there, so you can launch and manage guests on a bigger machine from your laptop.

- `rox up --host studio` runs `rox up` on `studio` in a Rex "Provision" tab (closed when it
  succeeds, left open if it fails), then registers the new guest with this machine's Rex hosts when
  it is on the tailnet. This machine reads the project and passes it (`--project`, `--repo`,
  `--branch`, your `.rox.local.json` settings) so the host needs no checkout. Launch options pass
  through; `--local` isn't supported with `--host`.
- For `start`, `stop`, `reconnect`, `down`, `shell`, `forward`, `funnel`, `add` and `open`, rox looks
  for the guest on this machine, then asks every Rex host (`rox ls --all`) and routes the command.
  Use `-S` to say which host. `rox cp` works only for guests on this machine.
- `rox ls --all` shows every reachable host's guests.
- Guests on other hosts need to be on the [tailnet](funnels-and-tailscale.md) for `rox open`,
  `rox add-remote-host` and `rox forward`; `rox shell` works through the host.

## The vault

Whose [rix](../rix/index.md) vault is used is set with `--vault`:

- `--vault host` (default): the host's rix vault signs the guest in and gives it Git keys.
- `--vault here`: the host's vault gives only the clone its Git keys (`--hooks=git` on the host), and
  afterwards this machine runs `rix apply NAME` to push credentials and agent setup over the guest's
  tailnet Rex server. `--vault here` needs `--host` and rix here.
