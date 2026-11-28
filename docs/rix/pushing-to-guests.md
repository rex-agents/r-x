---
sidebar:
  order: 6
---

# Pushing to running guests

New guests get credentials and agent setup from rox automatically. `rix apply` sends changes in the vault, such
as a rotated token, to guests that are already running.

```sh
rix apply rv-web-3f2a9c1b7d4e
rix apply --all                   # every running guest that has an agent CLI
```

`apply` reads the guest with `rox inspect`, sets its [Git identity](#git-identity), pushes [credentials](credentials.md) for the agents it has, then
[syncs agent setup](agent-setup-sync.md), and prints what it did and any warnings. Name a guest or pass `--all`,
not both. With `--all`, a failing guest is reported and the others still run; the command then exits non-zero.
A guest with no agent CLIs gets only the Git identity. New shells in the guest get the credentials immediately; agents started
by `rex run` get them after `rox stop NAME` and `rox start NAME`.

## What a credential push does

| Action | Effect |
| --- | --- |
| env | rewrites `~/.rox/agents.env` (mode 0600) with `export CLAUDE_CODE_OAUTH_TOKEN=…` or `ANTHROPIC_API_KEY=…` for installed agents; shells and the guest's Rex server read it |
| codex-auth | writes `~/.codex/auth.json` (0600) from your login file |
| codex-login | runs `codex login --with-api-key` |
| git-identity | sets the guest's global `user.name` and `user.email`, see below |
| codex-forget | removes Codex's login, only if rix signed it in (`~/.rox/rix-codex-login` marker, or rix's record); a login you made in the guest stays |

If the vault has no credential for an installed agent, rix warns that it starts signed out.

## Git identity

Every guest gets your Git identity as the guest user's global Git config (`git config --global user.name` and
`user.email`), so every agent and shell in it commits as you and GitHub attributes the commits to your account.
rix pushes it at provision (including `rox add` into a shared guest) and on `rix apply`, which is also how a
guest launched on another host with `--vault here` gets it. The identity comes from, in order:

1. your global Git config on this machine: `git config --global user.name` and `user.email` (both must be set);
2. else your GitHub identity from the [GitHub credential](github-access.md): your GitHub name (or login) and
   `ID+LOGIN@users.noreply.github.com`, the same identity rux uses;
3. else nothing: rix warns that commits in the guest will have no identity.

The values go to the guest as base64 on stdin (the `git-identity` action), never in argv. rix replaces only an
identity it set earlier (marked by `~/.rox/rix-git-identity`) or fills in an empty one; a name and email the
guest's own user set stay. rux sub-agents are told the identity is already set and not to change it, and rux's
own thread runners use your host Git config identity too.
