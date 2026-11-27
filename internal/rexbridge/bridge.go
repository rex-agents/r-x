// Package rexbridge makes every rox and rix command a Rex action, so the Rex command
// palette, key bindings and rex do can drive them.
//
// It generates a Lua module from the commands' own definitions, so the actions never drift
// from the CLIs. Each command runs one of three ways:
//
//   - terminal: in a new terminal split beside the pane that asked, so rox's own progress
//     shows and any prompt, such as a secret, is typed there. Rex actions get 30 seconds,
//     so anything slow or interactive runs this way.
//   - json: with rex.exec, returning the command's JSON as the action's result.
//   - text: with rex.exec, returning what the command printed.
//
// Commands mark themselves with Annotate. Positional arguments become named action
// arguments; flags become typed optional ones, with dashes as underscores.
package rexbridge

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Annotation keys a command can set.
const (
	keyArgs    = "rex.args"    // positional args: "name,project?,ports+"
	keyMode    = "rex.mode"    // terminal, json or text
	keyPick    = "rex.pick"    // positional arg filled from a guest picker
	keyChoices = "rex.choices" // "arg=a|b|c;other=x|y"
	keySkip    = "rex.skip"    // leave the command out
	keyTitle   = "rex.title"   // palette title
	keyCustom  = "rex.custom"  // a hand-written action replaces the generated one
)

// Spec describes how a command becomes an action.
type Spec struct {
	// Args are the positional arguments, in order. "?" marks optional, "+" one or more.
	Args string
	// Mode is terminal (the default), json or text.
	Mode string
	// Pick names the positional argument a guest picker fills when it's missing.
	Pick string
	// Choices list the values of positional arguments, for a picker.
	Choices map[string][]string
	// Title is the palette title, such as "Start a New Guest…".
	Title string
	// Skip leaves the command out.
	Skip bool
	// Custom names a hand-written action in customActions that replaces the generated one.
	Custom string
}

// Annotate records how cmd becomes an action.
func Annotate(cmd *cobra.Command, s Spec) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	set := func(k, v string) {
		if v != "" {
			cmd.Annotations[k] = v
		}
	}
	set(keyArgs, s.Args)
	set(keyMode, s.Mode)
	set(keyPick, s.Pick)
	set(keyTitle, s.Title)
	set(keyCustom, s.Custom)
	if s.Skip {
		cmd.Annotations[keySkip] = "1"
	}
	var parts []string
	for _, k := range sortedKeys(s.Choices) {
		parts = append(parts, k+"="+strings.Join(s.Choices[k], "|"))
	}
	set(keyChoices, strings.Join(parts, ";"))
	return cmd
}

// Program is one CLI to bridge.
type Program struct {
	Name     string // rox or rix
	Bin      string // absolute path, or "" to look it up on PATH
	Category string // palette group
	Root     *cobra.Command
}

// Action is one generated action.
type Action struct {
	Name, Title, Description, Category, Mode, Pick, Custom string
	Program                                                string
	Path                                                   []string
	Positional                                             []Arg
	Flags                                                  []Arg
	Choices                                                map[string][]string
}

// Arg is a positional argument or a flag.
type Arg struct {
	Name     string // Lua name
	Flag     string // the flag, without dashes
	Type     string // string, integer, boolean or array
	Optional bool
	Many     bool
	Help     string
}

// Actions walks the programs' commands into actions.
func Actions(programs []Program) []Action {
	var out []Action
	for _, p := range programs {
		var walk func(c *cobra.Command, path []string)
		walk = func(c *cobra.Command, path []string) {
			for _, sub := range c.Commands() {
				if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" || sub.Annotations[keySkip] != "" {
					continue
				}
				subPath := append(append([]string{}, path...), sub.Name())
				if sub.Runnable() {
					out = append(out, action(p, sub, subPath))
				}
				walk(sub, subPath)
			}
		}
		walk(p.Root, nil)
	}
	return out
}

func luaName(s string) string { return strings.ReplaceAll(s, "-", "_") }

func action(p Program, c *cobra.Command, path []string) Action {
	a := Action{
		Name:        luaName(p.Name + "_" + strings.Join(path, "_")),
		Description: c.Short,
		Category:    p.Category,
		Mode:        firstNonEmpty(c.Annotations[keyMode], "terminal"),
		Pick:        c.Annotations[keyPick],
		Custom:      c.Annotations[keyCustom],
		Program:     p.Name,
		Path:        path,
		Choices:     map[string][]string{},
	}
	a.Title = c.Annotations[keyTitle]
	if a.Title == "" {
		a.Title = p.Name + " " + strings.Join(path, " ")
	}
	for _, part := range strings.Split(c.Annotations[keyArgs], ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		arg := Arg{Type: "string"}
		switch {
		case strings.HasSuffix(part, "?"):
			arg.Optional, part = true, strings.TrimSuffix(part, "?")
		case strings.HasSuffix(part, "+"):
			arg.Many, arg.Type, part = true, "array", strings.TrimSuffix(part, "+")
		case strings.HasSuffix(part, "*"):
			arg.Many, arg.Optional, arg.Type, part = true, true, "array", strings.TrimSuffix(part, "*")
		}
		arg.Name = luaName(part)
		a.Positional = append(a.Positional, arg)
	}
	for _, spec := range strings.Split(c.Annotations[keyChoices], ";") {
		if name, values, ok := strings.Cut(spec, "="); ok {
			a.Choices[luaName(name)] = strings.Split(values, "|")
		}
	}
	taken := map[string]bool{}
	for _, arg := range a.Positional {
		taken[arg.Name] = true
	}
	c.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" || f.Name == "verbose" || f.Name == "version" {
			return
		}
		arg := Arg{Name: luaName(f.Name), Flag: f.Name, Optional: true, Help: f.Usage}
		switch f.Value.Type() {
		case "bool":
			arg.Type = "boolean"
		case "int", "int64", "uint":
			arg.Type = "integer"
		case "stringArray", "stringSlice":
			arg.Type = "array"
		default:
			arg.Type = "string"
		}
		if taken[arg.Name] {
			arg.Name += "_flag"
		}
		a.Flags = append(a.Flags, arg)
	})
	return a
}

// Lua writes the module that defines the actions.
func Lua(programs []Program, actions []Action) string {
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("local BIN = {\n")
	for _, p := range programs {
		fmt.Fprintf(&b, "  %s = %s,\n", p.Name, luaString(firstNonEmpty(p.Bin, p.Name)))
	}
	fmt.Fprintf(&b, "  rex = %s,\n", luaString(firstNonEmpty(RexBin, "rex")))
	b.WriteString("}\n\n")
	b.WriteString(runtime)
	b.WriteString("\n-- The actions, one per command.\n")
	for _, a := range actions {
		b.WriteString(actionLua(a))
	}
	return b.String()
}

func actionLua(a Action) string {
	if body, ok := customActions[a.Custom]; ok {
		return body
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nrex.action{\n  name        = %s,\n  title       = %s,\n  description = %s,\n  category    = %s,\n  keywords    = %s,\n",
		luaString(a.Name), luaString(paletteTitle(a)), luaString(a.Description), luaString(a.Category),
		luaString(strings.Join(append([]string{a.Program}, a.Path...), " ")+" guest workspace"))
	b.WriteString("  args        = {\n")
	for _, arg := range a.Positional {
		optional := arg.Optional || (a.Pick == arg.Name) || len(a.Choices[arg.Name]) > 0
		fmt.Fprintf(&b, "    [%s] = %s,\n", luaString(arg.Name), luaString(arg.Type+map[bool]string{true: "?", false: ""}[optional]))
	}
	for _, arg := range a.Flags {
		fmt.Fprintf(&b, "    [%s] = %s,\n", luaString(arg.Name), luaString(arg.Type+"?"))
	}
	b.WriteString("    [\"cwd_dir\"] = \"string?\",\n  },\n")
	b.WriteString("  run = function(ctx, args)\n    return bridge(ctx, args, {\n")
	fmt.Fprintf(&b, "      program = %s,\n      path = %s,\n      mode = %s,\n      pick = %s,\n",
		luaString(a.Program), luaList(a.Path), luaString(a.Mode), luaString(a.Pick))
	b.WriteString("      positional = {\n")
	for _, arg := range a.Positional {
		fmt.Fprintf(&b, "        { name = %s, optional = %t, many = %t, choices = %s },\n",
			luaString(arg.Name), arg.Optional, arg.Many, luaList(a.Choices[arg.Name]))
	}
	b.WriteString("      },\n      flags = {\n")
	for _, arg := range a.Flags {
		fmt.Fprintf(&b, "        { name = %s, flag = %s, type = %s },\n", luaString(arg.Name), luaString(arg.Flag), luaString(arg.Type))
	}
	b.WriteString("      },\n    })\n  end,\n}\n")
	return b.String()
}

// paletteTitle reads well in the palette: "Rox: Up…" for a command that asks or opens a pane.
func paletteTitle(a Action) string {
	title := a.Title
	if strings.HasPrefix(title, a.Program+" ") {
		words := strings.Fields(strings.TrimPrefix(title, a.Program+" "))
		for i, w := range words {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
		title = strings.ToUpper(a.Program[:1]) + a.Program[1:] + ": " + strings.Join(words, " ")
	}
	if (a.Mode == "terminal" || a.Pick != "") && !strings.HasSuffix(title, "…") {
		title += "…"
	}
	return title
}

func luaString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}

func luaList(items []string) string {
	if len(items) == 0 {
		return "{}"
	}
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = luaString(s)
	}
	return "{ " + strings.Join(quoted, ", ") + " }"
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

const header = `-- rox and rix as Rex actions. Generated by "rox rex lua"; rerun "rox rex install"
-- after updating rox, rather than editing this file.
--
-- Every rox and rix command is an action named PROGRAM_COMMAND, such as rox_up,
-- rox_funnel_add or rix_auth. Find them in the command palette under Rox and Rix,
-- bind them with rex.bind, or run them with rex do:
--
--   rex do rox_up backend=container
--   rex do rox_ls
--   rex do rox_shell name=rv-web-3f2a9c1b7d4e
--   rex do rix_status
--
-- Flags are arguments with dashes as underscores: rex do rox_up no_prebuild=true.
-- Repeatable flags and lists take JSON arrays: rex do rox_forward --args '{"name":"…","ports":["3000"]}'.
-- Slow or interactive commands open a terminal split beside the pane, and the action
-- returns its block. Quick ones return their output. cwd_dir sets the directory a
-- command runs in; it defaults to the pane's.

`

// runtime is the Lua shared by every action.
const runtime = `local function trim(s) return (s or ""):gsub("%s+$", "") end

local function fail(msg) error(msg, 0) end

-- The directory of the pane that asked, so rox finds the project checkout there.
local function pane_cwd(ctx)
  if ctx.block_id == nil then return nil end
  local blocks = rex.session.list_blocks{}
  for _, b in ipairs((blocks and blocks.blocks) or {}) do
    if b.block_id == ctx.block_id then
      return b.cwd or b.working_directory or (b.terminal and b.terminal.cwd)
    end
  end
  return nil
end

local function run_quick(argv, cwd)
  local r, err = rex.exec(argv, { timeout = 25, cwd = cwd, env = { NO_COLOR = "1" } })
  if r == nil then fail(err) end
  if r.timed_out then fail(table.concat(argv, " ") .. " took too long for an action. Run it in a terminal.") end
  if not r.ok then
    local msg = trim(r.stderr) ~= "" and trim(r.stderr) or trim(r.stdout)
    fail(msg ~= "" and msg or (argv[1] .. " exited with status " .. tostring(r.code)))
  end
  return r.stdout
end

-- Guests for the picker, from rox ls --json.
local function pick_guest(ctx, title)
  local out = run_quick({ BIN.rox, "ls", "--json", "--all-projects" }, nil)
  local list = rex.json.decode(out)
  local items = {}
  for _, g in ipairs((list and list.guests) or {}) do
    local where = { g.backend, g.phase }
    if g.projects and g.projects ~= "-" then table.insert(where, g.projects) end
    table.insert(items, { id = g.name, title = g.name, subtitle = table.concat(where, " · "), icon = "server",
      detail = g.tailnet ~= "" and g.tailnet or nil })
  end
  if #items == 0 then fail("No rox guests on this machine. Start one with rox up.") end
  local id, why = rex.ui.pick{ title = title, placeholder = "Filter guests", items = items }
  if id == nil then
    if why == "cancelled" then return nil end
    fail(title .. ": " .. tostring(why))
  end
  return id
end

local function pick_choice(title, choices, optional)
  local items = {}
  if optional then table.insert(items, { id = "", title = "Default", subtitle = "let the command decide" }) end
  for _, c in ipairs(choices) do table.insert(items, { id = c, title = c }) end
  local id, why = rex.ui.pick{ title = title, items = items }
  if id == nil then
    if why == "cancelled" then return nil end
    fail(title .. ": " .. tostring(why))
  end
  return id
end

local interactive = { key = true, palette = true }

local function as_list(v)
  if v == nil then return {} end
  if type(v) == "table" then return v end
  return { tostring(v) }
end

-- bridge turns an action's arguments into a command line and runs it.
local function bridge(ctx, args, spec)
  local argv = { BIN[spec.program] }
  for _, word in ipairs(spec.path) do table.insert(argv, word) end
  local asking = interactive[ctx.origin or ""] == true
  local label = spec.program .. " " .. table.concat(spec.path, " ")
  local rest = {}
  for _, p in ipairs(spec.positional) do
    local v = args[p.name]
    if v == nil and asking and spec.pick == p.name then
      v = pick_guest(ctx, label .. ": which guest?")
      if v == nil then return nil end
    elseif v == nil and asking and #p.choices > 0 then
      v = pick_choice(label .. ": " .. p.name, p.choices, p.optional)
      if v == nil then return nil end
      if v == "" then v = nil end
    elseif v == nil and asking and not p.optional then
      local text, why = rex.ui.input{ title = label .. ": " .. p.name, placeholder = p.name, confirm = "Run " .. label }
      if text == nil then
        if why == "cancelled" then return nil end
        fail(tostring(why))
      end
      if p.many then
        v = {}
        for word in text:gmatch("%S+") do table.insert(v, word) end
      else
        v = text
      end
    end
    if v == nil and not p.optional then fail(label .. " needs " .. p.name) end
    if p.many then
      for _, item in ipairs(as_list(v)) do table.insert(rest, tostring(item)) end
    elseif v ~= nil and v ~= "" then
      table.insert(argv, tostring(v))
    end
  end
  for _, f in ipairs(spec.flags) do
    local v = args[f.name]
    if v ~= nil then
      if f.type == "boolean" then
        if v then table.insert(argv, "--" .. f.flag) end
      elseif f.type == "array" then
        for _, item in ipairs(as_list(v)) do table.insert(argv, "--" .. f.flag .. "=" .. tostring(item)) end
      else
        table.insert(argv, "--" .. f.flag .. "=" .. tostring(v))
      end
    end
  end
  -- Variadic arguments go last; a command after -- is passed through as written.
  if #rest > 0 then
    local last = spec.positional[#spec.positional]
    if last and (last.name == "command") then table.insert(argv, "--") end
    for _, item in ipairs(rest) do table.insert(argv, item) end
  end

  local cwd = args.cwd_dir or pane_cwd(ctx)
  if spec.mode == "json" then
    table.insert(argv, "--json")
    local out = run_quick(argv, cwd)
    local value, err = rex.json.decode(out)
    if value == nil then fail("Could not read " .. label .. "'s JSON: " .. tostring(err)) end
    return value
  elseif spec.mode == "text" then
    local out = trim(run_quick(argv, cwd))
    if out == "" then return true end
    return { output = out }
  end

  -- terminal: a split beside the pane that asked, or a new window without one.
  local block = rex.layout.block{
    flavor  = "com.superlogical.terminal.shell",
    label   = label,
    options = { command = argv, cwd = cwd, exit = { on_completion = false } },
  }
  local result, err
  if ctx.block_id ~= nil then
    result, err = rex.session.new_split{ anchor_id = ctx.block_id, direction = "vertical", side = "after",
      ratio = 0.55, focus = true, layout = block }
  else
    result, err = rex.session.new_window{ layout = block }
  end
  if result == nil then
    fail("Could not open a terminal for " .. label .. ": " .. tostring(err) ..
      ". Run it from a Rex pane, or pass a session with rex do -s.")
  end
  return { block_id = result.block_ids and result.block_ids[1], window_id = result.window_id, command = argv }
end
`

// RexBin is the rex the actions run to reach the app, since the server's PATH may lack it.
var RexBin = ""

// customActions are hand-written actions for commands that work better from inside Rex.
var customActions = map[string]string{
	// The Rex app keeps its own host list, and its Add Remote Host dialog takes no arguments,
	// so the action copies the guest's address and opens the dialog for it to be pasted.
	// From the app's palette or a key binding it queues the dialog on the app that asked,
	// which needs no Remote Control. From rex do there's no app to queue on, so it asks this
	// machine's app with rex -C, which the app refuses while Remote Control is off; the
	// action then says so, and the address is still on the clipboard.
	"add_remote_host": `
-- The connected Rex app on this machine, from rex client ls.
local function local_app()
  local r = rex.exec({ BIN.rex, "--autostart=false", "--timeout", "5s", "client", "ls", "--json" }, { timeout = 8 })
  if r == nil or not r.ok then return nil end
  local listing = rex.json.decode(r.stdout)
  for _, c in ipairs((listing and listing.clients) or {}) do
    local info, principal = c.info or {}, c.principal or {}
    if info.kind == "app" and c.connection_state == "connected" and principal.transport == "unix" then
      return { id = c.client_id, label = info.label or c.label or "" }
    end
  end
  return nil
end

-- Whether app is already connected to the Rex server at address.
local function app_connected(app, address)
  local r = rex.exec({ BIN.rex, "--autostart=false", "--timeout", "5s", "-S", address, "client", "ls", "--json" }, { timeout = 8 })
  if r == nil or not r.ok then return false end
  local listing = rex.json.decode(r.stdout)
  for _, c in ipairs((listing and listing.clients) or {}) do
    local info = c.info or {}
    if info.kind == "app" and c.connection_state == "connected" and (app.label == "" or info.label == app.label) then
      return true
    end
  end
  return false
end

rex.action{
  name        = "rox_add_remote_host",
  title       = "Rox: Add a Guest to the Rex App…",
  description = "Copy a guest's address and open Add Remote Host in this app, to paste it.",
  category    = "Rox",
  keywords    = "rox add remote host guest sidebar app connect tailnet",
  args        = { ["name"] = "string?" },
  run = function(ctx, args)
    local asking = interactive[ctx.origin or ""] == true
    local name = args.name
    if name == nil then
      if not asking then fail("rox_add_remote_host needs name") end
      name = pick_guest(ctx, "Add which guest to the Rex app?")
      if name == nil then return nil end
    end
    -- rox inspect finds guests on other Rex hosts too.
    local record, err = rex.json.decode(run_quick({ BIN.rox, "inspect", name, "--json" }, nil))
    if record == nil then fail("Could not read " .. name .. ": " .. tostring(err)) end
    local address = record.tailnet
    local here_only = false
    if address == nil or address == "" then
      address, here_only = record.endpoint, true
    end
    if address == nil or address == "" or (here_only and address:sub(1, 7) ~= "unix://") then
      fail(name .. " isn't on the tailnet, so only its own host can reach it. Run rox add-remote-host " .. name .. " there")
    end

    local app = local_app()
    if app == nil then
      fail("Open the Rex app on this machine, then run this again. " .. name .. "'s address is " .. address)
    end
    if app_connected(app, address) then
      rex.log("info", name .. " is already in the Rex app.")
      return { name = name, address = address, already = true }
    end

    local copied = rex.exec({ "pbcopy" }, { stdin = address, timeout = 5 })
    copied = copied ~= nil and copied.ok
    local how = copied and ("Copied " .. address .. ". Paste it") or ("Enter " .. address)

    if asking then
      -- The app that asked performs this once the action returns.
      local queued, why = pcall(rex.client.queue, "client.host.add", {})
      if not queued then
        fail("Couldn't open Add Remote Host: " .. tostring(why) .. ". " .. how .. " into Add Remote Host yourself")
      end
    else
      local r = rex.exec({ BIN.rex, "--autostart=false", "--timeout", "8s", "-C", app.id, "do", "client.host.add" }, { timeout = 12 })
      if r == nil or not r.ok then
        local why = r and trim(r.stderr ~= "" and r.stderr or r.stdout) or "rex didn't run"
        fail("The Rex app didn't open Add Remote Host (" .. why .. "). Turn on Remote Control in the app, " ..
          "or run Rox: Add a Guest to the Rex App from its palette. " .. how .. " into Add Remote Host yourself")
      end
    end
    rex.log("info", how .. " into Add Remote Host to add " .. name .. ".")
    if here_only then
      rex.log("warn", name .. " isn't on the tailnet, so only this machine's app can reach it.")
    end
    return { name = name, address = address, copied = copied, opened = true }
  end,
}
`,
}
