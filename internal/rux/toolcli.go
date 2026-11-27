package rux

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// rux action: run the host tools enabled for this thread's project.
//
//	rux action                         list them
//	rux action NAME --help             a tool's arguments
//	rux action NAME [ARG] [--KEY VALUE]... [--wait SECONDS]
//	rux action status RUN

type guestTool struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Args        []toolArg `json:"args"`
	Approval    string    `json:"approval"`
}

func toolURL(path string, q url.Values) (string, string) {
	u, tok, err := uiEndpoint()
	if err != nil {
		fail("rux action: %v", err)
	}
	pu, _ := url.Parse(u)
	q.Set("thread", pu.Query().Get("thread"))
	pu.Path, pu.RawQuery = path, q.Encode()
	return pu.String(), tok
}

func toolGet(path string, out any) {
	u, tok := toolURL(path, url.Values{})
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		fail("rux action: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		fail("rux action: %s", strings.TrimSpace(string(b)))
	}
	json.Unmarshal(b, out)
}

func listGuestTools() []guestTool {
	var tools []guestTool
	toolGet("/tools", &tools)
	return tools
}

func toolHelp(t guestTool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "rux action %s", t.Name)
	for _, a := range t.Args {
		if a.Required {
			fmt.Fprintf(&b, " --%s %s", a.Name, strings.ToUpper(cmpOr(a.Type, "string")))
		}
	}
	fmt.Fprintf(&b, "\n  %s\n", t.Description)
	for _, a := range t.Args {
		req := ""
		if a.Required {
			req = ", required"
		}
		fmt.Fprintf(&b, "  --%s (%s%s)  %s\n", a.Name, cmpOr(a.Type, "string"), req, a.Description)
	}
	switch t.Approval {
	case "none":
		b.WriteString("  runs without asking the user\n")
	case "every-run":
		b.WriteString("  the user approves every run in the app\n")
	default:
		b.WriteString("  the user approves runs in the app, unless they've allowed it for this project\n")
	}
	return b.String()
}

func runTool(args []string) {
	if len(args) == 0 || args[0] == "list" || args[0] == "-h" || args[0] == "--help" {
		tools := listGuestTools()
		if len(tools) == 0 {
			fmt.Println("No actions are enabled for this project. The user turns them on in the project's settings in the rux app.")
			return
		}
		fmt.Println("Actions: they run on the user's Mac, not in this guest. File arguments (a file or folder) are uploaded.")
		fmt.Println("Usage: rux action NAME [--ARG VALUE]... [--wait SECONDS]; the first positional value goes to the first argument.")
		fmt.Println("If a run needs approval or takes long, rux action returns early and the result arrives as a \"[rux]\" message; end your turn then.")
		fmt.Println()
		for _, t := range tools {
			fmt.Println(toolHelp(t))
		}
		return
	}
	if args[0] == "status" && len(args) == 2 {
		var st struct {
			Run, Tool, State, Summary string
			Code                      int
			Log                       []string
		}
		toolGet("/tools/runs/"+url.PathEscape(args[1]), &st)
		fmt.Printf("%s (%s): %s", st.Run, st.Tool, st.State)
		if st.State == "failed" {
			fmt.Printf(" (exit %d)", st.Code)
		}
		fmt.Println()
		for _, l := range st.Log {
			fmt.Println("  " + l)
		}
		if st.Summary != "" {
			fmt.Println(st.Summary)
		}
		return
	}

	name := args[0]
	var tool *guestTool
	tools := listGuestTools()
	for i := range tools {
		if tools[i].Name == name {
			tool = &tools[i]
		}
	}
	if tool == nil {
		var names []string
		for _, t := range tools {
			names = append(names, t.Name)
		}
		fail("rux action: no action %q is enabled for this project (enabled: %s)", name, cmpOr(strings.Join(names, ", "), "none"))
	}
	byName := map[string]toolArg{}
	for _, a := range tool.Args {
		byName[a.Name] = a
	}
	vals := map[string]string{}
	wait := "90"
	pos := 0
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if a == "-h" || a == "--help" {
			fmt.Print(toolHelp(*tool))
			return
		}
		if !strings.HasPrefix(a, "--") {
			for pos < len(tool.Args) && vals[tool.Args[pos].Name] != "" {
				pos++
			}
			if pos >= len(tool.Args) {
				fail("rux action %s: unexpected %q\n%s", name, a, toolHelp(*tool))
			}
			vals[tool.Args[pos].Name] = a
			continue
		}
		k, v, hasV := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		if k == "wait" {
			if !hasV && i+1 < len(rest) {
				i++
				v = rest[i]
			}
			if _, err := strconv.Atoi(v); err != nil {
				fail("rux action: --wait takes seconds")
			}
			wait = v
			continue
		}
		spec, ok := byName[k]
		if !ok {
			fail("rux action %s: no argument --%s\n%s", name, k, toolHelp(*tool))
		}
		if !hasV {
			if spec.Type == "bool" {
				v = "true"
			} else if i+1 < len(rest) {
				i++
				v = rest[i]
			} else {
				fail("rux action %s: --%s needs a value", name, k)
			}
		}
		vals[k] = v
	}
	for _, a := range tool.Args {
		if a.Required && vals[a.Name] == "" {
			fail("rux action %s: --%s is required\n%s", name, a.Name, toolHelp(*tool))
		}
		if a.Type == "file" && vals[a.Name] != "" {
			if _, err := os.Stat(vals[a.Name]); err != nil {
				fail("rux action %s: --%s: %v", name, a.Name, err)
			}
		}
	}

	var req runRequest
	req.Args = map[string]string{}
	for k, v := range vals {
		if byName[k].Type != "file" {
			req.Args[k] = v
		}
	}
	if out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		req.Git.Branch = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output(); err == nil {
		req.Git.Commit = strings.TrimSpace(string(out))
		if exec.Command("git", "diff", "--quiet", "HEAD").Run() != nil {
			req.Git.Commit += "-dirty"
		}
	}

	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(writeRunUpload(pw, req, vals, byName)) }()
	u, tok := toolURL("/tools/run", url.Values{"tool": {name}, "wait": {wait}})
	hreq, _ := http.NewRequest("POST", u, pr)
	hreq.Header.Set("Authorization", "Bearer "+tok)
	hreq.Header.Set("Content-Type", "application/gzip")
	resp, err := http.DefaultClient.Do(hreq)
	if err != nil {
		fail("rux action: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		fail("rux action %s: %s", name, strings.TrimSpace(string(b)))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 2<<20)
	for sc.Scan() {
		var m struct {
			Run string
			Log *string
			End *struct {
				Run, State, Summary string
				Code                int
				Detached            bool
			}
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch {
		case m.Log != nil:
			fmt.Println(*m.Log)
		case m.End != nil:
			e := m.End
			if e.Detached {
				what := "is still running"
				if e.State == "approval" {
					what = "is waiting for the user to approve it in the app"
				}
				fmt.Printf("\nrun %s %s. Its result will arrive as your next message (\"[rux] Action run ...\"), so end your turn now. (rux action status %s checks on it.)\n", e.Run, what, e.Run)
				return
			}
			if e.Summary != "" {
				fmt.Println("\n" + e.Summary)
			}
			switch e.State {
			case "done":
				fmt.Printf("\nrun %s done\n", e.Run)
				return
			case "denied":
				fail("\nrun %s denied: %s", e.Run, e.Summary)
			default:
				fail("\nrun %s %s (exit %d)", e.Run, e.State, e.Code)
			}
		}
	}
	fail("rux action: lost the connection to the host; rux action status RUN checks on the run")
}

// writeRunUpload writes the run request and the file arguments as a gzipped tar.
func writeRunUpload(w io.Writer, req runRequest, vals map[string]string, args map[string]toolArg) error {
	zw := gzip.NewWriter(w)
	tw := tar.NewWriter(zw)
	b, _ := json.Marshal(req)
	if err := tw.WriteHeader(&tar.Header{Name: "rux-run.json", Mode: 0o600, Size: int64(len(b)), ModTime: time.Now()}); err != nil {
		return err
	}
	tw.Write(b)
	for k, v := range vals {
		if args[k].Type != "file" {
			continue
		}
		root, err := filepath.Abs(v)
		if err != nil {
			return err
		}
		prefix := "files/" + k + "/" + filepath.Base(root)
		add := func(p, name string) error {
			info, err := os.Lstat(p)
			if err != nil {
				return err
			}
			link := ""
			if info.Mode()&os.ModeSymlink != 0 {
				link, _ = os.Readlink(p)
			}
			hd, err := tar.FileInfoHeader(info, link)
			if err != nil {
				return err
			}
			hd.Name = name
			hd.Uname, hd.Gname, hd.Uid, hd.Gid = "", "", 0, 0
			if err := tw.WriteHeader(hd); err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				f, err := os.Open(p)
				if err != nil {
					return err
				}
				defer f.Close()
				_, err = io.Copy(tw, f)
				return err
			}
			return nil
		}
		// A git checkout: just the files git would keep (tracked, plus new
		// ones that aren't ignored), so .git and build output stay behind.
		if top, err := exec.Command("git", "-C", root, "rev-parse", "--show-toplevel").Output(); err == nil && sameDir(strings.TrimSpace(string(top)), root) {
			list, err := exec.Command("git", "-C", root, "ls-files", "-z", "-co", "--exclude-standard").Output()
			if err != nil {
				return fmt.Errorf("listing %s's files: %v", v, err)
			}
			if err := add(root, prefix); err != nil {
				return err
			}
			for _, rel := range strings.Split(string(list), "\x00") {
				if rel == "" {
					continue
				}
				p := filepath.Join(root, rel)
				if st, err := os.Lstat(p); err != nil || st.IsDir() { // deleted, or a submodule
					continue
				}
				if err := add(p, prefix+"/"+rel); err != nil {
					return err
				}
			}
			continue
		}
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			name := prefix
			if rel != "." {
				name += "/" + filepath.ToSlash(rel)
			}
			return add(p, name)
		})
		if err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}

func sameDir(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}
