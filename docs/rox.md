# rox

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
  without rix; with rix on `PATH` it calls it at fixed points (see [The rix contract](#the-rix-contract)).
- Every guest is named `rv-SLUG-XXXXXXXXXXXX` (the project's name, then 12 hex digits), such as
  `rv-web-3f2a9c1b7d4e`.

## Contents

- [Install](#install)
- [Quick start](#quick-start)
- [Guests and backends](#guests-and-backends)
- [Commands](#commands)
- [Project settings: .rox.json, .rox.local.json, .rox.sh](#project-settings)
- [Agents](#agents)
- [Configuration](#configuration)
- [Images and caching](#images-and-caching)
- [Tailscale and funnels](#tailscale-and-funnels)
- [Remote hosts](#remote-hosts)
- [The rix contract](#the-rix-contract)
- [Keeping guests running: the service](#keeping-guests-running-the-service)
- [Rex actions](#rex-actions)
- [Files, environment variables and output](#files-environment-variables-and-output)
- [Troubleshooting](#troubleshooting)

## Install

```sh
brew install rex-agents/tap/r-x        # or download an archive from the releases page
rox rex install                        # optional: every rox and rix command as a Rex action
rox skill install                      # optional: the rox skill for Claude Code and Codex
```

`rox --version` prints the version. A released binary updates itself: `rox self-update` installs the
latest release (and refreshes the skill and the Rex actions), `rox self-update --check` only says
whether one is out. Automatic checks happen at most every six hours and follow `"updates"` in the
config (`"auto"` the default, `"notify"`, `"off"`) or `ROX_UPDATES`.

rox needs these programs on `PATH`, depending on what you launch:

| Always | `rex`, `ssh`, `ssh-keygen` (plus `git` when a project is cloned from this machine) |
| --- | --- |
| `container` backend | the engine: `docker`, `podman`, or Apple's `container` |
| `linux`, `macos` backends | `tart` (Apple Silicon Mac only) |
| `fly` backend | `fly` or `flyctl`, logged in (`fly auth login`) |
| `railway` backend | `railway`, logged in (`railway login`) |
| Funnels | `tailscale` on this machine |
| `rox template` | `tart`, `ssh`, `ssh-keygen`, and `ssh-keyscan` (linux); `ditto` and the Rex app in `/Applications/Rex.app` (macos) |

rox checks the ones a launch needs before it starts and says which is missing.

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

## Guests and backends

`rox up [container|linux|macos|fly|railway]` chooses a backend. Without the argument, rox uses
`"backend"` from `.rox.local.json`, then from `.rox.json` on the branch; with neither it stops and
asks you to choose one.

| Backend | What runs | User | Needs |
| --- | --- | --- | --- |
| `container` | a container from the `rox:linux` image (Debian, sshd, Rex, git, Docker) | `workspace` | an engine: Apple `container` (default on Apple Silicon Macs), `docker` (default elsewhere), or `podman` |
| `linux` | a Linux VM cloned from a Tart template | `admin` by default | an Apple Silicon Mac, Tart, and a template from `rox template linux` |
| `macos` | a macOS VM cloned from a Tart template | `admin` by default | the same, with `rox template macos` |
| `fly` | a Fly Machine with a volume for the home directory | `workspace` | the Fly CLI, logged in |
| `railway` | a Railway sandbox | `workspace` | the Railway CLI, logged in, and `railway.project` in the config |

Details that differ:

- **container.** `--engine container|docker|podman` (or `container.engine` in the config) picks the
  engine; the default is `container` on Apple Silicon macOS and `docker` everywhere else. The image
  is `rox:linux`, which rox builds with your engine the first time and again when its Containerfile
  changes. Docker and Podman containers get no sudo and are reached through a published loopback
  port (`127.0.0.1:PORT`), and restart with the runtime (`--restart unless-stopped`). Apple's
  containers each run in their own VM with their own IP, give `workspace` sudo, and run Docker inside
  the guest. `--image` uses another image; rox skips the pull for `rox:` images.
- **linux / macos.** Guests are Tart clones of a prepared template. `rox template linux|macos`
  prepares the template and writes `linux.image`/`macos.image`, `user`, `identity` and `host_key`
  into the config; `rox up linux` fails and names `rox template linux` when they aren't set.
  `--image NAME` launches another template. Tart runs with `--no-graphics --no-clipboard`; its log
  is `tart.log` in the guest's state folder.
- **fly.** rox creates (once) a Fly app named `rox-` plus a hash of your account (or uses `fly.app`),
  builds rox's guest image on Fly's remote builder when the Containerfile changed, and runs a
  machine (default 2 CPUs, 4G memory, region `iad`, org `personal`, 10 GB volume mounted at
  `/home/workspace`). rox reaches it through `fly proxy`. `--cpus`, `--memory` and `--image` apply.
- **railway.** Sandboxes are sized by your Railway plan, so `--cpus`, `--memory`, `--image` and the
  corresponding config keys are refused. rox builds a sandbox template on first use. A Railway
  sandbox can't be stopped and started: `rox stop`/`rox start` tell you to use `rox down`.

A guest has a *phase* (`starting`, `ready`, `failed`, `removing`; shown by `rox ls`) and is `stopped`
after `rox stop`.

## Commands

Global flag on every command: `-v, --verbose` (or `ROX_VERBOSE=1`) shows the output of the tools rox
runs.

Most commands that take a guest NAME also take `-S, --host LABEL` (Rex host that runs it). Without
it, rox finds the guest on this machine, or else on the Rex hosts it can reach. See
[Remote hosts](#remote-hosts).

### rox up

```
rox up [container|linux|macos|fly|railway] [flags] [-- COMMAND...]
```

Starts and provisions a new guest. Inside a Git checkout it also clones the project; use
`--no-project` for a bare guest. Everything after `--` runs as the command of the project's Rex
session (kept open when it exits).

| Flag | Meaning |
| --- | --- |
| `--repo URL`, `--branch NAME`, `--remote NAME` | what to clone instead of the checkout's current branch and remote. `--repo` and `--remote` are mutually exclusive. HTTPS and SSH (`ssh://` or `user@host:path`) remotes only. |
| `--project NAME` | name the project when launching from `--repo` without a checkout (needs `--repo`). Without `--branch`, rox uses the remote's default branch. |
| `--cwd DIR` | the directory in the project to start from (default: current directory) |
| `--local` | also copy this checkout's unpushed commits and uncommitted files (not gitignored ones) into the guest, and use the checkout's own `.rox.json`. Needs a commit; can't be combined with `--host`, `--project` or `--no-project`. `.rox.sh` runs on the copied checkout, and no project image is used. |
| `--no-project` | start a guest with no project, even inside a checkout |
| `--image IMAGE` | container image, Fly image, or Tart template to launch |
| `--engine ENGINE` | `container`, `docker` or `podman` (container backend only) |
| `--cpus N`, `--memory SIZE` | CPUs and memory (`8G`, `512M`) for a container or VM; `--cpus` must be positive |
| `--agents LIST` | agents to install for this launch: `claude,codex` or `claude=1.2.3` |
| `--no-agents` | install no agents, whatever the project lists |
| `--no-hook` | skip the project's `.rox.sh` (also disables the project image) |
| `--no-prebuild` | clone and run `.rox.sh` at boot instead of using a project image |
| `--rebuild` | rebuild the project image before launching (an error when no image would be used) |
| `--no-tailscale` | keep this guest off the tailnet even when tailscale is configured |
| `--no-git-agent` | give the guest Git keys only while cloning, not for its whole life |
| `--git-key PATH` (repeatable), `--git-known-hosts FILE` | Git SSH identities and `known_hosts` for rix to use for this launch, replacing rix's configured ones |
| `--boot-timeout SECONDS` | per readiness check (default 120) |
| `--config FILE` | use this rox config instead of `~/.config/rox/config.json` (must exist) |
| `--open-shell` | open the guest's session in the Rex app (inside a Rex terminal; errors outside one) |
| `-S, --host LABEL`, `--vault host\|here` | launch on another Rex host; see [Remote hosts](#remote-hosts) |

```sh
rox up                                  # backend from .rox.json
rox up container --cpus 4 --memory 8G
rox up fly --agents claude
rox up container --no-project -- htop
rox up container --repo https://github.com/owner/web.git --branch dev
rox up container --local                # include my uncommitted work
rox up linux -S studio                  # on the Rex host "studio"
```

What a launch does, in order: read the branch for `.rox.sh` and `.rox.json` through a cached mirror
(rix lends an SSH agent for this: `pre-fetch`); resolve backend, settings and agents; make sure the
backend's runtime is up; prepare images; start the guest and wait for SSH; install agents (`post-boot`
hook); start Rex in the guest; join the tailnet if configured; register the Rex host; clone (the
`pre-clone` hook gives the guest its Git agent); run `.rox.sh`; start the project's Rex session; run the
`post-provision` hook. Each step prints one line.

### rox add

```
rox add NAME [flags] [-- COMMAND...]
```

Clones another project into a running guest of this machine (or, found automatically, on another
Rex host). Takes the project flags of `rox up` (`--repo`, `--branch`, `--remote`, `--project`, `--cwd`,
`--local`, `--no-hook`, `--agents`, `--no-agents`, `--git-key`, `--git-known-hosts`, `--config`,
`--open-shell`, `--vault`, `-S/--host`) and `--no-git-agent`. NAME must be a guest name. Unlike
`up`, it requires a checkout or `--repo`. The guest must be running; a stopped one asks you to
`rox start` it first, and one that didn't finish setup asks you to `rox reconnect` it. Agents the added
project lists are installed if the guest lacks them.

### rox ls

```
rox ls [--json] [-a|--all] [-S HOST] [--all-projects] [--cwd DIR]
```

Lists the guests on this machine: name, backend, phase, IP, projects. Inside a checkout it lists only
guests that have a checkout of this project; `--all-projects` lists every guest. `--all` also asks
every Rex host this machine can reach; `-S HOST` lists only that host's guests (the two are
mutually exclusive); other hosts add a `host` column. Hosts that can't be asked are reported and
skipped. `--json` prints `{"schema": 1, "guests": [...]}` (the contract rix and rux read); each guest has
`name`, `backend`, `engine`, `phase`, `ip`, `tailnet`, `projects`, `socket`, `home`, `user`, `agents`,
`checkouts`, `funnels`, `sessions` and, for other hosts, `host`. Piped, the table has no header and is
tab-separated. `ip` is `-` for Docker, Podman and cloud guests, which aren't reached by an address.

### rox start, stop, reconnect

```
rox start NAME [-S HOST]       # start a stopped guest, restore access
rox stop NAME [-S HOST]        # stop it and keep it stopped
rox reconnect NAME [-S HOST]   # like start, and repairs tunnel, Rex server and tailnet node even when running
```

`stop` stops the container/VM/machine, closes tunnels, funnels and the Git forward, and marks the guest
stopped so the [service](#keeping-guests-running-the-service) doesn't revive it. Files are kept.
`start` and `reconnect` bring back the runtime, SSH, the Rex server, the tailnet node, the Rex host
entry, funnels, and the Git agent (`resume` hook). `start` leaves a healthy guest alone; `reconnect`
always rebuilds the connection. Both refuse a guest whose first launch never finished (phase
other than `ready`): remove it with `rox down`. A guest whose container, VM or machine was deleted
outside rox reports so.

### rox down

```
rox down [NAME] [--cwd DIR] [-S HOST]
```

Deletes the guest (container/VM/machine, Fly volume, Railway sandbox), its files, state, funnels,
tailnet node and Rex host entry, after the `down` hook stops its Git agent. Without NAME, rox
chooses from the current project's guests (and asks which if several). For a guest on another host,
rox runs the removal there and removes its Rex host entry here.

### rox shell, open, add-remote-host

```
rox shell NAME [PROJECT]       # interactive shell in a guest, attached to its Rex session
rox open  NAME [PROJECT]       # show the guest's Rex session in the Rex app
rox add-remote-host NAME       # add the guest to the Rex app's host list
```

All take `-S, --host`. `shell` opens in the guest's only checkout; with several, give the project
id or name, or you get the guest's home. `open` and `add-remote-host` need the Rex app on this
machine, and for a guest on another host that guest must be on the tailnet (otherwise use
`rox shell`, which works through the host). After a launch inside the Rex app, `--open-shell` does
the same as `open`.

### rox forward

```
rox forward NAME PORT|LOCAL:GUEST... [-S HOST]
```

Forwards `localhost:LOCAL` on this machine to `localhost:GUEST` in the guest until Ctrl-C (`PORT` means
the same number on both sides). Ports are 1–65535. For a guest on another host, the guest must be on
the tailnet.

### rox cp

```
rox cp NAME SOURCE[:DEST]... [--project ID] [--cwd DIR]
```

Copies files (not directories) from this machine into a guest of this machine. A relative `SOURCE`
starts from `--cwd`; `DEST` is relative to the project's checkout (the only ready project, or the one
named by `--project`), or absolute, and defaults to `SOURCE` (an absolute source defaults to its file
name). Modes are preserved and writes are atomic.

```sh
rox cp rv-web-3f2a9c1b7d4e .env .env.local:config/.env
```

### rox funnel

```
rox funnel add NAME PORT [--https 443|8443|10000] [--path /]
rox funnel ls [NAME]
rox funnel remove NAME [PORT | --all]
```

Puts a port of a guest on the public internet with Tailscale Funnel; see
[Tailscale and funnels](#tailscale-and-funnels). All accept `-S, --host`.

### rox template

```
rox template macos|linux [--name N] [--base IMAGE] [--distro D] [--default] [--rex-channel C]
                         [--base-password P] [--boot-timeout S] [--config FILE] [-S HOST]
```

Prepares a Tart template for the `macos` or `linux` backend (Apple Silicon only) and points the
config at it. It clones a base image (`ghcr.io/cirruslabs/macos-tahoe-base:latest`, or for Linux an
Ubuntu 24.04, Debian trixie or Fedora 42 image), boots it, creates a VM login key (`~/.ssh/rox_vm`),
replaces the base image's password (default `admin`) with a random one saved beside the config, installs Rex (and Git for
Linux), shuts it down, and writes `user`, `identity`, `host_key` and `image` into `macos` or `linux`
in the config. Run it again after upgrading Rex.

- `--name`: template name (default `rox-macos`, or `rox-DISTRO`).
- `--distro ubuntu|debian|fedora|arch` (linux; default ubuntu; `arch` has no published image, so pass
  `--base`). `--default` makes this the `linux.image` even if another is set. `--rex-channel stable|preview|snapshot`.
  These three are refused for macos.
- `--base`: the Tart image to clone from. `--base-password`: the base image's login password.
- `--boot-timeout`: seconds per readiness check (default 600). `-S` runs it on another Rex host.

A first run downloads a large image.

### rox config

```
rox config [--config FILE]                 # edit the config in $VISUAL or $EDITOR (default vi)
rox config tailscale [--tag TAG]... [--control-url URL]
```

`rox config` creates `~/.config/rox/config.json` if missing, opens it, and checks it parses
afterwards. `rox config tailscale` asks for a Tailscale auth key (hidden prompt), saves it to a
private `tailscale-authkey` file beside the config, and writes the `tailscale` section. Tags default
to `tag:rox-guest`; `--control-url` takes an `https://` URL for Headscale or similar. Guests already
running stay off the tailnet.

### rox service

```
rox service install | uninstall | status | run [--interval SECONDS]
```

See [Keeping guests running](#keeping-guests-running-the-service).

### rox rex

```
rox rex install [--no-reload] | uninstall | lua
```

See [Rex actions](#rex-actions).

### rox skill, self-update

```
rox skill install [--agent claude|codex]... [--dir DIR] [--force]
rox self-update [--check]
```

`skill install` copies the bundled rox skill into `~/.claude/skills` and/or the Codex skills folder
(each agent set up on this machine, or the ones named by `--agent`), or into `--dir` (such as a project's
`.claude/skills`). `--agent` and `--dir` can't be combined. `--force` replaces a folder named `rox`
that isn't a rox skill. `self-update` also refreshes the skill.

### For rix and other tools

```
rox exec NAME [--stdin] [--agent-sock PATH] -- COMMAND...
rox inspect NAME --json
rox hooks
```

- `exec` runs a command in a guest over rox's SSH, passing stdout, stderr and the exit status
  through (and stdin with `--stdin`; `--agent-sock` forwards an SSH agent socket for the command). It
  never takes the guest's lock, so hooks can use it during `rox up`. For guests on other hosts it
  goes through the guest's tailnet Rex server.
- `inspect --json` prints one guest's record (schema 1). Without `--json` it prints a summary card.
- `hooks` shows the active hook provider, its version and the events it receives.

Hidden compatibility commands `rox key …` and `rox config agents …` forward to `rix key` and
`rix auth`/`rix sync` and print what to run instead.

## Project settings

A project configures its guests with up to three files. All JSON files are checked strictly: an
unknown or misplaced key is an error that names the file.

- **`.rox.json`** on the branch rox clones. It is read from a cached mirror, so it needs no checkout
  (or, with `--local`, from your checkout).
- **`.rox.local.json`** in the root of your own checkout, never read from the remote. It overrides
  `.rox.json`. rox prints a line when it uses it.
- **`.rox.sh`** on the branch: a script run in the project's checkout in the guest.

Keys in `.rox.json` and `.rox.local.json`:

```json
{
  "backend": "container",
  "agents": {"claude": true, "codex": "0.9.1"},
  "container": {"cpus": 4, "memory": "8G", "image": "rox:linux", "prebuild": true}
}
```

| Key | Meaning |
| --- | --- |
| `backend` | `container`, `linux`, `macos`, `fly` or `railway`; used when `rox up` gets no backend |
| `agents` | agents to install: `true` for the latest release, or a version such as `"1.2.3"`. See [Agents](#agents) |
| `container`, `linux`, `macos` | `cpus`, `memory`, `image`, `prebuild` for that backend |
| `fly`, `railway` | no settings; a branch can't choose what your cloud account runs or spends |

Settings that describe the launching machine (`engine`, `user`, `identity`, `host_key`, `git_keys`,
`git_known_hosts`, `tailscale`, `git_agent`) and the cloud sections' keys can only be set in rox's
own config and are rejected in project files.

Precedence, lowest to highest: the launcher's rox config section, `.rox.json`, `.rox.local.json`,
command-line flags. For `backend`: flag, `.rox.local.json`, `.rox.json`.

**`.rox.sh`** runs with the checkout as its working directory and these variables: `ROX_PROJECT_DIR`,
`ROX_NAME` (the guest), `ROX_BACKEND`, and `ROX_PHASE`, which is `build` when it is baked into a
project image (no Git keys, no Rex server, no `SSH_AUTH_SOCK`) and `boot` when it runs in a new guest
(with `REX_SERVER` set). It never gets your Git keys. A missing or unreadable branch (for example,
no network to the remote) is a warning: rox then runs `.rox.sh` at boot and ignores `.rox.json`. A
broken `.rox.json` stops the launch.

## Agents

rox installs agent CLIs only when a project asks. Claude Code and Codex are supported:

- `agents` in `.rox.json` or `.rox.local.json`, or `--agents` for one launch (`claude`, `codex`,
  `claude=1.2.3,codex`). `--no-agents` installs none. Later sources win: `.rox.json`, `.rox.local.json`,
  then the flag.
- `true`/no version means the latest release, looked up at most every six hours (cached in
  `~/.local/state/rox/agent-versions.json`); a failed check warns and uses the cached version. A
  version (`1.2.3`, `1.2.3-beta.1`) pins one.
- On container guests, agents are baked into a last image layer (`rox:NAME-agents`), so a new release
  doesn't rerun `.rox.sh`; if that build fails, they're installed at boot. rox records the installed
  versions in the guest record (`rox inspect`).
- Signing the agents in is rix's job (`rix auth claude|codex`); rox calls the `post-boot` hook with
  the agents that are new to the guest.

## Configuration

The rox config is JSON at `~/.config/rox/config.json` (`$XDG_CONFIG_HOME/rox/config.json`), or the
file passed with `--config`. `rox config` edits it. It holds the settings for the machine that runs
guests:

```json
{
  "updates": "auto",
  "hooks": "rix",
  "container": {"engine": "docker", "cpus": 4, "memory": "8G", "image": "rox:linux", "prebuild": true},
  "linux": {"image": "rox-ubuntu", "user": "admin", "identity": "~/.ssh/rox_vm", "host_key": "~/.ssh/rox_linux_host.pub"},
  "macos": {"image": "rox-macos", "user": "admin", "identity": "~/.ssh/rox_vm", "host_key": "~/.ssh/rox_macos_host.pub"},
  "fly": {"app": "", "org": "personal", "region": "iad", "disk": 10, "image": "", "cpus": 2, "memory": "4G"},
  "railway": {"project": "my-project", "environment": "production", "idle_timeout": 30},
  "tailscale": {"auth_key_file": "~/.config/rox/tailscale-authkey", "tags": ["tag:rox-guest"], "control_url": ""}
}
```

| Setting | Meaning |
| --- | --- |
| `updates` | `auto` (default), `notify`, `off` |
| `hooks` | `"off"`/`"none"`, or a program other than rix to call for [hook events](#the-rix-contract) |
| `container.engine` | `container`, `docker`, `podman` |
| `*.cpus`, `*.memory`, `*.image`, `*.prebuild` | defaults for the backend; `memory` like `4G`/`512M`; `prebuild` true/false (default true) |
| `linux`/`macos`: `user`, `identity`, `host_key` | VM login user (default `admin`), SSH private key, and file with the pinned host key. Required; `rox template` sets them |
| `fly.app`, `org`, `region`, `disk` | app (default `rox-HASH`), org (`personal`), region (`iad`), volume GB (10); app, org and region are lowercase names |
| `railway.project` | required; the Railway project (name or id) |
| `railway.environment`, `idle_timeout` | environment (default `production`); minutes 0–120 |
| `tailscale.auth_key_file`, `tags`, `control_url` | see [Tailscale](#tailscale-and-funnels). The key file must be readable only by you (`chmod 600`) |

Unknown settings in `tailscale` are errors; a config that still has `git_keys`, `git_known_hosts`,
`git_agent` or `agents` gets a warning that those moved to rix (`rix migrate`) and are ignored.
`rox config` rewrites preserve keys it doesn't know. Writes take a lock file beside the config.

## Images and caching

- **Base image (container).** `rox:linux` is built from the embedded Containerfile with your engine
  the first time, and rebuilt when its toolkit label (`io.rox.toolkit`) differs from the binary's.
  A custom `--image` is left alone.
- **Project image.** When the branch has a `.rox.sh`, rox bakes the project into an image once
  (clone, run `.rox.sh` as `ROX_PHASE=build`) and launches from it, so later `rox up`s skip the setup.
  Containers are named `rox:PROJECT`; Tart images `rox-PROJECT-BACKEND`. The image is reused while the
  `.rox.sh` file, the base image, the repository URL and the checkout folder are unchanged
  (containers record them as image labels; Tart images in `~/.local/state/rox/images/`); the launch then
  updates the image's checkout instead of cloning. `--rebuild` forces a rebuild; `--no-prebuild`,
  `--no-hook`, `--local`, `--no-project`, `prebuild: false`, and the `fly` and `railway`
  backends skip images. If the bake fails, the error suggests `--no-prebuild`.
- **Branch mirror.** To read `.rox.sh` and `.rox.json`, rox fetches the branch into a bare mirror in
  `~/.cache/rox/repos/` (one per remote URL, one hashed ref per branch), under a lock.
- **Agent layer.** See [Agents](#agents).
- **Fly image.** Built on Fly's builder, tagged `registry.fly.io/APP:rox-TOOLKIT`, rebuilt when the
  toolkit changes.

## Tailscale and funnels

Without Tailscale, a guest is a Rex host reachable only from this machine. `rox config tailscale`
saves an auth key (create a reusable, ephemeral one tagged for guests in the Tailscale admin console)
so new guests join your tailnet as `NAME`, with the Rex server's own node. Then other devices, and
the Rex app on them, reach the guest, and guests on other hosts can be shell'd into and forwarded
from here. rox waits up to a minute for the node; a key that's invalid, not reusable or not allowed to
use the tags leaves it waiting for a browser sign-in, which rox reports as a rejected key.
`--no-tailscale` skips it for one launch. `start`, `reconnect` and the service rejoin a guest that was on the tailnet using the config's settings.

`rox funnel add NAME PORT` serves a guest's port at a public `https://` URL using Tailscale Funnel on
**this machine's** Tailscale (Rex's own node can't funnel, so install Tailscale). `--https`
chooses the public port (443, 8443 or 10000); `--path` the path (default `/`). rox reports a
conflict with another guest's funnel or something Tailscale already serves. Funnels stop on
`rox stop` and `rox down` and are restored on `start`/`reconnect`. `rox funnel ls [NAME]` lists
them; `rox funnel remove NAME PORT` or `--all` takes them down. Funneling may first need enabling in
the Tailscale admin console.

## Remote hosts

A Rex host is another machine running a Rex server where rox is installed. `-S/--host LABEL` runs a
command there:

- `rox up --host studio` runs `rox up` on `studio` in a Rex "Provision" tab (closed when it
  succeeds, left open if it fails), then registers the new guest with this machine's Rex hosts when
  it is on the tailnet. This machine reads the project and passes it (`--project`, `--repo`,
  `--branch`, your `.rox.local.json` settings) so the host needs no checkout. Launch options pass
  through; `--local` isn't supported with `--host`.
- `--vault host` (default): the host's rix vault signs the guest in and gives it Git keys.
  `--vault here`: the host's vault gives only the clone its Git keys (`--hooks=git` on the host), and
  afterwards this machine runs `rix apply NAME` to push credentials and agent setup over the guest's
  tailnet Rex server. `--vault here` needs `--host` and rix here.
- For `start`, `stop`, `reconnect`, `down`, `shell`, `forward`, `funnel`, `add` and `open`, rox looks
  for the guest on this machine, then asks every Rex host (`rox ls --all`) and routes the command.
  Use `-S` to say which host. `rox cp` works only for guests on this machine.
- `rox ls --all` shows every reachable host's guests.

## The rix contract

rox runs `rix hook EVENT NAME` (`ROX_BIN` is set to rox's path) with the guest's record
(`rox inspect NAME --json`, schema 1, plus a `launch` object with this launch's choices) on stdin,
and reads one JSON reply from the last JSON line of stdout.

| Event | When | rix does |
| --- | --- | --- |
| `pre-fetch` | rox reads the branch (name is `-`) | lends an SSH agent until rox closes stdin |
| `post-boot` | guest reachable, agents installed | pushes credentials for those agents |
| `pre-clone` | before cloning | starts the guest's Git agent and returns its socket, which rox forwards |
| `post-provision` | projects set up | syncs agent setup, pushes credentials for newly installed agents |
| `resume` | after `start`, `reconnect` or a service restore | restores the Git agent |
| `down` | the guest is removed | stops the Git agent and forgets the guest |

The reply is `{"schema": 1, "agent_socket", "forward", "known_hosts", "done": [...], "warnings": [...]}`.
rox shows `done` and `warnings` as its own steps. A failed `pre-clone` fails the launch; every other
hook failure is a warning. Hooks run for up to ten minutes. `post-boot` runs only when agents were
requested. With no provider, a clone over SSH proceeds with a warning that the guest has no Git keys
(your `~/.ssh/known_hosts` entries for the server are still used).

Secrets reach a guest only on stdin of `rox exec NAME --stdin -- …`, never in argv, an image, or a
host process's environment. `rox hooks` shows the provider: rix on `PATH`, unless `ROX_HOOKS` or
`"hooks"` says `off`/`none` or names another program that speaks the same protocol.

## Keeping guests running: the service

Container runtimes and VMs stop when you log out or reboot, and tunnels break when a laptop sleeps.
`rox service install` installs a user service (a launchd agent on macOS, a systemd user unit
`rox.service` elsewhere) that runs `rox service run`. Every 15 seconds (`--interval`) it finds `ready`
guests that aren't `stopped` and restores any whose runtime stopped or whose tunnel was lost, backing
off from 30 seconds to ten minutes for a guest that keeps failing. It exits to be restarted when the
rox binary on disk changes. `rox stop` marks a guest so the service leaves it alone until
`rox start`.

`rox service status` shows whether it runs; `rox service uninstall` removes it (guests keep running,
nothing restarts them). On Linux, to keep it running while you're logged out, run
`loginctl enable-linger $USER`. The log is in the state folder.

## Rex actions

`rox rex install` writes `~/.config/rex/rox.lua` (or beside `$REX_CONFIG`) and adds a
`require("rox")` line to Rex's `init.lua`, then reloads Rex (`--no-reload` skips that). Every rox and
rix command becomes a Rex action named `PROGRAM_COMMAND`, for example `rox_up`, `rox_ls`,
`rox_funnel_add`, `rix_status`. See the README for examples. `rox rex lua` prints the module,
`rox rex uninstall` removes it. rox updates rewrite installed actions.

## Files, environment variables and output

| What | Where |
| --- | --- |
| Config | `~/.config/rox/config.json` (`XDG_CONFIG_HOME`) |
| State | `~/.local/state/rox/` (`XDG_STATE_HOME`): `NAME/guest.json` (record), `NAME/known_hosts`, `NAME/tart.log` and other logs, `images/`, `agent-versions.json`, `remote/login` (key for guests on other hosts), `templates/` |
| Cache | `~/.cache/rox/repos/` (`XDG_CACHE_HOME`): branch mirrors |
| Sockets | `/tmp/rox-UID/ID/` per guest (SSH control, relay and Rex sockets) |
| In the guest | `$HOME/.rox/NAME/` (Rex socket, state), checkouts in `$HOME/PROJECT` (`/home/workspace/PROJECT`) |
| Service | `~/Library/LaunchAgents/io.rox.service.plist`, or `~/.config/systemd/user/rox.service` |
| Rex actions | `~/.config/rex/rox.lua` |

Guest records are the same JSON the Python rox wrote and are read as they are.

| Variable | Effect |
| --- | --- |
| `ROX_VERBOSE=1` | same as `--verbose` |
| `ROX_UPDATES` | `auto`, `notify` or `off`; overrides the config's `updates` |
| `ROX_HOOKS` | hook provider program, or `off`/`none`; overrides the config |
| `ROX_THEME` | `light` or `dark` terminal theme (otherwise guessed from `COLORFGBG`) |
| `NO_COLOR`, `TERM=dumb` | plain output |
| `XDG_CONFIG_HOME`, `XDG_STATE_HOME`, `XDG_CACHE_HOME` | move the folders above |
| `REX_CONFIG` | Rex's `init.lua`, for `rox rex install` |
| `REX_SESSION` | set by Rex terminals; required by `--open-shell` |

Output: each step is a spinner, then one line. A failed step shows the last lines of what it
captured. Without a terminal (or with `NO_COLOR`) output is plain, and `rox ls` prints tab-separated
rows.

## Troubleshooting

- **`docker isn't running. Start it and try again`** — start the engine (for Podman on macOS,
  `podman machine start`). For Apple's `container`, rox starts its services itself; if that fails,
  run `container system start`.
- **`Install Apple's container tool … or choose --engine docker`** — `--engine container` is the default
  on Apple Silicon and needs the `container` CLI.
- **`Choose a backend with rox up container, linux, macos, fly or railway, or set "backend" in .rox.json`** —
  pass a backend or set `backend`.
- **`Configure user, identity and host_key for linux in …, or run rox template linux`** — prepare a
  template first (Apple Silicon Mac with Tart).
- **`… is not inside a Git checkout. Run from a checkout, or pass --repo`** — or use `--no-project`.
- **`Could not read BRANCH here, so .rox.sh runs at boot and .rox.json is ignored`** — rox couldn't
  fetch the branch (network, or no key). The launch continues without images; check your SSH access
  or install rix.
- **`Couldn't find … default branch … Pass --branch`** — name the branch.
- **`--rebuild needs a project image`** — the launch has no image (see [Images and caching](#images-and-caching)).
- **`Building rox:NAME failed while running .rox.sh. Launch with --no-prebuild`** — the setup script
  fails in the build; `--no-prebuild` runs it in the guest where you can look.
- **A guest is `failed`** — `rox down NAME` and launch again. The state folder keeps logs until then.
- **`NAME is stopped. Start it with rox start NAME`** — `rox stop` was used; `shell`, `cp`, `forward`
  need a running guest.
- **A guest's tunnel is gone after sleep or restart** — `rox reconnect NAME`, or `rox service install`.
- **`The container for NAME no longer exists`** — it was deleted outside rox; `rox down NAME` cleans up.
- **`Tailscale still wants a browser sign-in, so the auth key was rejected`** — create a reusable key
  allowed to use the configured tags (`rox config tailscale`).
- **`… holds a Tailscale auth key, so only you may read it`** — `chmod 600` the key file.
- **`Railway sandboxes can't be stopped and started again`** — use `rox down`.
- **`rox cp copies into guests on this machine`** — run it on the guest's host.
- **A hook warning** — `rox hooks` shows the provider; run with `--verbose` for details. Without
  rix, guests clone without your Git keys and agents aren't signed in.
- **Anything else** — `rox -v COMMAND` streams the output of every tool rox runs.
