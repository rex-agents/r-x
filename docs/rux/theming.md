---
sidebar:
  order: 13
---

# Settings and themes

Your authority has a name, a mascot and a theme. You change all three from **settings**: the
`⚙ settings` link in the web app's sidebar (`#/settings`) or the **SETTINGS** tab of the `rux` TUI
(`3`, or `tab` from MONITOR). Every change goes through the authority, so every open screen updates
at once: the web app on your phone, the TUI on your Mac, and anything else attached.

## The theme

A theme is a **preset** plus optional **overrides** for single colors. The presets:

| Preset | Look |
| --- | --- |
| `rux` | quiet greys with a pink accent (the default) |
| `matrix` | black and phosphor green |
| `ember` | warm dark browns with orange |
| `ocean` | deep blue with cyan |
| `paper` | light: warm white with magenta |

The colors a theme sets are its **tokens**:

| Token | Colors |
| --- | --- |
| `bg` | the page background |
| `panel` | cards, the sidebar and the composer |
| `raise` | menus and raised controls (and the TUI's key chips) |
| `line` | borders and rules |
| `text` | body text |
| `bright` | headings and emphasis |
| `muted` | secondary text |
| `faint` | hints and timestamps |
| `accent` | links, selection and primary actions |
| `accent2` | the second accent (the TUI's violet) |
| `ok` | working and success |
| `warn` | starting and warnings |
| `err` | failures and errors |
| `ink` | text on accent-colored backgrounds |

The web app uses every token. The TUI takes the text colors (`text`, `muted`, `faint`, the accents
and the status colors) and keeps your terminal's own background. With a light preset it keeps its
built-in greys for text, which a dark terminal can show, and takes only the accents.

In the web app, pick a preset card (hovering one previews it), or change a token with its color
picker; changes save as you pick, and a token that differs from the preset is marked `*`, with a
reset button. In the TUI, `enter` on a preset uses it, **Colors** takes `TOKEN=#hex` pairs, and
**Reset** goes back to the default.

### From the command line, and for agents

```sh
rux theme                          # the current theme, token by token
rux theme presets                  # the built-in themes
rux theme use matrix               # switch preset (drops overrides)
rux theme use ember accent=#ff5fa2 # a preset with an override
rux theme set accent=#ff8a3d ok=   # override tokens; TOKEN= drops one
rux theme reset                    # the default theme
rux theme --json                   # GET /api/theme
```

`rux theme` works on the authority's host and from inside a thread's guest, so you can ask any
agent to "make it look like a terminal from the 80s" and it can. Inside a guest, `rux theme`,
`rux authority rename` and `rux authority mascot` go through the runner listener's `/settings/…`
with the thread's own token, which reaches only these settings (see below). Thread agents are told
about the commands and to use them only when you ask.

## Name and mascot

**Name**: rename the authority; your agents call themselves this. An authority that's in a pack keeps
its name (leave the pack, rename it, join again). **Mascot**: describe what your agent looks like and
the authority's model draws it; keep the preview, draw again, or go back to the T-rex.

The same from the command line: `rux authority rename NAME`, `rux authority mascot "DESCRIPTION"`,
`rux authority mascot --reset`.

## API

| Endpoint | Does |
| --- | --- |
| `GET /api/theme` | `authapi.ThemeInfo`: `preset`, `colors` (the overrides), `default`, `resolved` (every token's color), `tokens` and `presets` |
| `PUT /api/theme` | `authapi.ThemeUpdate` `{"preset", "colors"}`: a `preset` switches and drops overrides unless `colors` comes too; `colors` sets tokens (`#rrggbb` or `#rgb`, an empty value drops one). 400 says what's wrong. |
| `DELETE /api/theme` | back to the default theme |

The theme is saved in `~/.rux/theme.json` (no file means the default). Each change is published on the
pack feed (`GET /api/pack/stream`) as a `theme` event with the new `ThemeInfo`, and each rename or
mascot change as a `persona` event with the new `Persona`. Clients that don't hold the feed open (the
web app outside PACK and MONITOR) pick a change up on their next poll, within ten seconds.

Agents in threads reach `GET|PUT|DELETE /settings/theme`, `GET /settings/persona`,
`PUT /settings/persona/name` and `POST|PUT|DELETE /settings/persona/mascot` on the runner listener,
with `?thread=ID` and `Authorization: Bearer <thread token>`; anything else under `/settings` is 403.
