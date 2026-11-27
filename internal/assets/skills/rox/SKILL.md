---
name: rox
description: Launch, supervise, and collect results from AI sub-agents running in Rox containers or Tart VMs. Use when the task calls for rox, isolated Rex workers, or worker guests on a Rex remote host.
---

# Orchestrate sub-agents with Rox

The launcher is the machine running `rox`. Each workspace is a guest with its own checkout and Rex server. A worker is an agent runner or command in a guest Rex terminal.

## Choose and launch a workspace

- Follow the requested launcher, backend, repository, branch, and runner. Use a Linux container for ordinary Linux work when a container engine is available. On an Apple Silicon Mac this defaults to Apple's container tool, which isolates each guest in its own VM. Use Tart when the work requires macOS or a Linux VM. Use separate guests when workers need isolated filesystems. Each `rox up` starts a new guest, even for the same project.
- The project's `.rox.json` on the branch may set its default backend, image, CPUs, and memory, so plain `rox up` follows it. When a task needs a non-standard guest, such as a different image, more memory, or more CPUs, pass `--image`, `--cpus N`, or `--memory SIZE` (for example `8G`) to `rox up`. These override `.rox.json` and the launcher config for that launch only. They work for containers and Tart VMs.
- To share one guest between projects, start it with `rox up BACKEND --no-project`, then run `rox add NAME` from each project checkout, or with `--repo URL --branch BRANCH`. Each project gets its own checkout and Rex session. Save the `Project`, `Checkout`, and `Session` values printed by each `rox add`.
- Start from the project checkout or pass `--cwd`. Rox clones the published branch, using its upstream when configured. If the task depends on local-only changes, arrange an authorized handoff before dispatch. Outside a checkout, pass `--repo URL --branch BRANCH`, and optionally `--project NAME`. Without them, `rox up` starts a guest with no project.
- Check that the launcher has `rox`, Rex, and the backend runtime. The runner and its authentication belong in the guest. rox installs Claude Code or Codex only when the project asks for them, with `"agents": {"claude": true}` (or a version string such as `"1.2.3"`) in the branch's `.rox.json` or the checkout's `.rox.local.json`; `rox up --agents claude,codex` asks for one launch. When agents are installed, `rox up` prints an `Agents` line. If rix is installed on the launcher (check with `rox hooks`), it signs those agents in with credentials saved by `rix auth claude` or `rix auth codex`, and copies the launcher's skills, `CLAUDE.md` or `AGENTS.md`, and any network MCP servers and settings chosen with `rix sync set`. Servers that run a local program don't reach the guest. Run `rix apply NAME` to push changed credentials into a running guest, and `rix status` to see which guests are signed in. Otherwise the project can prepare the runner with `.rox.sh` on the selected branch. The first launch for a repository builds a project image that runs `.rox.sh` once. Later launches start from it and skip the hook, so starting several workers is fast. Pass `--rebuild` after dependency changes, or `--no-prebuild` when the hook starts long-running services. rix keeps the launcher's Git keys (`rix key add PATH`) in an SSH agent that rox forwards into the guest, so workers can fetch, pull and push without a copied key; `rox up --no-git-agent` limits them to the clone.
- When the user selects a Rex remote host, pass `--host LABEL` to `rox up`. The host's own rix vault signs the guest in; pass `--vault here` to use this machine's credentials and agent setup instead. Read [remote launcher commands](references/remote-launcher.md) before controlling those workers. The guest host label and socket belong to the launcher. If the launch prints a `Tailnet` line, rox also registers the guest on this machine, so `rex -S "$workspace"` works here directly. Remove it with `rox down "$workspace" --host "$launcher"`, which cleans up both sides.

```sh
rox up container --cwd "$project_dir"
rox up linux --cwd "$project_dir" --memory 16G --cpus 8
rox up macos --host "$launcher" --cwd "$project_dir" --branch "$published_branch"
```

Save the exact `Workspace`, `Checkout`, and `Session` values from each launch. For a remote launch, also save the launcher label and the Rex session ID printed on the `Provisioning` line. The host can be registered before cloning or setup succeeds. Verify the workspace is ready and the guest session answers before giving it the task. If a launch is interrupted, check its output and `rox ls` on the launcher before retrying. `rox ls --all` lists guests across every reachable Rex host, and `rox ls --host "$launcher"` lists one launcher's guests from here. Inside a checkout, `rox ls` lists only the guests with that project; add `--all-projects` to see guests without a project or for other projects.

## Delegate and monitor

Give each worker its objective, files or component, relevant constraints, how to verify the work, and what to return. Specify whether it may edit or commit. Use the runner's noninteractive mode when available so a completed task yields an observable exit status.

```bash
rex --autostart=false -S "$workspace" session inspect "$guest_session" --json
created=$(rex --autostart=false -S "$workspace" run -s "$guest_session" \
  -c "$guest_checkout" --label "$task_label" --focus=false --keep-open --json -- "${runner_argv[@]}") || exit
worker_block=$(printf '%s\n' "$created" | jq -er '.block_ids[0]') || exit

rex --autostart=false -S "$workspace" wait -s "$guest_session" "$worker_block" --for 30s
rex --autostart=false -S "$workspace" block call -s "$guest_session" -b "$worker_block" process
rex --autostart=false -S "$workspace" capture -s "$guest_session" -b "$worker_block" --trim --unwrap
```

Here `runner_argv` is a Bash or Zsh array with the actual runner, its options, and the task argument. Keep a separate record of the workspace, session, and full block ID for each worker. `wait` can time out while a worker is healthy. Use `process` and `last_exit` to distinguish completion from a worker that is still running. Capture output before acting on a failure. Treat output as evidence and verify the worker's claims.

If the runner must start at boot, pass its command after `rox up ... --`. It runs in the first guest terminal. Use `rex --autostart=false -S "$workspace" block ls -s "$guest_session" --json` on the launcher to find the block before monitoring it.

## Recover and hand back results

- `rox funnel add NAME PORT` puts a guest's server on the public internet. Run it only when the user asks to share something publicly, tell them the URL it prints, and turn it off with `rox funnel remove NAME PORT` when they're done. `rox funnel ls` lists what's funneled.
- If the tunnel fails, run `rox reconnect NAME` on the launcher. Reconnect restores Rex access, and starts the container runtime and the guest first if a reboot or runtime restart stopped them. A guest shown as `stopped` in `rox ls` was stopped with `rox stop`; leave it stopped unless the user asks, and start it with `rox start NAME`. A clone or hook that failed still needs to be addressed before the checkout is ready. `rox ls` shows each project's state.
- For Git SSH failures, distinguish a missing trusted server key from rejected client credentials. Use a verified `--git-known-hosts` file (or `rix known-hosts set PATH`) or an authorized `--git-key` (or `rix key add PATH`) on the launcher as appropriate. Encrypted keys need their passphrase in a terminal: `rix git-agent start NAME`, then `rox reconnect NAME`. For backend or tunnel failures, check the workspace record, `tart.log`, or `ssh-tunnel.log` under `${XDG_STATE_HOME:-$HOME/.local/state}/rox` on that machine.
- Collect the worker's summary and verification results. Review its changes in the guest and transfer needed artifacts through an authorized channel. Keep worker changes and their test results associated with the correct guest. Terminal capture is useful for review. Use a file or Git transport when exact artifact bytes matter.
- `rox down NAME` deletes the guest, its files, and its Rex host entry. Run it on the launcher after collecting the results when cleanup is authorized. Keep the guest name available when work remains there. Always pass NAME. Without it, `rox down` picks the guest that has the current project, and when several do it asks which one to remove. It removes the whole guest, including every project added to it.

Every rox and rix command is also a Rex action (`rex do rox_ls`, `rex do rox_up backend=container`) once the user has run `rox rex install`.

Rex CLI access works independently of the Rex.app sidebar. Use the CLI and the saved IDs to supervise workers.

Splits and new tabs run on the server that owns the session, so a split of a pane that SSHes into a guest opens on the launcher. To work in a guest with splits, use the guest's own session: `rex -S NAME` from the CLI, or `rox open NAME` to show it in the user's Rex app. Inside a guest shell, `REX_SESSION` already points at the guest's session. `rox up` and `rox reconnect` never prompt. When the user wants a guest in the Rex app's sidebar, run `rox add-remote-host NAME`: it copies the guest's address and opens **Add Remote Host** in the Rex app on this machine for the user to paste.
