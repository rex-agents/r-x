---
sidebar:
  order: 10
---

# Configuration

The rox config holds settings for the machine that runs guests, as opposed to
[project settings](project-settings.md), which travel with a repository.

The rox config is JSON at `~/.config/rox/config.json` (`$XDG_CONFIG_HOME/rox/config.json`), or the
file passed with `--config`. `rox config` edits it.

```json
{
  "updates": "auto",
  "hooks": ["rix", "rox-rex"],
  "hosts": ["studio"],
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
| `hooks` | the [hook providers](rix-contract.md#providers) to call, in order: a string (`"rix,rox-rex"`) or a list; `"off"`/`"none"`. Default: `rix` and `rox-rex`, each when found on `PATH` |
| `hosts` | hosts `rox ls --all` and the automatic guest lookup ask, as a list: `["studio"]`. See [Remote hosts](remote-hosts.md) |
| `host_transport` | how rox reaches other hosts: `ssh` (default), or a program such as `rox-rex` |
| `container.engine` | `container`, `docker`, `podman` |
| `*.cpus`, `*.memory`, `*.image`, `*.prebuild` | defaults for the backend; `memory` like `4G`/`512M`; `prebuild` true/false (default true) |
| `linux`/`macos`: `user`, `identity`, `host_key` | VM login user (default `admin`), SSH private key, and file with the pinned host key. Required; [`rox template`](templates.md) sets them |
| `fly.app`, `org`, `region`, `disk` | app (default `rox-HASH`), org (`personal`), region (`iad`), volume GB (10); app, org and region are lowercase names |
| `railway.project` | required; the Railway project (name or id) |
| `railway.environment`, `idle_timeout` | environment (default `production`); minutes 0–120 |
| `tailscale.auth_key_file`, `tags`, `control_url` | see [Tailscale](funnels-and-tailscale.md). The key file must be readable only by you (`chmod 600`) |

Unknown settings in `tailscale` are errors; a config that still has `git_keys`, `git_known_hosts`,
`git_agent` or `agents` gets a warning that those moved to rix (`rix migrate`) and are ignored.
`rox config` rewrites preserve keys it doesn't know. Writes take a lock file beside the config.

## rox config

```
rox config [--config FILE]                 # edit the config in $VISUAL or $EDITOR (default vi)
rox config tailscale [--tag TAG]... [--control-url URL]
```

`rox config` creates `~/.config/rox/config.json` if missing, opens it, and checks it parses
afterwards. `rox config tailscale` asks for a Tailscale auth key (hidden prompt), saves it to a
private `tailscale-authkey` file beside the config, and writes the `tailscale` section. Tags default
to `tag:rox-guest`; `--control-url` takes an `https://` URL for Headscale or similar. Guests already
running stay off the tailnet.
