package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

// Tool is a tool an agent can call.
type Tool struct {
	Spec ToolSpec
	// Label is a short description of a call for the UI, such as `go test ./...`.
	Label func(input json.RawMessage) string
	Run   func(ctx context.Context, input json.RawMessage) (string, error)
	// Approve marks calls the user has to allow first.
	Approve bool
	// ReadOnly calls change nothing, so several can run at once.
	ReadOnly bool
}

func schema(required []string, props map[string]any) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) map[string]any  { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any  { return map[string]any{"type": "integer", "description": desc} }
func flag(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }

// decode parses a tool's input, and checks that the required fields are there: a
// streamed input can come back cut off.
func decode(input json.RawMessage, v any, required ...string) error {
	if err := json.Unmarshal(input, v); err != nil {
		return fmt.Errorf("the tool input isn't valid JSON (%v); send the call again", err)
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(input, &m)
	for _, r := range required {
		if _, ok := m[r]; !ok {
			return fmt.Errorf("missing %q; send the call again with it", r)
		}
	}
	return nil
}

func field(input json.RawMessage, name string) string {
	var m map[string]any
	_ = json.Unmarshal(input, &m)
	switch v := m[name].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

const maxOutput = 30000

// clip keeps the head and tail of long output.
func clip(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	head, tail := s[:maxOutput/3], s[len(s)-2*maxOutput/3:]
	return head + fmt.Sprintf("\n… %d characters cut …\n", len(s)-len(head)-len(tail)) + tail
}

// CodingTools are the tools an agent works with in ex: a shell and files.
func CodingTools(ex Executor) []Tool {
	return []Tool{bashTool(ex, false), readTool(ex), writeTool(ex), editTool(ex), grepTool(ex), listTool(ex)}
}

// ReadTools look but don't change anything, plus a shell the user approves call by call.
func ReadTools(ex Executor) []Tool {
	return []Tool{readTool(ex), grepTool(ex), listTool(ex), bashTool(ex, true)}
}

func bashTool(ex Executor, approve bool) Tool {
	desc := "Run a bash command in " + ex.Where() + ", in " + ex.Dir() + ". Output (stdout and stderr) " +
		"is returned with the exit code. Commands time out after timeout_seconds (default 120, max 1800). " +
		"Each call is a new shell, so cd doesn't persist; chain with &&. Don't start servers that never exit " +
		"in the foreground: background them with nohup ... & and a log file."
	if approve {
		desc += " The user approves each command, so prefer the read_file, grep and list_files tools for looking around."
	}
	return Tool{
		Spec: ToolSpec{Name: "bash", Description: desc, Schema: schema([]string{"command"}, map[string]any{
			"command":         str("the command"),
			"timeout_seconds": num("how long to let it run"),
		})},
		Approve: approve,
		Label:   func(in json.RawMessage) string { return field(in, "command") },
		Run: func(ctx context.Context, in json.RawMessage) (string, error) {
			var a struct {
				Command string `json:"command"`
				Timeout int    `json:"timeout_seconds"`
			}
			if err := decode(in, &a, "command"); err != nil {
				return "", err
			}
			t := time.Duration(a.Timeout) * time.Second
			if t <= 0 {
				t = 2 * time.Minute
			}
			t = min(t, 30*time.Minute)
			out, code, err := ex.Run(ctx, a.Command, nil, t)
			out = clip(out)
			if err != nil {
				return out, fmt.Errorf("%v\n%s", err, out)
			}
			if code != 0 {
				return out, fmt.Errorf("exit code %d\n%s", code, out)
			}
			if strings.TrimSpace(out) == "" {
				out = "(no output)"
			}
			return out, nil
		},
	}
}

func readTool(ex Executor) Tool {
	return Tool{
		ReadOnly: true,
		Spec: ToolSpec{Name: "read_file", Description: "Read a text file with line numbers. Paths are relative to " +
			ex.Dir() + ". Reads up to 2000 lines from offset (1-based); pass offset and limit for more.",
			Schema: schema([]string{"path"}, map[string]any{
				"path": str("the file"), "offset": num("first line, from 1"), "limit": num("how many lines"),
			})},
		Label: func(in json.RawMessage) string { return field(in, "path") },
		Run: func(ctx context.Context, in json.RawMessage) (string, error) {
			var a struct {
				Path          string `json:"path"`
				Offset, Limit int
			}
			if err := decode(in, &a, "path"); err != nil {
				return "", err
			}
			if a.Offset < 1 {
				a.Offset = 1
			}
			if a.Limit <= 0 {
				a.Limit = 2000
			}
			script := fmt.Sprintf(`f=%s; [ -f "$f" ] || { echo "no such file: $f" >&2; exit 2; }; `+
				`awk -v s=%d -v e=%d 'NR>=s && NR<=e { printf "%%6d\t%%s\n", NR, $0 } END { if (NR>e) printf "… %%d more lines\n", NR-e }' "$f"`,
				ShellQuote(a.Path), a.Offset, a.Offset+a.Limit-1)
			out, code, err := ex.Run(ctx, script, nil, time.Minute)
			if err != nil || code != 0 {
				return "", fmt.Errorf("%s", firstNonEmpty(strings.TrimSpace(out), fmt.Sprint(err)))
			}
			if out == "" {
				return "(empty file)", nil
			}
			return clip(out), nil
		},
	}
}

func writeTool(ex Executor) Tool {
	return Tool{
		Spec: ToolSpec{Name: "write_file", Description: "Create or overwrite a file with content. Parent directories " +
			"are created. Prefer edit_file for changes to an existing file.",
			Schema: schema([]string{"path", "content"}, map[string]any{"path": str("the file"), "content": str("the whole file")})},
		Label: func(in json.RawMessage) string { return field(in, "path") },
		Run: func(ctx context.Context, in json.RawMessage) (string, error) {
			var a struct{ Path, Content string }
			if err := decode(in, &a, "path", "content"); err != nil {
				return "", err
			}
			if err := writeFile(ctx, ex, a.Path, a.Content); err != nil {
				return "", err
			}
			return fmt.Sprintf("Wrote %s (%d lines)", a.Path, strings.Count(a.Content, "\n")+1), nil
		},
	}
}

func writeFile(ctx context.Context, ex Executor, p, content string) error {
	q := ShellQuote(p)
	script := fmt.Sprintf(`mkdir -p "$(dirname %s)" && cat > %s`, q, q)
	out, code, err := ex.Run(ctx, script, []byte(content), time.Minute)
	if err != nil || code != 0 {
		return fmt.Errorf("writing %s: %s", p, firstNonEmpty(strings.TrimSpace(out), fmt.Sprint(err)))
	}
	return nil
}

func editTool(ex Executor) Tool {
	return Tool{
		Spec: ToolSpec{Name: "edit_file", Description: "Replace old_string with new_string in a file. old_string must " +
			"match exactly, whitespace included, and be unique unless replace_all is set; include surrounding lines to make it unique.",
			Schema: schema([]string{"path", "old_string", "new_string"}, map[string]any{
				"path": str("the file"), "old_string": str("the text to replace"), "new_string": str("its replacement"),
				"replace_all": flag("replace every match"),
			})},
		Label: func(in json.RawMessage) string { return field(in, "path") },
		Run: func(ctx context.Context, in json.RawMessage) (string, error) {
			var a struct {
				Path       string `json:"path"`
				Old        string `json:"old_string"`
				New        string `json:"new_string"`
				ReplaceAll bool   `json:"replace_all"`
			}
			if err := decode(in, &a, "path", "old_string", "new_string"); err != nil {
				return "", err
			}
			if a.Old == "" {
				return "", errors.New("old_string is empty; use write_file to create a file")
			}
			out, code, err := ex.Run(ctx, "cat "+ShellQuote(a.Path), nil, time.Minute)
			if err != nil || code != 0 {
				return "", fmt.Errorf("reading %s: %s", a.Path, firstNonEmpty(strings.TrimSpace(out), fmt.Sprint(err)))
			}
			n := strings.Count(out, a.Old)
			switch {
			case n == 0:
				return "", fmt.Errorf("old_string isn't in %s; read the file and copy the text exactly", a.Path)
			case n > 1 && !a.ReplaceAll:
				return "", fmt.Errorf("old_string matches %d places in %s; add context to make it unique, or set replace_all", n, a.Path)
			}
			updated := strings.Replace(out, a.Old, a.New, map[bool]int{true: -1, false: 1}[a.ReplaceAll])
			if err := writeFile(ctx, ex, a.Path, updated); err != nil {
				return "", err
			}
			return fmt.Sprintf("Edited %s (%d %s)", a.Path, n, map[bool]string{true: "replacements", false: "replacement"}[n > 1]), nil
		},
	}
}

func grepTool(ex Executor) Tool {
	return Tool{
		ReadOnly: true,
		Spec: ToolSpec{Name: "grep", Description: "Search file contents with a regular expression (ripgrep syntax " +
			"when rg is installed, else grep -E). Returns matching lines as path:line:text. Skips .git and ignored files with rg.",
			Schema: schema([]string{"pattern"}, map[string]any{
				"pattern": str("the regular expression"), "path": str("file or directory (default .)"),
				"glob": str("only files matching this glob, such as *.go"), "ignore_case": flag("case-insensitive"),
			})},
		Label: func(in json.RawMessage) string {
			l := field(in, "pattern")
			if p := field(in, "path"); p != "" {
				l += " in " + p
			}
			return l
		},
		Run: func(ctx context.Context, in json.RawMessage) (string, error) {
			var a struct {
				Pattern, Path, Glob string
				IgnoreCase          bool `json:"ignore_case"`
			}
			if err := decode(in, &a, "pattern"); err != nil {
				return "", err
			}
			p := ShellQuote(firstNonEmpty(a.Path, "."))
			rg, gr := "rg -n --no-heading --max-columns 400", "grep -rnE --exclude-dir=.git --exclude-dir=node_modules"
			if a.IgnoreCase {
				rg, gr = rg+" -i", gr+" -i"
			}
			if a.Glob != "" {
				rg += " -g " + ShellQuote(a.Glob)
				gr += " --include=" + ShellQuote(a.Glob)
			}
			q := ShellQuote(a.Pattern)
			script := fmt.Sprintf(`if command -v rg >/dev/null; then %s -e %s %s; else %s -e %s %s; fi | head -400`, rg, q, p, gr, q, p)
			out, _, err := ex.Run(ctx, script, nil, time.Minute)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(out) == "" {
				return "No matches", nil
			}
			return clip(out), nil
		},
	}
}

func listTool(ex Executor) Tool {
	return Tool{
		ReadOnly: true,
		Spec: ToolSpec{Name: "list_files", Description: "List files under a directory, skipping .git and node_modules. " +
			"With pattern, only names matching that shell glob (such as *_test.go).",
			Schema: schema(nil, map[string]any{"path": str("directory (default .)"), "pattern": str("name glob")})},
		Label: func(in json.RawMessage) string {
			return path.Join(firstNonEmpty(field(in, "path"), "."), field(in, "pattern"))
		},
		Run: func(ctx context.Context, in json.RawMessage) (string, error) {
			var a struct{ Path, Pattern string }
			if err := decode(in, &a); err != nil {
				return "", err
			}
			name := ""
			if a.Pattern != "" {
				name = " -name " + ShellQuote(a.Pattern)
			}
			script := fmt.Sprintf(`find %s \( -name .git -o -name node_modules \) -prune -o -type f%s -print | sort | head -500`,
				ShellQuote(firstNonEmpty(a.Path, ".")), name)
			out, code, err := ex.Run(ctx, script, nil, time.Minute)
			if err != nil || code != 0 {
				return "", fmt.Errorf("%s", firstNonEmpty(strings.TrimSpace(out), fmt.Sprint(err)))
			}
			if strings.TrimSpace(out) == "" {
				return "No files", nil
			}
			return out, nil
		},
	}
}
