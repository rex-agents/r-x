---
sidebar:
  order: 1
---

# Git keys

rix chooses which Git SSH keys guests clone and push with, and which server host keys they trust. The keys stay
on your machine, held by the guest's [Git agent](git-agent.md).

## Keys

```sh
rix key add ~/.ssh/id_ed25519      # the file must exist; added once, paths are made absolute
rix key remove ~/.ssh/id_ed25519   # idempotent: "isn't a Git SSH key" if it wasn't listed
rix key ls                         # name and path of each key
```

Keys are stored as `git_keys` in the [config](configuration.md). With **no** `git_keys` setting, guests get ssh's
default identities that exist in `~/.ssh`: `id_rsa`, `id_ecdsa`, `id_ecdsa_sk`, `id_ed25519`, `id_ed25519_sk`
(`rix key ls` says so). Removing the last key leaves an empty list, which means *no* keys, not the defaults.
A launch can override both with `rox up --git-key PATH`.

## Known hosts

```sh
rix known-hosts set ~/.ssh/pinned_known_hosts   # file must exist
rix known-hosts clear                           # back to this machine's ~/.ssh/known_hosts entries
```

`known-hosts set` stores `git_known_hosts`; its contents are returned to rox for the clone (`rox up
--git-known-hosts PATH` overrides it per launch). This only matters for SSH remotes; `https://` URLs get no Git
agent and no known-hosts.
