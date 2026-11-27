---
sidebar:
  order: 12
---

# Remote hosts

A host is another machine where rox is installed. `-S/--host HOST` runs a command there, so you can
launch and manage guests on a bigger machine from your laptop. rox needs no Rex for this: it runs
`rox` on the host over SSH (a tailnet name, a Tailscale SSH host or any `ssh_config` alias works as
`HOST`, using your own SSH config, keys and agent).

- `rox up --host studio` runs `rox up` on `studio` over SSH and shows what it printed. This machine
  reads the project and passes it (`--project`, `--repo`, `--branch`, your `.rox.local.json`
  settings) so the host needs no checkout. Launch options pass through; `--local` isn't supported
  with `--host`. With `--open-shell`, the guest is then opened through the `open-shell` hook (or
  `rox shell`).
- For `start`, `stop`, `reconnect`, `down`, `shell`, `exec`, `forward`, `funnel`, `add` and `open`,
  rox looks for the guest on this machine, then asks every known host (`rox ls --all`) and runs the
  command there: `rox shell NAME` is `ssh -t HOST rox shell NAME`, and `rox forward NAME 3000` also
  forwards this machine's port to the host's. Use `-S` to say which host. `rox cp` works only for
  guests on this machine, and `rox exec --agent-sock` only for local guests.
- Which hosts `rox ls --all` and the automatic lookup ask: `"hosts": ["studio", "mini.tail1.ts.net"]`
  in the [config](configuration.md), or `ROX_HOSTS=studio,mini`. `-S HOST` works for any host.
- `rox ls --all` shows every known host's guests.

## Host transports

How rox reaches a host is a small interface, so something other than SSH can carry it. rox has two:

- `ssh` (default).
- A program that speaks `PROGRAM host list` (one host per line) and
  `PROGRAM host run [--tty] HOST -- ARGV…` (run `rox ARGV…` there, passing stdio and status through).
  Set `"host_transport": "rox-rex"` or `ROX_HOST_TRANSPORT=rox-rex` to use Rex hosts: rox-rex runs the
  command in a session on the Rex host, and `rox ls --all` asks every Rex host. A command transport
  can't forward ports to a guest on another host.

The rux authority is meant to become a transport the same way, through this interface.

## The vault

Whose [rix](../rix/index.md) vault is used is set with `--vault`:

- `--vault host` (default): the host's rix vault signs the guest in and gives it Git keys.
- `--vault here`: the host's vault gives only the clone its Git keys (`--hooks=git` on the host), and
  afterwards this machine runs `rix apply NAME` to push credentials and agent setup through
  `rox exec`, which reaches the guest through the host. `--vault here` needs `--host` and rix here.
