---
sidebar:
  order: 1
---

# Authorities, threads and the pack (design contract)

Status: design contract for the authority/quorum rework. Parallel branches implement it; anything a
branch needs to change here must be called out in its report.

## Words

| Word | Meaning |
| --- | --- |
| **host** | A machine (Mac or Linux) that can start rox guests. |
| **authority** | The rux daemon on a host (`rux authority`, evolved from `rux serve`). Owns that host's threads, sub-agents, guests and its local vault access. |
| **thread** | A project agent that manages sub-agents (previously "lead"; in `rux serve`, threads and managers). Runs in a rox guest by default. |
| **sub-agent** | A worker a thread starts. Always in a rox guest unless the user opts out. |
| **quorum / pack** | Authorities joined together. |
| **pack leader** | The authority that aggregates the pack's hosts and workloads, and places new work. |
| **vault host** | The authority whose rix serves credentials to the pack (default: the pack leader at quorum creation). |

## CLI

| Command | Does |
| --- | --- |
| `rux` | Starts the local authority if it isn't running (detached, `~/.rux/authority.sock` + HTTP), then opens the **authority TUI** attached to the pack leader (or the local authority when not in a pack). |
| `rux agent [PROMPT]` | Today's harness TUI (`runHarness`), in the current context: on the host, or inside a VM. Flags unchanged (`--model`, `--agent-model`, `--effort`). |
| `rux agent spawn\|ls\|read\|send\|wait\|focus\|stop` | Unchanged subcommands. A first argument that is one of these is a subcommand; anything else is a prompt. |
| `rux authority [start\|stop\|status\|logs]` | Manage the daemon. `start --foreground` is what `rux serve` used to be. `rux serve` stays as a hidden alias of `rux authority start --foreground`. |
| `rux quorum create\|join HOST\|leave\|status\|promote HOST\|policy` | Pack membership (see below). |
| `rux PROMPT...` | Deprecated: prints a one-line notice and runs `rux agent PROMPT...`. |

## Where work runs

- Default: every thread and every sub-agent runs in its own rox guest.
- Opt-out: `--local` on `rux agent` / thread creation (`"placement": {"local": true}`) runs on the host itself.
- Pinning: `"placement": {"authority": "NAME"}` (CLI `--on NAME`) pins to an authority; otherwise the pack leader places it.
- A thread in a VM runs `rux agent` under `rux runner` (the existing serve runner). It spawns sub-agents
  by calling its authority's guest MCP endpoint (`/mcp`, thread-scoped token), which forwards placement to the leader.

## Credentials: the model proxy and the vault

- Model keys never enter guests. Each authority serves a **model proxy** on its guest listener:
  `/proxy/anthropic/...`, `/proxy/openai/...` (OpenAI-compatible). Guests get
  `ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL` pointing at it and a thread-scoped token as the API key;
  the authority swaps in the real key. Claude Code subscription tokens (`sk-ant-oat…`) are injected
  the same way.
- The authority gets credentials from its **vault source**: `local` (its own rix) or `leader`
  (the vault host's authority over the quorum link). Precedence: spawn flag `--vault` > project setting >
  `~/.rux/authority.json` `"vault"` > pack default (`leader`). Outside a pack: `local`.
- Fetched credentials are held in memory only, with a TTL (default 10 min), never written to disk.
- GitHub token: `rux git-credential` → own authority → vault source (unchanged chain otherwise).
- Git SSH keys: the vault host's rix runs the guest's `ssh-agent`; the member authority relays the
  socket over the quorum link (`/quorum/ssh-agent/{guest}` WebSocket) into the guest. (Phase 2 may land
  after the rest; until then members fall back to their local rix for Git keys and say so in `status`.)
- The vault host enforces a policy: `{"members": {"NAME": {"allow": ["ANTHROPIC_API_KEY", "github", ...]}}}`
  (`rux quorum policy`). Every vault request is audited (member, thread, secret name, time).

## Quorum

- Transport: HTTPS/WebSocket over the tailnet between authorities (`/quorum/*` on the authority's quorum
  listener, default port 7710). Membership auth: `rux quorum join HOST` prints a one-time code on the
  leader (`rux quorum create` / `rux quorum invite`); joining exchanges per-member bearer credentials,
  stored 0600 in `~/.rux/quorum.json`.
- Members are the source of truth for their own workloads. They push a **host report** to the leader
  every 2s and on change: `{"authority","host":{"os","cpus","mem","load","guests_max"},"threads":[...],"subagents":[...],"term"}`.
- Replicated config (small): members, succession order, vault host, vault policy, project defaults.
- Leader election: with ≥3 authorities, `hashicorp/raft` over the quorum link elects the leader and
  replicates the config log. With 2, no automatic failover: `rux quorum promote HOST`.
- Fencing: every placement carries the leader's raft term; authorities reject placements from an older term.
- During a failover, running work continues; placement requests queue on members and retry.
- Vault host is a separate role and does not move automatically. If it's unreachable: cached model keys
  keep working until TTL, Git push stops for relayed agents, new credentialed work fails with a clear
  error, members with `vault: local` are unaffected. The TUI and web app show "vault unreachable".
- Scheduling: pinned → that authority; else the member with the most free guest slots, then lowest
  load, then the requesting authority (locality) on ties.

## One API, two clients (TUI and web app in sync)

The TUI and the web app are both clients of the same authority HTTP API; neither has features or state
the other can't see. The web app's existing `/api/*` endpoints stay and gain:

| Endpoint | Returns |
| --- | --- |
| `GET /api/pack` | `{"leader","term","vault_host","vault_ok","authorities":[{"name","url","status","host":{...},"local":bool,"guests":[...]}],"clients":[...]}` |
| `GET /api/pack/stream` | SSE: `pack` snapshots, then `pack.delta`, `pack.clients` and `thread.*` events. The single live feed both clients render from. Each open stream is a connected client (see below). |
| `GET /api/threads?authority=NAME` | Threads across the pack (each with `authority`, `guest`, `placement`, `subagents`). |
| `POST /api/threads` | Gains `placement` and `vault`. |

The leader serves the pack-wide view; a member's `/api/pack*` proxies to the leader (or serves its local
view, flagged `"degraded": true`, when there is none). Every mutating action in the TUI is a call to this
API; anything the TUI can do, the web app gets the same endpoint for, and the web app gains a pack view
(leader → project agents → threads) and a monitor (clients → hosts → guests). Shared Go types live in `internal/rux/authapi` (JSON tags are
the wire format; the web app mirrors them).

### Manager, guests and clients

The pack view carries three more things, all additive:

- **Thread `kind`.** A thread summary has `"kind": "manager"` when it is a project's manager (see [projects and managers](projects-and-managers.md)); it is omitted for every other thread. A manager's workers are the threads whose `parent` is the manager's id. `POST /api/projects/{id}/manager` with `{"text": ...}` starts a project's manager when it has none.
- **Guests.** Each authority in the pack lists the rox guests on its host in `guests`: `{"name","phase","backend","cpu","mem_bytes","mem_limit","thread"}`. `phase` is `running`, `stopped`, `starting`, and so on; `backend` is `container`, `linux`, `macos`, `fly` or `railway`; `cpu` is a percent of one CPU; `thread` is the id of the thread whose `guest` is this one, if any. CPU and memory come from the host's container stats, which refresh while a pack stream or the overview is open. Guests travel in `pack.delta` with the rest of the authority.
- **Clients.** `clients` lists the UIs connected to the pack right now, each a live `GET /api/pack/stream` connection: `{"id","kind","device","authority","since","last_seen"}`. `kind` is `tui` or `web`: the TUI sends `X-Rux-Client: tui` and `X-Rux-Device: <host name>`, and anything else is `web`. `device` is `X-Rux-Device`, else the tailnet peer name or remote host. `authority` is the authority the client connected through. `last_seen` is refreshed on every keepalive (20 s). Each authority reports its own clients to the leader with its host report, so the leader lists every client exactly once, under the authority it came through; a member that proxies a stream to the leader sets `X-Rux-Via: <its name>` and the leader uses that name. The `pack.clients` event carries the whole list (`[]ClientInfo`) whenever clients connect or leave (not when only `last_seen` changes).

## The authority TUI

Node graph, left to right: **leader → project agents → threads**, with a preview column on the
right (the selected node's last events, files, cost). rux's existing palette (`harness_palette.go`) and the
animated T-rex (`harness_logo.go`) at the bottom of the first column. PACK is **LEADER → PROJECT AGENTS → THREADS** (a project's manager and its workers; a thread's sub-agents open one step further right). MONITOR is **CLIENTS → HOSTS → GUESTS**: the UIs connected, every authority, and the rox VMs on the selected host (see [the authority TUI](authority-tui.md)). Header tabs: `PACK` `MONITOR` (an open conversation shows as a `PACK › project › thread` breadcrumb); header
right: live/degraded dot, leader name, term, vault status, uptime. Footer: breadcrumb and key hints
(`←↑↓→` move, `enter` open, `n` new thread, `tab` monitor, `a` archived, `?` help, `q` quit). `enter` on a
thread opens it (its conversation, the same as the web app's thread page); `n` creates a thread via
`POST /api/threads`.

## Rex is optional: the rex rox hook

rox must not assume a Rex server on every host or guest. Everything Rex moves behind rox's hook
protocol (`docs/rox/rix-contract.md`) as a second provider:

- rox gets **multiple hook providers**: `"hooks": ["rix", "rex"]` (also `ROX_HOOKS=rix,rex`), called in
  order per event; replies are merged (`done`/`warnings` concatenated; the first non-empty
  `agent_socket`/`forward`/`known_hosts` wins). Default: each of `rix`, `rox-rex` found on `PATH`.
- New events for the Rex provider: `post-boot` (install and start Rex in the guest), `post-provision`
  (create the project's Rex session; reply gains `"session"`), `resume` (restore the Rex tunnel),
  `down` (remove the Rex host entry), and `open-shell` (open a shell in the Rex app).
- The hook program is `rox-rex` (built from this repo, `cmd/rox-rex`), and holds `rexbridge`, Rex
  host registration, the app-opening code and `rox-rex install`.
- rox core uses plain SSH (and the tailnet for guests on other hosts) for `exec`, `shell` and the
  remote host path; remote hosts become authorities (`rox up --host NAME` goes through that host's
  authority when rux is present, else SSH over the tailnet). It never needs Rex.
- rux uses Rex panes only for `rux claude` / `rux agent spawn` when `REX_SESSION` is set; nothing in a
  guest or an authority requires Rex.

Implemented as: `"hooks": ["rix", "rox-rex"]` (the provider is named `rox-rex`, not `rex`), `ROX_HOOKS`, the
reply fields `session` and `endpoint`, the `open-shell` event, and a host-transport interface in rox
(`docs/rox/remote-hosts.md#host-transports`) for the authority to implement. See `docs/rox/rex-hook.md`.
Without `REX_SESSION`, `rux agent spawn` runs the sub-agent headless and `rux agent read` prints its log.
