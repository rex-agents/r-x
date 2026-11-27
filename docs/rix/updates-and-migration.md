---
sidebar:
  order: 12
---

# Updates and migration

Keeping rix current, and moving over from the Python version of rox.

## Updates

rix updates itself like rox: `rix self-update` installs the latest release, `rix self-update --check` only
reports. Unless set to `off` or `notify` (config `updates`, or `RIX_UPDATES`), a released rix updates itself
before running other commands, at most every six hours, and then refreshes rox's Rex actions. Development builds
don't update.

## Migrating from Python rox

```sh
rix migrate --dry-run   # say what would move
rix migrate [--rox-config PATH]   # default ~/.config/rox/config.json
```

Moves `git_keys`, `git_known_hosts`, `git_agent`, each agent's credential setting and `sync`, and the secret files
rox saved beside its config, into rix, without overwriting keys rix already has. It removes the moved settings from
rox's config, takes over the Git keys recorded for running guests (in rox's state), and prints a `.rox.local.json`
snippet to keep the agents rox used to install in every guest.
