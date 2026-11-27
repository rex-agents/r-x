---
sidebar:
  order: 4
---

# Named secrets

Named secrets are for tools on this machine, mainly [rux](../rux/index.md) actions that list secrets in their
`tool.json`. They are stored in `secrets.json` beside the [config](configuration.md) (mode `0600`, locked while
updated); guests never see them.

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
