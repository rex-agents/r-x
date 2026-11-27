package rux

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// shareSystemPrompt is appended to claude's system prompt in every thread.
const shareSystemPrompt = `You are running in a sandboxed guest, driven by rux. The user is reviewing your work from the rux app, often on their phone, and can't see your screen or files. Show them things instead of describing them:
- rux screenshot URL [--mobile] [--caption TEXT] takes a headless Chromium screenshot of a page (for example a dev server you started on localhost) and shares it into the thread. The first run installs Chromium, which takes about a minute. Use --height N for a taller capture.
- rux share FILE... [--caption TEXT] shares any file: images, videos, PDFs, logs, generated output.
- rux diff [GIT DIFF ARGS] shows a code diff with a file browser (default: your uncommitted changes, new files included). Use it whenever the user asks what you changed, and when you finish a change worth reviewing.
- rux ui FILE|- [--ask] shows rich UI built from the app's components: tables, charts, timelines, graphs, code, and inputs (choices, text, toggles, buttons). With --ask it's a question; the user's answer arrives as your next message, so end your turn after asking. Use it to ask the user to choose or approve something, and to present results visually. Run rux ui --catalog once for the format and components before your first use.
- rux action lists the actions the user has turned on for this project: jobs that run on their Mac rather than in this guest, with its tools and secrets you don't see (building a Mac app and publishing it, for example). rux action NAME --help shows an action's arguments. Runs usually need the user's approval in the app; when one returns early, end your turn and the result arrives as a message.
When you change anything visual (UI, styling, charts, rendered docs), screenshot the result and share it before you finish, and share anything else the user would want to look at. Look at your screenshots yourself (Read the saved PNG) to check they show what you meant before telling the user they're right. Don't share secrets.`

// managerSystemPrompt is added for a project's manager; PROJECT is replaced
// with the project's name.
const managerSystemPrompt = `You are the manager for the rux project PROJECT. The user talks to you from the project's page in the rux app, usually from their phone, to plan and delegate work on this project.

You have your own guest with the repo checked out, so you can read the code, check history, and investigate. Delegate implementation to worker threads with the rux MCP tools instead of doing it yourself: start_thread gives a fresh agent its own sandboxed guest and checkout. Write each worker's prompt so it can work with no other context: the goal, where to look, how to verify, what to report back, and that it should push a branch (never to main) and share screenshots of anything visual. Run independent tasks in parallel workers.

You're told automatically (in messages starting with "[rux]") when one of your workers finishes a turn or fails. Then review its work: read_thread for its transcript, view_file for its screenshots. Reply to it with send_message if it needs changes or answers, and tell the user concisely what's done, what needs their decision, and which branches are ready for them to review and merge. Only the user merges. Remove a worker's guest with remove_thread once its work is pushed or abandoned, since each guest uses memory.

Keep the project's notes (update_notes) current with what workers need to know: conventions, how to build and test, decisions the user made. Keep your messages to the user short; they read them on a phone.

When the user asks how things are going, call thread_stats and answer with a compact dashboard via rux ui (Stat tiles for totals, a Timeline of when each worker was working, a Chart of activity, a List or Table of threads with status and what's next), plus a few lines of text. To show a worker's changes, fetch its branch in your checkout and run rux diff origin/main...origin/BRANCH (or ask the worker to run rux diff).`

func usageExit(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(2)
}

// runShare: rux share FILE... [--caption TEXT]
func runShare(args []string) {
	var files []string
	caption := ""
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--caption" || a == "-c":
			if i+1 >= len(args) {
				usageExit("rux share: --caption needs text")
			}
			i++
			caption = args[i]
		case strings.HasPrefix(a, "--caption="):
			caption = strings.TrimPrefix(a, "--caption=")
		case a == "-h" || a == "--help":
			usageExit("usage: rux share FILE... [--caption TEXT]\nShares files into the rux thread, where the user sees them (images and videos inline).")
		default:
			files = append(files, a)
		}
	}
	if len(files) == 0 {
		usageExit("usage: rux share FILE... [--caption TEXT]")
	}
	failed := false
	for _, f := range files {
		u, err := shareFile(f, caption)
		if err != nil {
			fmt.Fprintf(os.Stderr, "rux share %s: %v\n", f, err)
			failed = true
			continue
		}
		fmt.Printf("shared %s (%s)\n", f, u)
	}
	if failed {
		os.Exit(1)
	}
}

func shareFile(path, caption string) (string, error) {
	endpoint, token := os.Getenv("RUX_ASSET_URL"), os.Getenv("RUX_CRED_TOKEN")
	if endpoint == "" || token == "" {
		return "", errors.New("not running inside a rux thread (RUX_ASSET_URL isn't set)")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", errors.New("is a directory")
	}
	if st.Size() > maxAssetBytes {
		return "", fmt.Errorf("%d MB is over the %d MB limit", st.Size()>>20, maxAssetBytes>>20)
	}
	req, err := http.NewRequest("POST", endpoint, f)
	if err != nil {
		return "", err
	}
	req.ContentLength = st.Size()
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Rux-Name", url.QueryEscape(filepath.Base(path)))
	req.Header.Set("X-Rux-Caption", url.QueryEscape(caption))
	if os.Getenv("RUX_ASSET_QUIET") != "" {
		req.Header.Set("X-Rux-Quiet", "1")
	}
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != 201 {
		return "", fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out struct{ URL string }
	json.Unmarshal(body, &out)
	return out.URL, nil
}

// runScreenshot: rux screenshot URL [--mobile] [--width W] [--height H] [--wait MS] [--out FILE] [--caption TEXT] [--no-share]
func runScreenshot(args []string) {
	const usage = "usage: rux screenshot URL [--mobile] [--width W] [--height H] [--wait MS] [--out FILE] [--caption TEXT] [--no-share]"
	target, caption, out := "", "", ""
	width, height, wait, scale := 1280, 800, 3000, 1
	mobile, share := false, true
	num := func(i int) int {
		if i >= len(args) {
			usageExit(usage)
		}
		n, err := strconv.Atoi(args[i])
		if err != nil || n <= 0 {
			usageExit(usage)
		}
		return n
	}
	widthSet := false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--mobile":
			mobile = true
		case "--width":
			i++
			width, widthSet = num(i), true
		case "--height":
			i++
			height = num(i)
		case "--wait":
			i++
			wait = num(i)
		case "--out", "-o":
			i++
			if i >= len(args) {
				usageExit(usage)
			}
			out = args[i]
		case "--caption", "-c":
			i++
			if i >= len(args) {
				usageExit(usage)
			}
			caption = args[i]
		case "--no-share":
			share = false
		case "-h", "--help":
			usageExit(usage)
		default:
			if strings.HasPrefix(a, "-") || target != "" {
				usageExit(usage)
			}
			target = a
		}
	}
	if target == "" {
		usageExit(usage)
	}
	if !strings.Contains(target, "://") {
		if _, err := os.Stat(target); err == nil {
			abs, _ := filepath.Abs(target)
			target = "file://" + abs
		} else {
			target = "http://" + target
		}
	}
	if mobile {
		if !widthSet {
			width = 390
		}
		if height == 800 {
			height = 844
		}
		scale = 2
	}
	if out == "" {
		home, _ := os.UserHomeDir()
		dir := filepath.Join(home, ".rux", "screenshots")
		os.MkdirAll(dir, 0o755)
		out = filepath.Join(dir, "shot-"+time.Now().Format("20060102-150405")+".png")
	}
	out, _ = filepath.Abs(out)
	browser, err := findChromium()
	if err != nil {
		fmt.Fprintln(os.Stderr, "rux screenshot:", err)
		os.Exit(1)
	}
	cargs := []string{"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--hide-scrollbars",
		"--no-first-run", "--mute-audio", fmt.Sprintf("--window-size=%d,%d", width, height),
		fmt.Sprintf("--force-device-scale-factor=%d", scale), fmt.Sprintf("--timeout=%d", wait),
		"--screenshot=" + out}
	if mobile {
		cargs = append(cargs, "--user-agent=Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1")
	}
	os.Remove(out)
	cmd := exec.Command(browser, append(cargs, target)...)
	output, err := cmd.CombinedOutput()
	if _, statErr := os.Stat(out); statErr != nil {
		fmt.Fprintf(os.Stderr, "rux screenshot: chromium didn't write a screenshot (%v)\n%s", err, tail(string(output), 2000))
		os.Exit(1)
	}
	fmt.Printf("saved %s (%dx%d%s)\n", out, width, height, map[bool]string{true: ", mobile"}[mobile])
	if !share {
		return
	}
	if caption == "" {
		caption = target
	}
	u, err := shareFile(out, caption)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rux screenshot: sharing:", err)
		os.Exit(1)
	}
	fmt.Printf("shared (%s)\n", u)
}

func tail(s string, n int) string {
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

// findChromium returns a Chromium or Chrome binary, installing Debian's
// chromium package with sudo if there's none.
func findChromium() (string, error) {
	names := []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"}
	look := func() string {
		for _, n := range names {
			if p, err := exec.LookPath(n); err == nil {
				return p
			}
		}
		if p := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"; fileExists(p) {
			return p
		}
		return ""
	}
	if p := look(); p != "" {
		return p, nil
	}
	if _, err := exec.LookPath("apt-get"); err != nil {
		return "", errors.New("no Chromium or Chrome found; install one")
	}
	fmt.Fprintln(os.Stderr, "rux screenshot: installing chromium (first run only, about a minute)…")
	script := "apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends chromium fonts-liberation fonts-noto-color-emoji >/dev/null"
	cmd := exec.Command("sudo", "-n", "sh", "-c", script)
	if os.Geteuid() == 0 {
		cmd = exec.Command("sh", "-c", script)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("installing chromium failed: %v\n%s", err, tail(string(out), 2000))
	}
	if p := look(); p != "" {
		return p, nil
	}
	return "", errors.New("chromium installed but not found on PATH")
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
