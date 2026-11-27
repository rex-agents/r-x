# Control workers on a Rex remote launcher

Use these commands from a controller that can reach the launcher in Rex. The launcher runs the VM or container and owns its state. The launcher session shows provisioning output from `rox up --host`. The guest session and worker blocks belong to the Rex server inside the guest.

## Establish both targets

Run `rex hosts list` to find the launcher label and `rex --autostart=false -S "$launcher" ls --json` to check it. Launch from the source checkout with `rox up BACKEND --host "$launcher" --cwd "$project_dir"`. Machine-specific paths in `--config`, `--git-key`, and `--git-known-hosts` are interpreted on the launcher. The controller's checkout supplies the clone URL and branch.

Save the launcher session ID from the `Provisioning` line. Save the workspace name, guest checkout path, and guest session ID after setup. If the launch printed a `Tailnet` line, the guest's Rex server is on the tailnet and rox registered it on the controller, so a controller-local `rex -S "$workspace"` reaches it directly. Remove it later with `rox down "$workspace" --host "$launcher"`. Use the guest commands from the main skill as written. Otherwise the guest's socket exists only on the launcher, so run guest commands from a Rex terminal on the launcher, or dispatch them there with an outer Rex command as below.

## Dispatch from the controller

The inner `rex run` starts the worker inside the guest. The outer `rex run` executes that CLI command on the launcher and keeps its JSON result in a tab for capture.

```bash
dispatch_status=0
dispatch=$(rex --autostart=false -S "$launcher" run -s "$launcher_session" \
  --label "Dispatch $task_label" --focus=false --keep-open --wait --json -- \
  rex --autostart=false -S "$workspace" run -s "$guest_session" \
  -c "$guest_checkout" --label "$task_label" --focus=false --keep-open --json -- "${runner_argv[@]}") || dispatch_status=$?
dispatch_block=$(printf '%s\n' "$dispatch" | jq -er '.block_ids[0]') || exit
rex --autostart=false -S "$launcher" capture -s "$launcher_session" -b "$dispatch_block" --trim --unwrap
```

If `dispatch_status` is zero, read `.block_ids[0]` from the inner command's JSON in the captured output. Use that guest worker ID for subsequent guest observations. The outer block ID identifies the launcher tab containing the dispatch output. If a command fails or times out, inspect its retained tab and the guest blocks before submitting another worker.

To inspect this worker from the controller, use the same outer pattern with an inner `rex -S "$workspace" block call -s "$guest_session" -b "$worker_block" process` or `rex -S "$workspace" capture -s "$guest_session" -b "$worker_block" --trim --unwrap`. For a wait, include `--for 30s` on the inner command. When batching observations, group them in a launcher terminal where both target IDs are explicit.

`rox shell`, `rox forward`, `rox reconnect`, `rox start`, `rox stop`, `rox down` and `rox add` find a guest on another Rex host by name, so you can run them from here; pass `-S HOST` when you already know the launcher. `rox ls --all` lists the guests on every host. Run `rox down NAME` only when the user asks. If the launcher session closed, select another designated session on that launcher. Guest access is independent of whether the local application displays the guest in its host list.
