---
sidebar:
  order: 9
---

# Rich UI, diffs and dashboards

In a [`rux serve`](serve.md) thread, agents can show UI instead of text: forms, tables, charts and diffs.

## Rich UI, diffs and dashboards

Agents can show UI in their thread, not just text: `rux ui spec.json` renders a
[json-render](https://json-render.dev) style spec with the app's own components (`web/ui.js`):
layout, Stat tiles, Meter, Table, List, Timeline, Chart (line, area, bar, stacked bar, sparkline),
Graph, Code, Diff and Image, plus inputs (Choice, Select, TextInput, Toggle, Button).

- `rux ui --ask spec.json` asks a question. Your answer (the form values and which button) goes back
  to the agent as a message, so you can answer whenever.
- `rux diff [git diff args]` shows a diff with a file browser, unified or split view, and full
  screen. By default it shows uncommitted changes, including new files.
- `rux ui --catalog` prints the format and components; agents read it before first use.
- [Managers](projects-and-managers.md) have a `thread_stats` tool and are told to answer "how's it going?" with a dashboard.
