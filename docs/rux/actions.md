---
sidebar:
  order: 11
---

# Actions

Actions are jobs that run on the rux host and that agents in [`rux serve`](serve.md) guests can trigger: CI/CD on your own machine that your agents drive.

## Actions

Actions are jobs that run on the rux host (your Mac) and that agents in guests can trigger: building a Mac app, publishing to itch.io, anything that needs the Mac's tooling or secrets the guests mustn't see. Think of it as CI/CD on your own machine that your agents drive.

An action is a folder `~/.rux/actions/NAME/` with a `tool.json` and a script. `examples/rux-actions/` in this repo has examples (`cp -R examples/rux-actions/itch-publish ~/.rux/actions/`).

```json
{
  "description": "what it does, for the agent",
  "args": [{"name": "source", "type": "file", "description": "..."}],
  "settings": [{"name": "target", "description": "set per project in the app"}],
  "secrets": ["BUTLER_API_KEY"],
  "approval": "ask",
  "timeout": "30m",
  "run": "./run.sh"
}
```

- Actions are off until you turn them on in a project's settings (⋯ → settings → actions), where you also fill in their settings.
- In a guest, `rux action` lists the enabled actions and `rux action NAME --source . ...` runs one. `file` arguments (a file or folder) are uploaded to the host. For a git checkout, only the files git keeps are sent.
- `approval`: `ask` (the default) shows a card in the thread with Run it, Always allow here, and Deny. `every-run` always asks, and `none` never asks.
- Secrets come from rix: `rix secret set NAME` stores one, and `rix secret import ~/.rux/secrets.env` moves an old secrets file in (and deletes it). Only the secrets an action lists are fetched and passed to it, and their values are scrubbed from its output.
- The script runs in the action's folder with `RUX_ARG_<NAME>`, `RUX_SETTING_<NAME>`, `RUX_GIT_BRANCH`, `RUX_GIT_COMMIT`, `RUX_PROJECT`, `RUX_REPO` and `RUX_BIN` (the rux binary), plus these paths:
  - `RUX_OUT`: files written here are shared into the thread.
  - `RUX_RESULT`: a short summary written here goes back to the agent.
  - `RUX_STATE`: a folder that persists between runs.
- Output streams into the thread. A run that's waiting for approval or takes longer than `--wait` (seconds; default 90) reports back to the agent as a `[rux]` message when it ends.
- Actions run as you. Anything that runs code from a guest, like a build, must go through `rux sandbox DIR -- CMD`. That's a macOS sandbox with no network, no Apple Events or system services, and nothing in your home folder readable or writable except DIR.

## itch-publish

`itch-publish` builds the game on the Mac with the project's build command (default `make dist`, sandboxed) and pushes the output (default `build/itch`) with butler. Each branch goes to its own channel (`mac-BRANCH`), and the main branch goes to `mac`. It can also push a finished build with `--build`. Set `target` (`user/game`) in the project's settings, and store `BUTLER_API_KEY` with `rix secret set BUTLER_API_KEY`; `butler login` saves one in `~/Library/Application Support/itch/butler_creds`.
