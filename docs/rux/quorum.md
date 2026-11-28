---
sidebar:
  order: 3
---

# Quorum: running a pack of authorities

A quorum (the **pack**) joins the [authorities](authority.md) on several machines. One of them is the **pack leader**: it sees every host's load and workloads and decides where new work runs. One is the **vault host**: its [rix](../rix/index.md) holds the credentials the whole pack uses.

Authorities talk over your tailnet, on the quorum listener (port 7710 by default, `/quorum/*`). The tailnet encrypts the link; every request also carries the member's own bearer credential.

## Create and join

On the machine that should lead (and host the vault):

```sh
rux quorum create
rux quorum invite
```

`invite` prints a one-time join code and the command to run on the other machine. A code works once and expires after 10 minutes.

```sh
rux quorum join HOST CODE
```

`HOST` is the leader's tailnet name or address (`:7710` is assumed). Joining hands the new authority its own credential, kept in `~/.rux/quorum.json` (mode 0600). `rux quorum leave` removes it.

`create` and `join` work with the authority stopped: they write `~/.rux/quorum.json`, and the authority picks it up on start. Everything else talks to the running authority.

If the URL other authorities should use isn't your tailnet address, pass `--url` or set `RUX_QUORUM_URL`.

## Status

`rux quorum status` shows the leader and term, the members and which of them vote, the vault host, how Git keys are served, and anything degraded.

## Failover

Members push a host report to the leader every 2 seconds and whenever something changes. The leader's view of them is what the TUI and web app show. If a host goes quiet it shows as stale, then unreachable.

| Members | Leader election |
| --- | --- |
| 1 | Just the leader. |
| 2 | **No automatic failover.** Two members can't tell a dead peer from a broken link, so neither takes over alone. Run `rux quorum promote HOST` (on `HOST` when the leader is down). |
| 3 or more | Every member votes ([Raft](https://raft.github.io/)). When the leader goes away the others elect a new one within a few seconds. `rux quorum promote HOST` hands leadership over on request. |

The small replicated config — members, succession, vault host, vault policy, project defaults — lives in a Raft log under `~/.rux/raft`, so every member has it.

While there is no leader, work that is already running keeps running. New placements wait on the requesting authority and retry with backoff (up to 30 seconds), so a failover only delays them.

Every placement carries the leader's term. An authority refuses a placement from a term older than the newest it has seen, so a leader that was cut off and comes back can't start work.

Pinning (`--on NAME`) always goes to that authority. Otherwise the leader picks the authority with the most free guest slots, then the lowest load, then the one that asked.

## The vault host

The vault host is a separate role. It starts as the pack's creator and **never moves on its own**, even when leadership does. Move it with `rux quorum vault-host HOST` (on the vault host or on the leader; `HOST` must be a member). This is also the way out when the vault host is gone for good: run it on the leader, naming a member that has its own rix (`rix auth claude`, `rix secret set ...`). The policy is part of the replicated config and carries over; the old host's secrets do not.

A restarted authority, leader or not, rejoins with its membership: it keeps its credential and the member list in `~/.rux/quorum.json` and its Raft log in `~/.rux/raft`, and follows whoever leads by then.

Members with `vault: leader` ask the vault host for each credential: the model keys behind the [model proxy](authority.md#credentials-the-model-proxy-and-the-vault), the GitHub token, and the Claude and Codex credentials rix stores. A member keeps what it fetched in memory only, for 10 minutes. Nothing is written to disk. Members with `vault: local` use their own rix and never ask.

### Policy

A member can read only what the vault host's policy allows. A **new member starts with the model providers' credentials and GitHub**: `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`, `OPENAI_API_KEY`, `claude`, `codex` and `github`. Every other secret (a `rix secret set NAME`, `ssh-agent`) is denied until you allow it, and `rux quorum policy deny` takes any of the defaults away:

```sh
rux quorum policy                                  # show
rux quorum policy allow laptop ANTHROPIC_API_KEY github
rux quorum policy deny laptop github
```

Edit the policy on the vault host. Names are `github`, `claude`, `codex`, the credential names `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN` and `OPENAI_API_KEY`, any secret from `rix secret set NAME`, and `ssh-agent` for the Git key relay. `*` allows everything.

### Audit

Every request to the vault host is appended to `~/.rux/quorum-audit.jsonl`: time, member, thread, secret name and result (`ok`, `denied`, `missing`, `error`). Secret values are never logged.

### Git keys

The vault host's rix can run each guest's `ssh-agent`; a member relays that socket over `/quorum/ssh-agent/GUEST` into the guest, so private keys never leave the vault host. This needs the vault host's authority to say which socket belongs to which guest. Until it does, members use their local rix for Git keys, and `rux quorum status` says so on the `git keys` line.

## Degraded states

| You see | What it means | What still works |
| --- | --- | --- |
| **no leader** | A failover is in progress, or a two-member pack lost its leader. | Running threads continue. New work queues, then fails after 30 seconds. Run `rux quorum promote HOST` in a two-member pack. |
| **vault unreachable** | The vault host is down or cut off. | Credentials already fetched work until their 10 minute TTL. Git over the relay stops. New credentialed work fails with a clear error. Members with `vault: local` aren't affected. |
| **stale / unreachable** (an authority) | The leader hasn't heard from it in several report intervals. | Its work keeps running if the host is fine. The leader stops placing work there. |
| **degraded pack view** | A member can't reach the leader, and shows only itself. | Local threads. |
| **authority isn't running** | You ran `rux quorum` without the daemon. | `create`, `join` and `status` still work. |
