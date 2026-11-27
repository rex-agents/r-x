# Managing credentials with rix

rix is the vault for the secrets and settings that agents and Git use inside [rox](../rox/index.md) guests. It
keeps, on your machine:

- the **Git SSH keys** guests clone and push with (held in a per-guest `ssh-agent`; the keys never leave the host),
- the **Claude Code and Codex credentials** guests sign in with,
- the **agent setup** you want guests to share: skills, instructions, remote MCP servers and settings,
- a **GitHub token** and **named secrets** for other tools on this machine, such as [rux](../rux/index.md).

rix installs nothing and never opens SSH. It reaches a guest only through `rox exec`, reads guests with
`rox inspect --json` and `rox ls --json`, and sends every secret on the stdin of `rox exec`, never in argv,
an image or a host process's environment. rox calls rix at fixed points in a guest's life
([the contract](rox-contract.md)), so a normal `rox up` needs no rix command at all once the vault is set up.
rox works without rix; without it guests simply start signed out and with no Git keys.

## Install

rix ships in the same release archive as rox and rux (`brew install rex-agents/tap/r-x`, or from the releases
page). From a checkout:

```sh
make install          # rox, rix and rux into ~/.local/bin
go build ./cmd/rix    # just rix
```

rix needs `rox` to talk to guests (the `ROX_BIN` environment variable, else `rox` on `PATH`), and `ssh-agent` and
`ssh-add` for Git keys. `gh` is used only as a fallback source of a GitHub token. `rix self-update` and the
automatic update are described in [updates and migration](updates-and-migration.md).

## Quick start

```sh
rix key add ~/.ssh/id_ed25519       # optional: otherwise guests get ssh's default keys from ~/.ssh
claude setup-token                  # makes a Claude subscription token (or use an Anthropic API key)
rix auth claude                     # paste it; stored 0600
rix auth codex                      # use ~/.codex/auth.json, or paste an OpenAI API key
rix sync show                       # what guests will get from this machine's agent setup
rox up --agents claude,codex        # rox calls rix: Git agent, credentials, synced setup
rix status                          # each guest's sign-in, Git agent and last sync
```

Credentials are pushed only into guests that have the agent, which projects choose with `"agents"` in
`.rox.json` (see the [rox docs](../rox/index.md)). Changes to the vault reach guests that are already running
with [`rix apply`](pushing-to-guests.md).

## Commands

`rix --help` lists them. The global flag `-v, --verbose` shows the output of the tools rix runs (also
`RIX_VERBOSE=1`). `rix --version` prints the version.

| Command | What it does |
| --- | --- |
| [`rix key add\|remove\|ls`](git-keys.md) | choose the Git SSH keys guests use |
| [`rix known-hosts set\|clear`](git-keys.md#known-hosts) | pin Git server host keys |
| [`rix auth claude\|codex\|github`](credentials.md) | store a credential |
| [`rix sync set\|show`](agent-setup-sync.md) | choose what of this machine's agent setup guests get |
| [`rix secret set\|ls\|rm\|import\|get`](named-secrets.md) | named secrets for rux actions |
| [`rix credential github`](github-access.md) | print the GitHub token, for tools |
| [`rix apply NAME\|--all`](pushing-to-guests.md) | push credentials and agent setup into running guests |
| [`rix status [NAME]`](status.md) | each guest's credentials, Git agent and last sync |
| [`rix git-agent start\|stop\|sock NAME`](git-agent.md) | manage a guest's Git agent |
| [`rix config`](configuration.md) | open the config in `VISUAL` or `EDITOR` |
| [`rix migrate`](updates-and-migration.md#migrating-from-python-rox) | move keys and credentials from the Python rox config |
| [`rix self-update [--check]`](updates-and-migration.md#updates) | install the latest release, or only say whether one is out |
| `rix hook EVENT NAME` | hidden: the [entry point rox calls](rox-contract.md) |
| `rix completion`, `rix help` | shell completion and help |

## In this section

| Page | Covers |
| --- | --- |
| [Git keys](git-keys.md) | Git SSH keys and pinned known hosts |
| [Credentials](credentials.md) | Claude Code and Codex sign-in |
| [Agent setup sync](agent-setup-sync.md) | copying skills, instructions, MCP servers and settings into guests |
| [Named secrets](named-secrets.md) | secrets for rux actions |
| [GitHub access](github-access.md) | the GitHub token served to tools |
| [Pushing to running guests](pushing-to-guests.md) | `rix apply` |
| [Status](status.md) | `rix status` |
| [The Git agent](git-agent.md) | per-guest `ssh-agent` |
| [Configuration](configuration.md) | files, folders and config keys |
| [The rox contract](rox-contract.md) | how rox calls rix |
| [Security model](security-model.md) | what stays on your machine and what goes into guests |
| [Updates and migration](updates-and-migration.md) | self-update, migrating from Python rox |
| [Troubleshooting](troubleshooting.md) | common messages and fixes |
