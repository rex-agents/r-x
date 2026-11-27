package rux

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// catalogDoc is what `rux ui --catalog` prints: the spec format and every
// component the app renders. Keep in sync with uiCatalog and web/ui.js.
const catalogDoc = `rux ui: show UI in the thread, rendered by the rux app (json-render style specs, https://json-render.dev).

USAGE
  rux ui FILE|- [--ask] [--title TEXT]     show a spec (JSON file, or - for stdin)
  rux diff [GIT DIFF ARGS] [--title TEXT]  show a code diff (default: uncommitted changes, including new files)
  --ask    it's a question: when the user submits or presses a button, you get a message
           "[rux] The user answered your UI ..." with the action, params and form values.
           Don't wait in a loop for it; end your turn and the answer arrives as the next message.

SPEC
  {"root": "main", "state": {"choice": null},
   "elements": {
     "main": {"type": "Card", "props": {"title": "Pick a plan"}, "children": ["pick", "go"]},
     "pick": {"type": "Choice", "props": {"options": ["Fast", "Thorough"], "value": {"$bindState": "/choice"}}},
     "go":   {"type": "Button", "props": {"label": "Go", "variant": "primary"}, "on": {"press": {"action": "submit"}}}}}
  A nested tree also works: {"type": "Card", "props": {...}, "children": [{"type": ...}, ...]}.

  Expressions, usable in any prop:
    {"$state": "/path"}                 read state (JSON pointer)
    {"$bindState": "/path"}             two-way binding, for an input's "value"
    {"$template": "Hi ${/name}"}        string with state
    {"$cond": COND, "$then": A, "$else": B}
  COND: {"$state": "/path"} (truthy), plus optional "eq": V or "neq": V; {"not": COND}; a list means all.
  Any element can have "visible": COND.
  "on": {"press": ACTION} (Button), {"change": ACTION} (inputs). ACTION is {"action": NAME, "params": {...}} or a list.
    setState  params {"statePath": "/x", "value": V}: change state in the app only
    submit    send the form values (all state) and params to you
    any other name is sent to you the same way, e.g. {"action": "approve", "params": {"id": 3}}

COMPONENTS (props; * = required)
  Layout
    Card      title, subtitle, tone (green|warn|error)       children
    Stack     direction (vertical|horizontal), gap (0-6), align (start|center|end|stretch), wrap   children
    Grid      columns (1-6), minWidth (px, responsive)       children
    Tabs      tabs* (labels, one per child)                  children
    Heading   text*, level (1-3)
    Text      text* (markdown), tone (muted|faint|green|warn|error), mono
    Badge     text*, tone (green|warn|error|muted)
    Divider
  Data
    Stat      label*, value*, sub, tone, trend (number: + up, - down)
    Meter     label*, value* (0-1, or with max), max, sub
    KeyValue  items* [{key, value}]
    Table     columns* [string | {key, label, align (left|right), mono}], rows* [object | array], maxRows
    List      items* [string | {title*, sub, badge, status (running|idle|done|failed|waiting), href}]
    Timeline  items* [{label*, start* (ISO time or ms), end, status, lane}], from, to: a Gantt chart; lanes group bars into rows
  Charts
    Chart     kind (line|area|bar|stackedBar|sparkline), labels [x labels], series* [{name, data* [numbers]}],
              height (px), yUnit, xLabel, yLabel, max
    Graph     nodes* [{id*, label*, sub, status, col}], edges [{from*, to*, label, live (animated), dashed}]
              columns come from "col" or from the edges (left to right)
  Inputs (bind "value" with $bindState)
    Choice    options* [string | {value, label, description}], multiple, value, label
    Select    options* (as Choice), value, label, placeholder
    TextInput value, label, placeholder, multiline, rows
    Toggle    value, label
    Button    label*, variant (primary|default|danger), on.press
    Buttons   children (Buttons in a row)
  Code
    Code      code*, language, filename, startLine, highlight [line numbers]
    Diff      patch (unified git diff) or src (a shared file URL), title, view (unified|split)
    Image     src* (a URL from rux share), caption, alt

Use real data, keep it compact (the user is often on a phone), and prefer a Table, Chart or Diff over
pasting the same thing as text. Put large data (diffs, logs) in files: rux diff does this for diffs.`

func uiEndpoint() (string, string, error) {
	u, tok := os.Getenv("RUX_UI_URL"), os.Getenv("RUX_CRED_TOKEN")
	if u == "" || tok == "" {
		return "", "", errors.New("not running inside a rux thread (RUX_UI_URL isn't set)")
	}
	return u, tok, nil
}

func postUI(spec json.RawMessage, ask bool, title string) (string, error) {
	endpoint, tok, err := uiEndpoint()
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]any{"spec": spec, "ask": ask, "title": title})
	req, _ := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode != 201 {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(b)))
	}
	var out struct{ UI string }
	json.Unmarshal(b, &out)
	return out.UI, nil
}

// runUI: rux ui FILE|- [--ask] [--title TEXT] | rux ui --catalog
func runUI(args []string) {
	const usage = "usage: rux ui FILE|- [--ask] [--title TEXT]   (rux ui --catalog for the format and components)"
	file, title, ask := "", "", false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--catalog", "-h", "--help":
			fmt.Println(catalogDoc)
			return
		case "--ask":
			ask = true
		case "--title", "-t":
			i++
			if i >= len(args) {
				usageExit(usage)
			}
			title = args[i]
		default:
			if file != "" || (strings.HasPrefix(a, "-") && a != "-") {
				usageExit(usage)
			}
			file = a
		}
	}
	if file == "" {
		usageExit(usage)
	}
	var b []byte
	var err error
	if file == "-" {
		b, err = io.ReadAll(os.Stdin)
	} else {
		b, err = os.ReadFile(file)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "rux ui:", err)
		os.Exit(1)
	}
	if !json.Valid(b) {
		fmt.Fprintln(os.Stderr, "rux ui: the spec isn't valid JSON")
		os.Exit(1)
	}
	id, err := postUI(b, ask, title)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rux ui:", err)
		os.Exit(1)
	}
	if ask {
		fmt.Printf("shown as %s; the user's answer will arrive as your next message, so end your turn now\n", id)
	} else {
		fmt.Printf("shown as %s\n", id)
	}
}

// runDiff: rux diff [GIT DIFF ARGS] [--title TEXT]. With no args it shows
// uncommitted changes against HEAD, including untracked files.
func runDiff(args []string) {
	title := ""
	var gitArgs []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--title" || args[i] == "-t" {
			i++
			if i < len(args) {
				title = args[i]
			}
			continue
		}
		gitArgs = append(gitArgs, args[i])
	}
	var patch bytes.Buffer
	base := []string{"-c", "core.quotePath=false", "diff", "--no-color", "--no-ext-diff", "-M"}
	if len(gitArgs) == 0 {
		if exec.Command("git", "rev-parse", "--verify", "-q", "HEAD").Run() == nil {
			gitArgs = []string{"HEAD"}
		}
		out, err := exec.Command("git", append(base, gitArgs...)...).Output()
		if err != nil {
			fail("rux diff: git diff: %v", err)
		}
		patch.Write(out)
		untracked, _ := exec.Command("git", "ls-files", "--others", "--exclude-standard", "-z").Output()
		for _, f := range strings.Split(string(untracked), "\x00") {
			if f == "" {
				continue
			}
			// --no-index exits 1 when files differ, which they always do here.
			out, _ := exec.Command("git", append(base, "--no-index", "--", "/dev/null", f)...).Output()
			patch.Write(out)
		}
		if title == "" {
			title = "uncommitted changes"
		}
	} else {
		out, err := exec.Command("git", append(base, gitArgs...)...).Output()
		if err != nil {
			fail("rux diff: git diff %s: %v", strings.Join(gitArgs, " "), err)
		}
		patch.Write(out)
		if title == "" {
			title = "git diff " + strings.Join(gitArgs, " ")
		}
	}
	if patch.Len() == 0 {
		fmt.Println("no changes")
		return
	}
	home, _ := os.UserHomeDir()
	dir := home + "/.rux/diffs"
	os.MkdirAll(dir, 0o755)
	path := fmt.Sprintf("%s/changes-%s.diff", dir, time.Now().Format("20060102-150405"))
	if err := os.WriteFile(path, patch.Bytes(), 0o644); err != nil {
		fail("rux diff: %v", err)
	}
	url, err := shareFileQuiet(path)
	if err != nil {
		fail("rux diff: uploading: %v", err)
	}
	spec, _ := json.Marshal(map[string]any{"type": "Diff", "props": map[string]any{"src": url, "title": title}})
	id, err := postUI(spec, false, "") // the Diff shows the title itself
	if err != nil {
		fail("rux diff: %v", err)
	}
	files := strings.Count(patch.String(), "\ndiff --git ") + map[bool]int{true: 1}[strings.HasPrefix(patch.String(), "diff --git ")]
	fmt.Printf("shown as %s (%d files, %d KB)\n", id, files, patch.Len()/1024)
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

// shareFileQuiet uploads a file as an asset without it showing up on its own
// in the thread (the UI that references it shows it).
func shareFileQuiet(path string) (string, error) {
	os.Setenv("RUX_ASSET_QUIET", "1")
	defer os.Unsetenv("RUX_ASSET_QUIET")
	return shareFile(path, "")
}
