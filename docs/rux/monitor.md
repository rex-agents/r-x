---
sidebar:
  order: 5
---

# rux monitor

`rux monitor` is a live dashboard of the sub-agents started with [`rux agent`](rux-agent.md), the rox guests and the host. [`rux claude`](rux-claude.md) opens it for you.

## rux monitor

The monitor follows every sub-agent in this Rex session, as Rex reports it: working, waiting for you, needs you, or done. It also shows the last lines of the selected agent's screen, every rox guest with its CPU and memory, and the host's load.

| Key | Does |
| --- | --- |
| `enter` | go to the agent's pane |
| `m` | type a line into the agent's pane |
| `x` | stop the agent: `y` removes its guest, `k` keeps it (a guest that was there before is always kept) |
| `o` | open a shell in the guest |
| `n` | go back to the lead |
| `tab` | move between agents and guests |
| `r` | refresh |
| `q` / `esc` | quit the monitor |

Move with `↑` `↓` (or `j` `k`). `rux agent ls --watch` is the plain-text version.
