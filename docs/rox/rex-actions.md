---
sidebar:
  order: 14
---

# Rex actions

Every rox and rix command can be a Rex action, so you can run it from the Rex app.

`rox rex install` writes `~/.config/rex/rox.lua` (or beside `$REX_CONFIG`) and adds a
`require("rox")` line to Rex's `init.lua`, then reloads Rex (`--no-reload` skips that). Every rox and
rix command becomes a Rex action named `PROGRAM_COMMAND`, for example `rox_up`, `rox_ls`,
`rox_funnel_add`, `rix_status`. See the README for examples. rox updates rewrite installed actions.

## rox rex

```
rox rex install [--no-reload] | uninstall | lua
```

- `install` sets the actions up (as above).
- `lua` prints the module.
- `uninstall` removes it.
