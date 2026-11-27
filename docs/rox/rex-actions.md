---
sidebar:
  order: 14
---

# Rex actions

Every rox and rix command can be a Rex action, so you can run it from the Rex app. This is part of
[rox-rex](rex-hook.md); rox doesn't need it.

`rox-rex install` writes `~/.config/rex/rox.lua` (or beside `$REX_CONFIG`) and adds a
`require("rox")` line to Rex's `init.lua`, then reloads Rex (`--no-reload` skips that). Every rox and
rix command becomes a Rex action named `PROGRAM_COMMAND`, for example `rox_up`, `rox_ls`,
`rox_funnel_add`, `rix_status`. See the README for examples. Updates of rox, rix and rox-rex rewrite
installed actions.

## rox-rex install

```
rox-rex install [--no-reload]
rox-rex uninstall
rox-rex lua
```

- `install` sets the actions up (as above).
- `lua` prints the module.
- `uninstall` removes it.

`rox rex install|uninstall|lua` still works as a hidden command that forwards to `rox-rex`.
