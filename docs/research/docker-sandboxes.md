# Spike: Docker Sandboxes (`sbx`) as a rox guest backend

Date: 2026-11-27. Researched against sbx **v0.54.1** (latest release on 2026-11-24). The sbx Linux arm64 tarball was
downloaded and its `--help` read. It could not run here: there is no `/dev/kvm`, and the license is proprietary.
Everything marked **UNVERIFIED** needs the manual test at the end, on a macOS host.

## Verdict: feasible with caveats

sbx has everything the `Backend` interface needs. It has a custom-image path (`--template`), a loopback port publisher
(`sbx port publish`), `exec`, `cp`, JSON `ls`, and a persistent microVM per sandbox. It is shaped like Railway: build a
template, create, start sshd by exec, publish the port, read the host key by exec.

Main blockers and risks, worst first:

1. **sbx never runs the image's ENTRYPOINT as PID 1.** It keeps the sandbox alive itself and starts the "agent" as a
   separate process. rox must start `rox-container` (sshd) with `sbx exec`, and again after every stop/start. `sbx exec`
   has no detach flag, so the background process has to survive the exec session ending. **UNVERIFIED.**
2. **Custom images must follow sbx's contract.** That means an `agent` user (UID 1000, `/home/agent`) in `/etc/passwd`,
   `/bin/bash`, `curl`, `git` and CA certs. `rox:linux` has none of these as written: it has a `workspace` user and
   no `agent`. Whether sbx hard-fails without `agent` is **UNVERIFIED**.
3. **Interactive Docker login is required** (`sbx login`, browser OAuth). Docs show no headless or token login.
   Fine for a developer laptop, bad for CI.
4. **Proprietary and closed source.** Governed by the Docker Subscription Service Agreement. No Go SDK is documented.
   The CLI is the only integration surface. Its JSON schema is undocumented (`sbx ls --json` fields are not in the docs).
5. **Local sandboxes are host-limited:** Apple silicon with macOS 14+, Ubuntu 24.04+ with KVM, or Windows 11 with
   Hypervisor Platform. There is no Intel Mac support, and no `--platform` for local sandboxes (cloud only).
6. **Deny-by-default network proxy.** Outbound HTTPS and git need policy allow rules, and non-HTTP TCP such as
   `git@github.com` over SSH is "forwarded transparently" but still policy-checked. Needs a policy init and probably
   allow rules (see 3).

The sbx CLI is young (about 54 minor releases, with breaking changes in 0.51 and 0.54). Pin a minimum version and
treat output parsing defensively.

## 1. What sbx is today

- Docker Sandboxes run AI coding agents in a **microVM per sandbox**, local or in Docker's cloud, driven by the `sbx`
  CLI. Each VM has its own kernel and its own private Docker Engine. Sources:
  [overview](https://docs.docker.com/ai/sandboxes/),
  [architecture](https://docs.docker.com/ai/sandboxes/architecture/),
  [security](https://docs.docker.com/ai/sandboxes/security/).
- **Install** ([install guide](https://docs.docker.com/ai/sandboxes/install/)):
  - macOS: `brew trust docker/tap; brew install docker/tap/sbx`.
  - Windows: `winget install -h Docker.sbx`.
  - Ubuntu: `curl -fsSL https://get.docker.com | sudo SBX=1 sh`, or `apt install docker-sbx`.
  - Other: tarballs and debs/rpms at <https://github.com/docker/sbx-releases>. I fetched
    `DockerSandboxes-linux-arm64.tar.gz`. It contains `sbx`, the `containerd-shim-nerdbox` runtime, a kernel and a
    rootfs; this is the "nerdbox" microVM stack.
- **Host requirements**
  - macOS 14 Sonoma+ on **Apple silicon only**.
  - Linux: Ubuntu 24.04+, amd64 or arm64, KVM and membership in the `kvm` group. Nested virtualization is needed inside
    a VM.
  - Windows 11 x64 with Windows Hypervisor Platform.
  - **Docker Desktop and Docker Engine are not required.**
- **Login and plan:**
  - `sbx login` (browser OAuth) is required, and `--no-input` blocks sign-in prompts and prints the command to run.
  - "The `sbx` CLI and local sandbox compute are free, including for commercial work." Cloud compute is paid, and org
    policy governance is a separate paid subscription ([overview](https://docs.docker.com/ai/sandboxes/)).
  - `LICENSE` in the tarball: proprietary, under the
    [Docker Subscription Service Agreement](https://www.docker.com/legal/docker-subscription-service-agreement).
  - Telemetry can be disabled: `SBX_NO_TELEMETRY=1` or `sbx settings set telemetry.disabled true`
    ([FAQ](https://docs.docker.com/ai/sandboxes/faq/)).
- **Scriptable?** Mostly. Global `--no-input` makes prompts fail instead of blocking, and `ls`, `inspect`, `stop`,
  `rm` and `port publish` take `--json` or `--format json`. First-run needs `sbx login` and a network policy
  (`sbx policy init balanced`). Without a policy init, `sbx run` prompts
  ([get started](https://docs.docker.com/ai/sandboxes/get-started/)). `sbx rm` needs `--force` to skip confirmation.
  `sbx logout` needs `--yes`.

## 2. Custom images

- **Yes, arbitrary OCI images work as templates**, but they must meet a contract
  ([templates](https://docs.docker.com/ai/sandboxes/customize/templates/),
  [base images](https://docs.docker.com/ai/sandboxes/customize/author/base-images/)):
  - `curl`, `git`, trusted CA certs;
  - executable `/bin/sh` and `/bin/bash`;
  - a non-root **`agent`** account, UID 1000, home `/home/agent`, in `/etc/passwd`, with `USER agent`. sbx reads
    uid/gid/home from that entry;
  - `ENTRYPOINT` or `CMD` set to the agent or shell to run.
  - The optional label/capability `com.docker.sandbox/sbx@1` makes conformance tooling check the contract.
- `sbx create -t/--template IMAGE` is "Container image for the sandbox". `--pull always|missing|never`; the default
  is `always` (`never` for saved local snapshots). So a **local-only tag needs `--pull never`**, and a registry image
  needs the full registry domain (`docker.io/...`; sbx doesn't resolve bare Docker Hub names).
- **How a local image gets in** (sbx has its own image store inside its runtime, not the host Docker store):
  1. `docker save rox:linux -o rox.tar && sbx template load rox.tar`, then
     `sbx create --pull never -t rox:linux shell`. The `template load` help example uses `sbx run --pull never -t
     myimage:v1.0 claude`. Whether it accepts `docker save` (or OCI-layout) tars, rather than only tars from
     `sbx template save -o`, is **UNVERIFIED**. rox's default engine on Apple Silicon is Apple `container`, which
     has `container image save`, also **UNVERIFIED**.
  2. Push to a registry and let sbx pull it (`--pull always`). Works for any host but needs a registry and
     credentials. This is the Fly route.
  3. **No external engine at all (recommended for the first build):** create from Docker's `docker.io/sbx/shell-image`,
     run rox's template steps by `sbx exec -u root` (as `railwayTemplateSteps` does for Railway), then
     `sbx template save SANDBOX rox-guest:TAG`. It stays in sbx's local image store, and `--pull never` then picks it
     up. Rebuild by tag hash, as railwayTemplate does.
  4. Experimental "kits" (`sbx create ./my-kit` or an OCI kit ref, v3 spec,
     [sandbox-kit-spec](https://github.com/docker/sandbox-kit-spec)) are a heavier packaging format. Skip them.
- **PID 1 / process model.** "`sbx` keeps the sandbox running and starts the agent as a separate process using the
  image's launch command", through non-interactive bash. No environment is read except `BASH_ENV`, so no `.profile`.
  `tini -> rox-container -> sshd` therefore does **not** become the sandbox's PID 1. For the `shell` agent, the
  launch command is the interactive shell session of `sbx run`, not a daemon. rox has to start `rox-container`
  itself with `sbx exec`, exactly like the Railway backend does (`exec ... rox-container` detached). The Containerfile
  `ENTRYPOINT` is ignored for our purposes.
- Docker's own images (`docker.io/sbx/shell-image`, Debian-based, and `shell-docker-image` with Docker Engine) are the
  fallback base.

## 3. Networking

- **Inbound: yes.** `sbx port publish SANDBOX [[HOST_IP:]HOST_PORT:]SANDBOX_PORT[/PROTO] --json`
  ([reference](https://docs.docker.com/reference/cli/sbx/port/publish/), added in 0.54.0, which deprecates `sbx ports`;
  `-p/--publish` also exists on `create` and `run`).
  - Omitting HOST_IP binds loopback.
  - Omitting HOST_PORT gives an ephemeral port.
  - Default protocol is tcp4, which is 127.0.0.1 only.
  - It starts a stopped sandbox first.
  - Mappings are re-published when the sandbox restarts. Whether the ephemeral host port stays the same is
    **UNVERIFIED**; rox should re-query it each time.
  - The service must listen on 0.0.0.0, not just 127.0.0.1. sshd's default (all interfaces) is fine.
  - 0.54.0 / 0.53 notes also mention host-port requests needing approval for kits. A user-initiated CLI publish
    appears not to prompt. **UNVERIFIED.**
- So rox can SSH to `127.0.0.1:<published>` directly, like the Docker backend. No ProxyCommand is needed.
- **Alternative: `sbx setup ssh`** (experimental). It writes `~/.ssh/config` so `ssh NAME.sbx` works with no key,
  authenticated by the daemon socket plus Docker login. How the transport works is undocumented, and it bypasses rox's
  key and host-key pinning. Don't use it, except possibly as a debugging aid.
- **Outbound:** all outbound TCP goes through a host-side proxy with a **deny-by-default policy**. HTTP/HTTPS use a
  forward proxy that enforces policy and injects credentials (the VM sees placeholders). Other TCP is "forwarded
  transparently". UDP and ICMP are blocked ([architecture](https://docs.docker.com/ai/sandboxes/architecture/),
  [security](https://docs.docker.com/ai/sandboxes/security/)). Sandboxes can't reach each other or the host network.
  - Policy is managed with `sbx policy init <allow-all|balanced|deny-all>`, `sbx policy ls/rm`, and per-sandbox
    `--deny-network`. `allow-all` is the closest match to the other backends.
  - Whether git over SSH to github.com works under `balanced` is **UNVERIFIED**. HTTPS git is expected to work if
    github.com is allowed.
  - The proxy MITMs TLS (`--tls` compatibility setting in 0.54.0), so guests need the sbx CA. The base image's
    `ca-certificates` and sbx's injection handle this. Custom images should be checked: **UNVERIFIED.**

## 4. Lifecycle mapping

All commands take `--no-input`. Local sandboxes are in the `sandboxd` daemon, which the CLI starts on demand
(`sbx daemon` manages it). Sources: `sbx <cmd> --help` for 0.54.1, plus the
[CLI reference](https://docs.docker.com/reference/cli/sbx/).

| rox `Backend` | sbx command |
|---|---|
| `Kind()` | `"sandbox"` (same word as Railway: "container, VM, machine or sandbox") |
| `Ensure()` | `sbx version`, then `sbx ls -q --no-input`. A failure means "Install sbx (`brew install docker/tap/sbx`) and run `sbx login`". Optionally `sbx policy ls` and `sbx policy init allow-all` or `balanced` if none exists. |
| `Launch()` | build/load template, then `sbx create --name N -t T --pull never --cpus C --memory M shell` (no PATH, so mountless), `sbx port publish N 22 --json`, `sbx exec` to start `rox-container`, `sbx exec` to read the host key |
| `State()` | `sbx ls --json`, match by name, map the status field. The field names and values are not documented. `sbx inspect N --json` has "State". |
| `Start()` | `sbx port publish` or `sbx exec` both auto-start a stopped sandbox. Use `sbx run -d --name N` or a no-op `sbx exec N true`, then start `rox-container` again, then wait for the host key |
| `Stop()` | `sbx stop N` |
| `Destroy()` | `sbx rm --force N`. Skip if `State` is missing. Kill any helper pids. |
| `Address()` | `sbx port ls N --json` (0.54.0, schema undocumented) or `port publish` output, giving `127.0.0.1:<hostport>` |
| `Prebuild()` | `""` at first, as Railway. Later: `sbx template save` of a configured sandbox, one per project. |
| resources | `--cpus` N (0 = all host CPUs) and `-m/--memory` (`512m`, `8g`; minimum 512 MiB; default 50% of host RAM). rox's `4G` must be lower-cased. UNVERIFIED that uppercase is rejected. |
| file copy in | `sbx cp ./f NAME:/abs/path` (absolute sandbox paths only). Not needed for SSH setup. |

Other lifecycle facts:
- **Persistence:** "Stopping doesn't delete the VM." Packages, images and files persist across stop/start. `sbx rm`
  deletes everything. A mountless sandbox keeps `/home/workspace` on its own disk, which suits rox (no host mount).
- **Workspace:** a `PATH` argument bind-mounts a host dir (virtiofs) at the same absolute path. rox doesn't need it
  because it copies or clones into the guest over SSH. Skip it.
- **`sbx exec` has no `--detach`** (removed in 0.53, "Exec sessions cannot detach"). It does have `-u root`, `-e`, `-w`.
  It auto-starts stopped sandboxes, and its exit code is the command's (125 means sbx failed).
- The `shell` agent's own entry command may also run at `sbx run`. With `create` only, nothing interactive should start.

## 5. Gotchas

- **Root and sudo:** inside the VM the agent has full sudo ("including sudo" in the
  [security page](https://docs.docker.com/ai/sandboxes/security/)). Docker's templates run as `agent` with sudo. rox
  should pass `ROX_SUDO=1`, as it does for Apple container, Fly, and Railway, because the VM is the boundary. Run
  `rox-container` via `sbx exec -u root`, since it writes `/etc/sudoers.d` and `authorized_keys`.
- **User mismatch:** sbx wants `agent`/UID 1000. rox's login user is `workspace` (`AllowUsers=workspace`). SSH login
  needs only the `workspace` account, so keep both. Add an `agent` user for sbx's contract (UID 1000 is probably
  `workspace`'s, so alias with `useradd -o -u 1000 -g workspace -d /home/workspace agent`, or move `workspace`
  to 1001). **UNVERIFIED** whether sbx needs `agent` for the `shell` agent with a bare template.
- **Docker-in-sandbox:** each sandbox VM already has a private Docker Engine, and `shell-docker-image` bundles one.
  rox's `ROX_SUDO=1` path runs its own `dockerd` inside the guest and needs a writable `/proc/sys` and cgroups. If the
  sbx guest runs as a container inside the VM's engine, that is a container within a container and may be
  non-privileged. **UNVERIFIED.** Fallback: derive from `shell-docker-image`.
- **Startup time:** not documented. Test it. Expect a template-load and first-boot cost, then fast restarts from the
  cached image.
- **Architecture:** local sandboxes run native-arch only (Apple silicon means arm64 guests). `--platform` is cloud
  only. rox:linux for arm64 is already built for Apple container, so this is the same image.
- **rox's own agents/credentials:** sbx injects API creds through its proxy (`sbx secret`). rox's guests carry their
  own keys, so this is unneeded, and the proxy's header rewriting could surprise tools. Check HTTPS to
  api.anthropic.com from inside the guest.
- **Secrets in create/exec:** `-e KEY=VALUE` on the command line is visible in the host process list. For the public
  key that is fine. Use `--env-file` for anything secret.
- **Removal side effects:** `rm` also removes git remotes it added to the host repo (for clone mode only) and
  scoped secrets. Not relevant for mountless sandboxes.
- **Name rules:** 2-63 chars, letters, digits, hyphens, periods. rox names should fit. `default` is reserved.
- **Cloud mode** (`sbx --cloud`) is a separate, paid backend with other limits (billable shapes, no `-u`, templates
  uploaded as tars). Out of scope for v1.

## Design sketch: `sbxBackend`

New file `internal/rox/backend_sbx.go`. `Guest.Backend == "sbx"` selects it in `backendFor`. `Kind()` returns
`"sandbox"`. Config section `sbx:` is optional (policy preset, minimum version). `Guest.ContainerID` stores the sandbox
name (names are unique and stable), or the id from `sbx inspect`.

```go
func sbx(g *Guest, timeout time.Duration, args ...string) (string, error) // sbx --no-input <args>, with a pinned CLI path
```

**Template (`sbxTemplate`)**, once per (rox version, steps) hash, like `railwayTemplate`:
1. `sbx create --name rox-tpl-<hash> shell` (mountless), `sbx exec -u root` each template step from the
   Containerfile equivalent (the `workspace` user, sshd, `/usr/local/bin/rox-container`, an `agent` alias user, a
   `/etc/skel`, and tini if wanted), then `sbx template save rox-tpl-<hash> rox-guest:<hash>`, then `sbx rm -f rox-tpl-<hash>`.
2. Or, when a host engine exists: `<engine> save rox:linux -o $RUNTIME/rox.tar && sbx template load $RUNTIME/rox.tar`
   (faster and exact, if sbx accepts the format).

**Launch:**
1. `newLoginKey(g)`.
2. `sbxTemplate(g)`, then
   `sbx create --name g.Name -t rox-guest:<hash> --pull never [--cpus N] [--memory 4g] shell`.
   Save `g.ContainerID = g.Name`.
3. `sbx port publish g.Name 127.0.0.1::22/tcp4 --json` gives the host port. Store it in `g.Port` if rox keeps one.
4. Start the guest: `sbx exec -u root -e ROX_SSH_PUBLIC_KEY=<key> -e ROX_SUDO=1 g.Name sh -c
   'setsid nohup /usr/local/bin/rox-container </dev/null >/var/log/rox-container.log 2>&1 &'`. Same shape as Railway's
   `exec ... --detach`. If background processes die with the exec session, use `sbx exec` with a long-lived helper (a
   rox-owned `sbx exec ... sleep infinity` process kept by a pid file, as `forwardedAddress` does), or a
   `sbx exec` launching a `systemd-run`/`daemonize` equivalent.
5. Host key: `sys.WaitFor(boot, ..., sbx exec g.Name cat /etc/ssh/ssh_host_ed25519_key.pub)` and `pinHost(g, key)`.
6. Return `127.0.0.1`, the published port.

**State:** `sbx ls --json`, find `g.Name`, map status. Running and "starting" are `running`, stopped is `stopped`,
absent is `missing`. Pin the field names after the manual test.

**Start:** `sbx exec -u root g.Name true` (this starts the VM), re-run step 4 (the public key is read again from
`g.Identity + ".pub"`), wait for the host key and sshd, re-query the port.

**Stop:** `sbx stop g.Name`. **Destroy:** `sbx rm --force g.Name`, tolerating "not found".

**Address:** `sbx port ls g.Name --json` for the 22/tcp mapping. If it is gone, re-publish. Return `127.0.0.1`.

**Prebuild:** `""` initially. A later version can build a project template with `sbx template save`.

**Wiring and UX:** add `sbx` to the backend list in `docs/rox.md`; add an `--sbx` or `rox up sbx` entry point beside
`container` and `railway`; document that the host needs Apple silicon, `sbx login` and a network policy. Gate
`Ensure()` on `sbx version >= 0.54.0` (port publish).

## Manual test script (macOS, Apple silicon)

```sh
set -x
brew trust docker/tap && brew install docker/tap/sbx
sbx version && sbx login
sbx policy init allow-all          # or balanced; then re-test outbound below

# 1. Build a rox-ish template without any engine, and see the contract
sbx create --name probe shell
sbx exec -u root probe sh -c 'id agent; cat /etc/os-release | head -2; which sshd tini git bash sudo'
sbx exec -u root probe sh -c 'apt-get update && apt-get install -y openssh-server tini sudo'
sbx template save probe probe-tpl:1 -o /tmp/probe.tar
sbx rm -f probe

# 2. Does a docker-built image load? (needs docker or `container`)
docker build -t rox:linux -f internal/assets/Containerfile internal/assets   # adjust context
docker save rox:linux -o /tmp/rox.tar && sbx template load /tmp/rox.tar && sbx template ls
#    (Apple container:  container image save rox:linux -o /tmp/rox.tar)

# 3. Create from the custom image: does it fail without an `agent` user?
time sbx create --name t1 -t rox:linux --pull never --cpus 2 --memory 4g shell
sbx ls --json; sbx inspect t1 --json                      # record state field names/values

# 4. Port publish + background sshd that survives the exec session
sbx port publish t1 127.0.0.1::22/tcp4 --json             # note host port
ssh-keygen -q -t ed25519 -N '' -f /tmp/k
sbx exec -u root -e ROX_SSH_PUBLIC_KEY="$(cat /tmp/k.pub)" -e ROX_SUDO=1 t1 \
  sh -c 'setsid nohup /usr/local/bin/rox-container </dev/null >/tmp/rc.log 2>&1 &'
sleep 3; sbx exec t1 sh -c 'ps aux | grep sshd; tail /tmp/rc.log'     # is sshd still there after exec exited?
sbx exec t1 cat /etc/ssh/ssh_host_ed25519_key.pub
ssh -i /tmp/k -p <hostport> -o StrictHostKeyChecking=no workspace@127.0.0.1 'id; sudo -n true && echo sudo-ok'

# 5. Outbound
ssh ... 'curl -sI https://github.com | head -1; git ls-remote https://github.com/rex-agents/r-x HEAD; \
         ssh -o StrictHostKeyChecking=no -T git@github.com; curl -sI https://api.anthropic.com | head -1'

# 6. Stop / start / persistence / port stability
ssh ... 'echo hi > ~/persist'
sbx stop t1; sbx ls --json
sbx exec -u root t1 true; sbx port ls t1 --json            # same host port? sshd running? (expect NO sshd)
sbx exec -u root -e ROX_SSH_PUBLIC_KEY="$(cat /tmp/k.pub)" -e ROX_SUDO=1 t1 sh -c 'setsid nohup /usr/local/bin/rox-container </dev/null >/tmp/rc.log 2>&1 &'
ssh ... 'cat ~/persist'

# 7. Docker inside (rox runs dockerd when ROX_SUDO=1)
ssh ... 'sudo docker info | head -3; docker run --rm hello-world'

# 8. Cleanup
sbx rm -f t1; sbx template rm rox:linux
```

Questions the script answers: does `docker save`/Apple `container image save` output load (2); is the `agent` user
required (3); the real `ls --json` schema and status strings (3); does a background sshd survive the exec and
reconnect (4); does git and Anthropic traffic pass under which policy (5); is the published port stable across
stop/start, and is persistence real (6); does dockerd work (7); startup times (the `time` in 3).

## Sources

- <https://docs.docker.com/ai/sandboxes/> and `/install/`, `/get-started/`, `/architecture/`, `/security/`, `/faq/`,
  `/release-notes/`
- <https://docs.docker.com/ai/sandboxes/customize/templates/>,
  <https://docs.docker.com/ai/sandboxes/customize/author/base-images/>
- CLI reference: <https://docs.docker.com/reference/cli/sbx/> (`create`, `run`, `exec`, `ls`, `inspect`, `cp`,
  `port/publish`, `template`, `template/load`, `template/save`, `setup/ssh`)
- <https://github.com/docker/sbx-releases> (v0.54.1 tarball, `LICENSE`, `sbx --help` output)
- <https://github.com/docker/sandbox-kit-spec>
