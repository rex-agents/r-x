---
sidebar:
  order: 3
---

# Remote hosts

A sub-agent's guest doesn't have to run on the machine where you started rux: it can run on one of your Rex remote hosts.

## Remote hosts

Every model loop runs where rux runs; only tool calls go to the guests. The lead can start a
sub-agent's guest on one of your Rex remote hosts (`spawn_agent` with `host`; `list_hosts` lists them),
which rox puts on the tailnet so rux reaches it with `rox exec`. An existing guest on another host works
the same way. Run rux itself on a remote host (in a Rex pane there) to use that host's rix.

With [`rux agent spawn`](rux-agent.md#spawn-flags) use `--host` instead. See [the lead's tools](lead-and-sub-agents.md#the-leads-tools) for the `spawn_agent` parameters.
