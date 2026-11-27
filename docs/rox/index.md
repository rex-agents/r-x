# Managing VMs with rox

rox starts disposable Linux and macOS guests, clones your project into them, and gives you a
Rex session in each. It is one static Go binary. It replaces the Python `rox` script.

```
this machine                                    a guest
rox up ──▶ backend (container, Tart, Fly, Railway)  ──▶ sshd + Rex server
   │                                                     └── ~/PROJECT (a clone)  + Rex session
   ├─ rix hook EVENT NAME  (Git keys, agent sign-in, setup sync)
   └─ rox exec NAME -- …   (rix's only way into a guest)
```

- rox owns the guest: boot, SSH, the Rex server inside it, the Rex host entry on your machine, the
  tailnet node, funnels, stop/start and removal.
- rix owns secrets: Git SSH keys, Claude Code and Codex credentials, synced agent setup. rox works
  without rix; with rix on `PATH` it calls it at fixed points (see [The rix contract](rix-contract.md#the-rix-contract)).
  See also the [rix](../rix/index.md) docs. [rux](../rux/index.md) builds a multi-agent harness on rox guests.
- Every guest is named `rv-SLUG-XXXXXXXXXXXX` (the project's name, then 12 hex digits), such as
  `rv-web-3f2a9c1b7d4e`.

## Guests and backends at a glance

| Backend | What runs |
| --- | --- |
| `container` | a container from the `rox:linux` image (Docker, Podman or Apple `container`) |
| `linux`, `macos` | a Tart VM cloned from a template (Apple Silicon Mac) |
| `fly` | a Fly Machine with a volume |
| `railway` | a Railway sandbox |

Details are in [Guests and backends](guests-and-backends.md).

## Quick start

```sh
cd ~/code/web                  # a Git checkout
rox up container               # a new container guest with this project cloned in
rox ls                         # the guests with this project
rox shell rv-web-3f2a9c1b7d4e  # a shell in the project's checkout, inside the guest's Rex session
rox stop  rv-web-3f2a9c1b7d4e  # stop it, keep its files
rox start rv-web-3f2a9c1b7d4e  # start it again
rox down  rv-web-3f2a9c1b7d4e  # delete it, its files and its Rex host entry
```

`rox up` clones the **remote** branch you are on, using that branch's upstream remote (`origin` if
none). Unpushed commits and uncommitted files don't travel unless you pass `--local`. Outside a
checkout, name the repository: `rox up container --repo git@github.com:owner/web.git`.

A launch looks like this (see the README for a full transcript):

```
◆ rox up  web · main → container
  ✓ Read main (found .rox.sh and .rox.json at 3f2a9c1)
  ✓ Started rv-web-3f2a9c1b7d4e · container container
  ✓ Cloned web (main) → /home/workspace/web
  ✓ Set up web
● rv-web-3f2a9c1b7d4e  ready
```

If a launch fails after the guest exists, rox keeps it (phase `failed`) and says so; remove it with
`rox down NAME`. A failed Git clone or `.rox.sh` leaves the Rex host available so you can look
around.

## Pages in this section

| Page | What it covers |
| --- | --- |
| [Install](install.md) | installing rox, required programs, updates, the rox skill |
| [Guests and backends](guests-and-backends.md) | the five backends and their differences, guest phases |
| [Creating guests](creating-guests.md) | `rox up` and `rox add`, flags and what a launch does |
| [Managing guests](managing-guests.md) | `rox ls`, `start`, `stop`, `reconnect`, `down` |
| [Shell, files and ports](shell-and-files.md) | `rox shell`, `open`, `add-remote-host`, `forward`, `cp` |
| [Tailscale and funnels](funnels-and-tailscale.md) | joining a tailnet, `rox funnel` |
| [Templates](templates.md) | `rox template` for the `linux` and `macos` backends |
| [Project settings](project-settings.md) | `.rox.json`, `.rox.local.json`, `.rox.sh` |
| [Agents](agents.md) | installing Claude Code and Codex in guests |
| [Configuration](configuration.md) | the rox config file and `rox config` |
| [Images and caching](images-and-caching.md) | base, project, agent and Fly images, branch mirrors |
| [Remote hosts](remote-hosts.md) | running guests on another Rex host with `-S/--host` |
| [Keeping guests running](service.md) | the background service |
| [Rex actions](rex-actions.md) | every rox and rix command as a Rex action |
| [The rix contract](rix-contract.md) | hook events, `rox exec`, `rox inspect`, `rox hooks` |
| [Files, environment variables and output](environment-and-files.md) | where rox keeps things, variables, output |
| [Troubleshooting](troubleshooting.md) | common errors and fixes |

## Commands

| Command | Page |
| --- | --- |
| `rox up`, `rox add` | [Creating guests](creating-guests.md) |
| `rox ls`, `start`, `stop`, `reconnect`, `down` | [Managing guests](managing-guests.md) |
| `rox shell`, `open`, `add-remote-host`, `forward`, `cp` | [Shell, files and ports](shell-and-files.md) |
| `rox funnel` | [Tailscale and funnels](funnels-and-tailscale.md#rox-funnel) |
| `rox template` | [Templates](templates.md) |
| `rox config` | [Configuration](configuration.md#rox-config) |
| `rox service` | [Keeping guests running](service.md#rox-service) |
| `rox rex` | [Rex actions](rex-actions.md#rox-rex) |
| `rox skill`, `rox self-update` | [Install](install.md#rox-skill-self-update) |
| `rox exec`, `inspect`, `hooks` | [The rix contract](rix-contract.md#for-rix-and-other-tools) |

### Global flags

Every command takes `-v, --verbose` (or `ROX_VERBOSE=1`) to show the output of the tools rox runs.

Most commands that take a guest NAME also take `-S, --host LABEL` (the Rex host that runs it).
Without it, rox finds the guest on this machine, or else on the Rex hosts it can reach. See
[Remote hosts](remote-hosts.md).
