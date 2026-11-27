# rix

rix is the vault for the secrets and settings that agents and Git use inside [rox](../README.md) guests. It
keeps, on your machine:

- the **Git SSH keys** guests clone and push with (held in a per-guest `ssh-agent`; the keys never leave the host),
- the **Claude Code and Codex credentials** guests sign in with,
- the **agent setup** you want guests to share: skills, instructions, remote MCP servers and settings,
- a **GitHub token** and **named secrets** for other tools on this machine, such as [rux](rux.md).

rix installs nothing and never opens SSH. It reaches a guest only through `rox exec`, reads guests with
`rox inspect --json` and `rox ls --json`, and sends every secret on the stdin of `rox exec`, never in argv,
an image or a host process's environment. rox calls rix at fixed points in a guest's life
([the contract](#the-rox-contract)), so a normal `rox up` needs no rix command at all once the vault is set up.
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
automatic update are described [below](#updates).

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
`.rox.json` (see the rox docs). Changes to the vault reach guests that are already running with `rix apply`.

## Commands

`rix --help` lists them. The global flag `-v, --verbose` shows the output of the tools rix runs (also
`RIX_VERBOSE=1`). `rix --version` prints the version.

| Command | What it does |
| --- | --- |
| [`rix key add\|remove\|ls`](#git-keys) | choose the Git SSH keys guests use |
| [`rix known-hosts set\|clear`](#git-keys) | pin Git server host keys |
| [`rix auth claude\|codex\|github`](#credentials) | store a credential |
| [`rix sync set\|show`](#agent-setup-sync) | choose what of this machine's agent setup guests get |
| [`rix secret set\|ls\|rm\|import\|get`](#named-secrets) | named secrets for rux actions |
| [`rix credential github`](#github-access) | print the GitHub token, for tools |
| [`rix apply NAME\|--all`](#pushing-to-running-guests) | push credentials and agent setup into running guests |
| [`rix status [NAME]`](#status) | each guest's credentials, Git agent and last sync |
| [`rix git-agent start\|stop\|sock NAME`](#the-git-agent) | manage a guest's Git agent |
| [`rix config`](#configuration) | open the config in `VISUAL` or `EDITOR` |
| [`rix migrate`](#migrating-from-python-rox) | move keys and credentials from the Python rox config |
| `rix self-update [--check]` | install the latest release, or only say whether one is out |
| `rix hook EVENT NAME` | hidden: the entry point rox calls |
| `rix completion`, `rix help` | shell completion and help |

### Git keys

```sh
rix key add ~/.ssh/id_ed25519      # the file must exist; added once, paths are made absolute
rix key remove ~/.ssh/id_ed25519   # idempotent: "isn't a Git SSH key" if it wasn't listed
rix key ls                         # name and path of each key
```

Keys are stored as `git_keys` in the [config](#configuration). With **no** `git_keys` setting, guests get ssh's
default identities that exist in `~/.ssh`: `id_rsa`, `id_ecdsa`, `id_ecdsa_sk`, `id_ed25519`, `id_ed25519_sk`
(`rix key ls` says so). Removing the last key unsets `git_keys`, so guests get those defaults again.
A launch can override both with `rox up --git-key PATH`.

```sh
rix known-hosts set ~/.ssh/pinned_known_hosts   # file must exist
rix known-hosts clear                           # back to this machine's ~/.ssh/known_hosts entries
```

`known-hosts set` stores `git_known_hosts`; its contents are returned to rox for the clone (`rox up
--git-known-hosts PATH` overrides it per launch). This only matters for SSH remotes; `https://` URLs get no Git
agent and no known-hosts.

### Credentials

`rix auth` stores what an agent signs in with and points the config at it. Secret files are written with mode
`0600`, in the same folder as the config, and are named by the config.

```sh
rix auth claude                  # prompts (no echo) for a token or API key
rix auth claude --off            # forget it; guests start Claude Code signed out
rix auth codex                   # use your Codex login file, or paste an OpenAI API key
rix auth codex --auth-file ~/other/auth.json
rix auth codex --api-key         # an OpenAI API key instead of a login file
rix auth codex --off
rix auth github                  # see "GitHub access"
```

**Claude Code.** Accepts a single token starting `sk-ant-`: a subscription token (`sk-ant-oat01-…`, made by
`claude setup-token`, for Claude Pro or Max) or an Anthropic API key (`sk-ant-api03-…`). At a terminal, with
`claude` installed, rix offers to run `claude setup-token` for you. An empty answer keeps guests signed out.
The token is saved as `claude-token` (setting `token_file`) or the key as `anthropic-api-key` (`api_key_file`).
In a guest they become `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY`.

**Codex.** By default rix copies your Codex login file, `~/.codex/auth.json` (or `$CODEX_HOME/auth.json`, or
`--auth-file`), into guests' `~/.codex/auth.json`. It is referenced in place, not copied into the vault, and
must be readable only by you (`chmod 600`). At a terminal rix asks before using it. With `--api-key`, or when
there is no login file, rix prompts for an OpenAI API key, saves it as `openai-api-key`, and signs the guest in
with `codex login --with-api-key`. If guests sign each other out because they share one ChatGPT login, use
`--api-key`.

Only one credential per agent is kept: running `rix auth AGENT` again replaces the old one, and the old secret
file rix wrote is deleted once the config no longer names it. Existing `sync` settings survive. A credential
file named in the config that is group- or world-readable is refused with a `chmod 600` hint.

### Agent setup sync

Besides credentials, rix can copy parts of this machine's own Claude Code and Codex setup into guests.

```sh
rix sync show                          # what each agent's guests would get now
rix sync set claude skills,instructions
rix sync set codex skills,instructions,mcp,settings
rix sync set claude none               # nothing
rix sync set claude skills --mcp linear,sentry   # only these remote MCP servers
```

The default, with no `sync` setting, is **skills and instructions**. `rix sync set` rewrites all four switches,
so list everything you want. Items:

| Item | Copies | From (this machine) | To (guest `~`) |
| --- | --- | --- | --- |
| `skills` | each skill folder holding a `SKILL.md` (not `.git`; links are followed). A skill named `rox` is skipped | Claude: `~/.claude/skills`; Codex: `~/.codex/skills` and `~/.agents/skills` | `.claude/skills`, `.codex/skills`, `.agents/skills` |
| `instructions` | `CLAUDE.md` / `AGENTS.md` | `~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md` | same paths |
| `mcp` | remote MCP servers only (Claude: `type` `http`/`sse` with a `url`; Codex: a `url` and no `command`) | `mcpServers` in `~/.claude.json`; `mcp_servers` in `~/.codex/config.toml` | `.claude.json`, `.codex/config.toml` |
| `settings` | settings, minus host-bound keys | `~/.claude/settings.json`; `~/.codex/config.toml` | `.claude/settings.json`, `.codex/config.toml` |

`CLAUDE_CONFIG_DIR` and `CODEX_HOME` relocate the source folders. `--mcp NAMES` (a comma list) replaces `mcp`
and copies only those servers; a named server that is missing or runs a local program is skipped with a warning
(install such servers in the guest image). Settings that name programs or paths on this machine or hold its
secrets stay behind: Claude's `hooks`, `statusLine`, `apiKeyHelper`, `awsAuthRefresh`, `awsCredentialExport`,
`otelHeadersHelper`, `env`, `enabledPlugins`, `extraKnownMarketplaces`; Codex's `notify`, `projects`,
`mcp_servers`, `hooks`, `plugins`, `marketplaces`, `desktop`. When it adds MCP servers, rix sets
`hasCompletedOnboarding` in the guest's `~/.claude.json` if it isn't set.

In the config, `agents.AGENT.sync` is an object: `{"skills": true, "instructions": true, "settings": false,
"mcp": false}`, where `mcp` is `true` (all remote servers), `false`, or a list of names.

**How a sync lands in the guest.** rix builds one tar archive and pipes it to `rix-guest.sh` through `rox exec`.
It is merged, not overwritten: keys and MCP servers rix put in `~/.claude.json` and `~/.codex/config.toml` last
time are removed first and the current ones added, so settings you made in the guest stay. A record,
`~/.rox/synced.json` in the guest, lists what rix synced, so what you stopped syncing is removed next time.
On the guest:

- One sync at a time: `~/.rox/sync-lock` is a symlink holding `PID:START` of its owner. A lock is taken over only
  if its owner is gone, never for its age. A second sync fails with "Another sync is running". If
  `~/.rox/sync-lock` is a folder, rix refuses and leaves it. If a stuck `~/.rox/sync-reap` blocks things, remove it.
- The archive is unpacked into `~/.rox/sync-work/stage` and checked (paths must be relative, inside
  `.claude`, `.codex` or `.agents`, without `..`; the archive must end with its completeness marker) before
  anything changes.
- Each path is then swapped in, with the old one kept in `~/.rox/sync-work/undo`, and a journal written first.
  Any failure, or a later sync after a crash, restores the originals. If it can't, the backups stay in
  `~/.rox/sync-work` and the error says so; the next sync tries again.
- Stale paths from the previous sync are removed only after the new setup is in place.

### Named secrets

For tools on this machine, mainly rux actions that list secrets in their `tool.json`. Stored in `secrets.json`
beside the config (mode `0600`, locked while updated); guests never see them.

```sh
rix secret set ITCH_API_KEY            # prompts without echo
echo "$KEY" | rix secret set NAME --stdin
rix secret ls [--json]                 # names only (never values)
rix secret rm NAME...
rix secret import ~/.rux/secrets.env [--keep]   # NAME=VALUE lines; deletes the file unless --keep
rix secret get NAME... [--json]        # prints values, one per line; fails if one is missing
```

Names are `[A-Za-z_][A-Za-z0-9_]*`. An empty value is refused (use `rm`). `import` accepts `export NAME=VALUE`,
skips blanks and `#` comments, and strips one pair of matching quotes. `get --json` prints
`{"values": {…}, "missing": […]}` and succeeds even when some are missing. A `secrets.json` that others can
read is refused.

### GitHub access

```sh
rix auth github          # prompts for a personal access token; stored as github-token (0600)
rix auth github --gh     # use gh's login on this machine; forgets any stored token
rix auth github --off    # tools get no GitHub access from rix
rix credential github          # prints the token
rix credential github --json   # {"username":"x-access-token","password":"…","source":"vault"|"gh"}
```

A blank answer to the prompt is the same as `--gh`. Without a stored token, `rix credential github` runs
`gh auth token --hostname github.com`. The token never goes into a guest from rix itself; rux fetches it
on demand (see [rux.md](rux.md)). Use a fine-grained token limited to the repositories agents work on.
`--api-key` and `--auth-file` are rejected for `github`, and `--gh` for the agents.

### Pushing to running guests

```sh
rix apply rv-web-3f2a9c1b7d4e
rix apply --all                   # every running guest that has an agent CLI
```

`apply` reads the guest with `rox inspect`, pushes credentials for the agents it has, then syncs agent setup,
and prints what it did and any warnings. Name a guest or pass `--all`, not both. With `--all`, a failing guest
is reported and the others still run; the command then exits non-zero. A guest with no agent CLIs is skipped.
New shells in the guest get the credentials immediately; agents started by `rex run` get them after
`rox stop NAME` and `rox start NAME`.

What a credential push does in the guest:

| Action | Effect |
| --- | --- |
| env | rewrites `~/.rox/agents.env` (mode 0600) with `export CLAUDE_CODE_OAUTH_TOKEN=…` or `ANTHROPIC_API_KEY=…` for installed agents; shells and the guest's Rex server read it |
| codex-auth | writes `~/.codex/auth.json` (0600) from your login file |
| codex-login | runs `codex login --with-api-key` |
| codex-forget | removes Codex's login, only if rix signed it in (`~/.rox/rix-codex-login` marker, or rix's record); a login you made in the guest stays |

If the vault has no credential for an installed agent, rix warns that it starts signed out.

### Status

```sh
rix status            # table: guest, agents (signed in/out), git agent, last sync
rix status NAME
rix status --json     # {"schema":1,"guests":[{name, agents, credentials, git_agent, git_keys, synced}]}
```

`git agent` is `none` (no keys recorded), `up` (the agent socket answers `ssh-add -l` with keys) or `down`.
Guests come from `rox ls --json --all-projects`.

### The Git agent

For each guest rix runs one `ssh-agent -D -a SOCKET` on your machine, and loads only that guest's keys with
`ssh-add`. rox forwards the socket into the guest. The keys, and their passphrases, stay on this machine.

```sh
rix git-agent start NAME   # start it and load keys, asking for passphrases in this terminal
rix git-agent stop NAME
rix git-agent sock NAME    # print the socket path
```

The socket is `$XDG_RUNTIME_DIR/rix/ID/agent.sock`, or `/tmp/rix-UID/ID/agent.sock` when that variable is unset
(as on macOS), where `ID` is the last part of the guest name. Directories are mode 0700; keep paths short, since
unix socket paths have a length limit. Hooks run without a terminal, so an encrypted key that needs a
passphrase can't be loaded then: run `rix git-agent start NAME` in a terminal, then `rox reconnect NAME` to
forward it. `git-agent start` on a guest rix doesn't know yet uses the configured keys (or ssh's defaults) and
marks them to be kept for the guest's life. rix only signals an agent it can prove it started: the recorded PID
must still have the recorded start time and the exact command line `ssh-agent -D -a SOCKET`.

By default a guest keeps its Git agent for its whole life so agents can push. Set `"git_agent": false` in the
config, or use `rox up --no-git-agent`, to keep the keys for the clone only; rix then stops the agent when
provisioning finishes.

## Configuration

`rix config` opens the config in `VISUAL`, `EDITOR` or `vi` (creating `{}` if absent) and checks that it is
valid JSON afterwards. Files:

| Path | Holds |
| --- | --- |
| `$XDG_CONFIG_HOME/rix/config.json` (default `~/.config/rix/`) | settings; the folder is mode 0700 |
| same folder: `claude-token`, `anthropic-api-key`, `openai-api-key`, `github-token` | secrets saved by `rix auth` (0600) |
| same folder: `secrets.json` | named secrets (0600) |
| same folder: `*.lock` | lock files (`config.json.lock`, `secrets.json.lock`) taken while a file is rewritten; harmless to leave |
| `$XDG_STATE_HOME/rix/GUEST/state.json` (default `~/.local/state/rix/`) | per guest: keys held, whether to keep them, agent PID and start, agents signed in, last sync time and summary |
| `$XDG_RUNTIME_DIR/rix/ID/agent.sock` or `/tmp/rix-UID/ID/agent.sock` | a guest's Git agent socket |
| in the guest: `~/.rox/agents.env`, `~/.codex/auth.json`, `~/.rox/rix-codex-login`, `~/.rox/synced.json`, `~/.rox/sync-lock`, `~/.rox/sync-work/` | see above |

Config keys (unknown keys are preserved when rix rewrites the file):

```json
{
  "git_keys": ["~/.ssh/id_ed25519"],
  "git_known_hosts": "~/.ssh/pinned_known_hosts",
  "git_agent": true,
  "agents": {
    "claude": { "token_file": "~/.config/rix/claude-token",
                "sync": { "skills": true, "instructions": true, "settings": false, "mcp": ["linear"] } },
    "codex":  { "auth_file": "~/.codex/auth.json" }
  },
  "github": { "token_file": "~/.config/rix/github-token" },
  "updates": "auto"
}
```

- `agents.claude` takes one of `token_file` or `api_key_file`; `agents.codex` one of `auth_file` or
  `api_key_file`; both also `sync` and `version`. `version` (such as `1.2.3`) is the release rox installs when a project asks for the agent without pinning one; a version set in `.rox.json`, `.rox.local.json` or `--agents` wins, and unset means the latest release. Anything else, or two credentials
  for one agent, is an error. Credential files are read at push time, so rotating a token needs only `rix apply`.
- `github` is `{"token_file": PATH}` or `{"off": true}`; absent means gh's login.
- `updates`: `auto`, `notify` or `off`; the `RIX_UPDATES` environment variable overrides it.

## The rox contract

rox runs `rix hook EVENT NAME` (hidden, not for people) with the guest record on stdin, one JSON line (the output
of `rox inspect NAME --json`, `"schema": 1`), and reads one JSON line on stdout:
`{"schema":1,"agent_socket","forward","known_hosts","done":[…],"warnings":[…]}`. A schema mismatch is an error
that tells you to update both. rox's `launch` object in the record carries this launch's `--git-key`,
`--git-known-hosts`, `--no-git-agent`, the repository URL and the newly installed agents.

| Event | rix does |
| --- | --- |
| `pre-fetch` (NAME is `-`) | for an SSH URL, starts a throwaway agent in a temp dir, loads the keys without a terminal, replies with its socket and known hosts, and keeps it until rox closes stdin. If keys can't be loaded it warns and replies without a socket |
| `post-boot` | pushes credentials for the agents the guest has |
| `pre-clone` | for SSH URLs: starts the guest's Git agent, saves its keys in state, replies with `agent_socket`, `forward` and `known_hosts`. Warns if there are no keys. `https://` URLs do nothing |
| `post-provision` | pushes credentials if agents were newly installed, syncs agent setup (a failure is a warning), and stops the agent if the guest keeps keys only for the clone |
| `resume` | restarts the guest's agent if it died and replies with its socket, when keys are kept for life |
| `down` | stops the agent, deletes the guest's state and runtime folder |

Errors in `pre-clone` fail the launch; the rest are reported as warnings by rox. rix finds rox through `ROX_BIN`
(set by rox when it calls rix) or `PATH`. `rox hooks` shows the active hook provider; `"hooks": "off"` in rox's
config turns rix off for rox. On a remote host (`rox up --host`) the host's rix is used by default; with
`--vault here` this machine's rix pushes credentials over the guest's tailnet Rex server (rox calls the hidden
`rix apply NAME --quiet`).

## Security model

**Stays on your machine:** Git private keys and their passphrases (only the agent socket is forwarded; guests can
sign with the keys while the forward is up but cannot read them); named secrets; the GitHub token (served to
tools on stdout on request); every config and secret file, all mode 0600 in 0700 folders, and rix refuses
credential files that others can read.

**Goes into guests:** the Claude Code token or API key (`~/.rox/agents.env`, 0600), Codex's login file or API
key (`~/.codex/auth.json`, 0600), and the synced skills, instructions, MCP server definitions and settings.
These are only sent to guests that have that agent installed. Remote MCP server definitions may include
headers or tokens, so sync them only to guests you trust; a guest is as trusted as your own shell. Treat anyone
who can run commands in a guest as holding those credentials. Prefer limited tokens (API keys with a spend limit,
fine-grained GitHub tokens).

Secrets reach guests only on stdin of `rox exec`, so they do not show in process lists.

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

## Troubleshooting

| Message | Cause and fix |
| --- | --- |
| `Install rox: rix reaches guests only through rox exec` | `rox` not on `PATH`; set `ROX_BIN` |
| `Could not load the Git keys for NAME … run rix git-agent start NAME in a terminal` | a key needs a passphrase; run that command, then `rox reconnect NAME` |
| `No Git SSH key is configured and ~/.ssh has none…` | `rix key add PATH` |
| `X holds a credential for …, so only you may read it` | `chmod 600` the file |
| `Expected a single token with no spaces` / `That doesn't look like a Claude Code token` | paste only the `sk-ant-…` value |
| `Claude Code has no credential in rix, so it starts signed out` | `rix auth claude`, then `rix apply NAME` |
| `Another sync is running` | wait; if a guest sync died, the next sync recovers a dead lock by itself |
| `~/.rox/sync-lock is a folder` | move that folder away in the guest |
| `GitHub access is off` / `No GitHub token` | `rix auth github` (or `--gh`, then `gh auth login`) |
| `rox speaks guest record schema N and this rix speaks M` | update rox and rix together |
| agent socket path too long | set a shorter `XDG_RUNTIME_DIR` (or `TMPDIR` for tests) |
| `git agent down` in `rix status` | `rix git-agent start NAME`, then `rox reconnect NAME` (rox's `resume` also restarts it) |
