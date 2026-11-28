package authtui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The "new project" modal (p on the PACK tab): add a project from one of the user's GitHub
// repositories, or from a git checkout on the authority's machine, picked in a directory
// browser. Every request runs as a tea.Cmd, so the UI never waits on the network.

const (
	projGitHub = iota
	projLocal
)

var projTabNames = []string{"GitHub", "Local directory"}

type projForm struct {
	tab    int
	filter textinput.Model
	err    string // shown inline, in red
	busy   bool   // the create request is in flight

	// GitHub
	repos       []Repo
	reposLoaded bool
	reposNote   string // why the list is empty: GitHub access is off, or the listing failed
	rsel        int

	// Local directory
	list    DirListing
	haveDir bool
	loading bool
	hidden  bool // the listing in view includes dot-directories
	dsel    int
	seq     int // the newest directory request; older answers are dropped
}

// Messages of the modal.
type (
	reposMsg struct {
		repos   []Repo
		enabled bool
		note    string
		err     error
	}
	dirsMsg struct {
		seq    int
		list   DirListing
		hidden bool
		err    error
	}
	projectMsg struct {
		p   Project
		err error
	}
)

const (
	rowFree = iota // what was typed, taken as is
	rowRepo
	rowHere // the directory being browsed
	rowDir
)

type projRow struct {
	kind  int
	name  string
	value string // the repo or path a row stands for
	hint  string
	git   bool
}

func (m *Model) openProjForm() tea.Cmd {
	in := textinput.New()
	in.Prompt = "❯ "
	in.Placeholder = "filter, or type owner/name or a URL"
	in.Focus()
	m.pform = &projForm{filter: in}
	return tea.Batch(textinput.Blink, m.fetchRepos())
}

func (m *Model) fetchRepos() tea.Cmd {
	return func() tea.Msg {
		repos, enabled, note, err := m.c.Repos()
		return reposMsg{repos, enabled, note, err}
	}
}

func (m *Model) fetchDirs(f *projForm, path string, hidden bool) tea.Cmd {
	f.seq++
	f.loading = true
	seq := f.seq
	return func() tea.Msg {
		l, err := m.c.FsDirs(path, hidden)
		return dirsMsg{seq, l, hidden, err}
	}
}

func (m *Model) createProject(f *projForm, repo, dir string) tea.Cmd {
	f.busy, f.err = true, ""
	return func() tea.Msg {
		p, err := m.c.CreateProject(repo, dir)
		return projectMsg{p, err}
	}
}

// looksLikeRepo: something typed that names a repository rather than filtering the list.
func looksLikeRepo(q string) bool { return strings.ContainsAny(q, "/:@") }

func pathLike(q string) bool { return strings.HasPrefix(q, "/") || strings.HasPrefix(q, "~") }

// rows is what the list shows for the filter as typed.
func (f *projForm) rows() []projRow {
	q := trim(f.filter.Value())
	lq := strings.ToLower(q)
	var out []projRow
	if f.tab == projGitHub {
		exact := false
		for _, r := range f.repos {
			exact = exact || strings.EqualFold(r.Name, q)
		}
		if q != "" && looksLikeRepo(q) && !exact {
			out = append(out, projRow{kind: rowFree, name: q, value: q, hint: "use as typed"})
		}
		for _, r := range f.repos {
			if lq != "" && !strings.Contains(strings.ToLower(r.Name+" "+r.Description), lq) {
				continue
			}
			hint := r.Description
			if r.Private {
				hint = strings.TrimSpace("private · " + hint)
			}
			out = append(out, projRow{kind: rowRepo, name: r.Name, value: r.Name, hint: hint})
		}
		return out
	}
	if pathLike(q) {
		out = append(out, projRow{kind: rowFree, name: q, value: q, hint: "use this path"})
	} else if q == "" && f.haveDir {
		out = append(out, projRow{kind: rowHere, name: "this directory", value: f.list.Path, git: f.list.Git})
	}
	for _, d := range f.list.Dirs {
		if !pathLike(q) && lq != "" && !strings.Contains(strings.ToLower(d.Name), lq) {
			continue
		}
		out = append(out, projRow{kind: rowDir, name: d.Name, value: d.Path, git: d.Git})
	}
	return out
}

func (f *projForm) sel() *int {
	if f.tab == projGitHub {
		return &f.rsel
	}
	return &f.dsel
}

func (f *projForm) clampSel() {
	n := len(f.rows())
	s := f.sel()
	*s = min(max(*s, 0), max(n-1, 0))
}

func (m *Model) projKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.pform
	s := k.String()
	if s == "esc" {
		m.pform = nil
		return m, nil
	}
	if f.busy {
		return m, nil
	}
	rows := f.rows()
	f.clampSel()
	sel := f.sel()
	switch s {
	case "tab", "shift+tab":
		f.tab = 1 - f.tab
		f.err = ""
		f.filter.SetValue("")
		if f.tab == projGitHub {
			f.filter.Placeholder = "filter, or type owner/name or a URL"
			return m, nil
		}
		f.filter.Placeholder = "filter, or type a path such as ~/code/app"
		if !f.haveDir && !f.loading {
			return m, m.fetchDirs(f, "", false)
		}
		return m, nil
	case "up", "ctrl+p":
		*sel = max(*sel-1, 0)
		return m, nil
	case "down", "ctrl+n":
		*sel = min(*sel+1, max(len(rows)-1, 0))
		return m, nil
	case "pgup":
		*sel = max(*sel-8, 0)
		return m, nil
	case "pgdown":
		*sel = min(*sel+8, max(len(rows)-1, 0))
		return m, nil
	case "enter":
		if len(rows) == 0 {
			if f.tab == projGitHub {
				f.err = "no repository matches; type owner/name or a URL"
				if trim(f.filter.Value()) == "" {
					f.err = "pick a repository, or type owner/name or a URL"
				}
			}
			return m, nil
		}
		r := rows[*sel]
		switch {
		case f.tab == projGitHub:
			return m, m.createProject(f, r.value, "")
		case r.kind == rowDir:
			return m, m.descend(f, r.value)
		default:
			return m, m.createProject(f, "", r.value)
		}
	case "ctrl+a": // choose the highlighted directory without opening it
		if f.tab == projLocal && len(rows) > 0 {
			return m, m.createProject(f, "", rows[*sel].value)
		}
		return m, nil
	case "left", "backspace":
		if f.tab == projLocal && f.filter.Value() == "" {
			if f.list.Parent != "" && !f.loading {
				return m, m.descend(f, f.list.Parent)
			}
			return m, nil
		}
	case "right":
		if f.tab == projLocal && f.filter.Value() == "" && len(rows) > 0 && rows[*sel].kind == rowDir {
			return m, m.descend(f, rows[*sel].value)
		}
	}
	before := f.filter.Value()
	var cmd tea.Cmd
	f.filter, cmd = f.filter.Update(k)
	if f.filter.Value() != before {
		f.err, *sel = "", 0
		// A filter that starts with a dot looks for dot-directories, which are listed on request.
		if want := strings.HasPrefix(f.filter.Value(), "."); f.tab == projLocal && f.haveDir && want != f.hidden && !f.loading {
			return m, tea.Batch(cmd, m.fetchDirs(f, f.list.Path, want))
		}
	}
	return m, cmd
}

// descend opens a directory in the browser. The filter starts empty there.
func (m *Model) descend(f *projForm, path string) tea.Cmd {
	f.filter.SetValue("")
	f.err, f.dsel = "", 0
	return m.fetchDirs(f, path, false)
}

func (m *Model) projUpdate(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case reposMsg:
		if f := m.pform; f != nil {
			f.reposLoaded = true
			switch {
			case msg.err != nil:
				f.reposNote = "couldn't list your repositories (" + errMessage(msg.err) + ")"
			case !msg.enabled:
				f.reposNote = "GitHub access is off on this authority"
			case msg.note != "" && len(msg.repos) == 0:
				f.reposNote = "couldn't list your repositories (" + msg.note + ")"
			default:
				f.repos = msg.repos
			}
			f.clampSel()
		}
		return nil, true
	case dirsMsg:
		f := m.pform
		if f == nil || msg.seq != f.seq {
			return nil, true
		}
		f.loading = false
		if msg.err != nil {
			f.err = errMessage(msg.err)
			return nil, true
		}
		f.list, f.haveDir, f.hidden = msg.list, true, msg.hidden
		if m.home == "" {
			m.home = msg.list.Home
		}
		f.clampSel()
		return nil, true
	case projectMsg:
		f := m.pform
		if msg.err != nil {
			if f != nil {
				f.busy, f.err = false, errMessage(msg.err)
			} else {
				m.flash = "new project failed: " + errMessage(msg.err)
			}
			return nil, true
		}
		m.pform = nil
		m.projects[msg.p.ID] = msg.p
		m.clamp()
		m.selectProject(msg.p.ID)
		m.flash = "project " + cmpOr(msg.p.Name, msg.p.ID) + " added"
		return m.wantPreview(), true
	}
	return nil, false
}

// selectProject puts the PACK cursor on a project.
func (m *Model) selectProject(id string) {
	m.tab, m.focus = tabPack, colProject
	for i, n := range m.column(colProject) {
		if n.key == id {
			m.sel[colProject] = i
		}
	}
	for c := colProject + 1; c < numCols; c++ {
		m.sel[c] = 0
	}
	m.clamp()
}

// tildePath shows a path inside home as ~/…
func tildePath(p, home string) string {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home != "" && (p == home || strings.HasPrefix(p, home+string(filepath.Separator))) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// tailFit keeps the end of a path when it doesn't fit: …/code/app.
func tailFit(s string, w int) string {
	if lipgloss.Width(s) <= w || w < 2 {
		return s
	}
	r := []rune(s)
	return "…" + string(r[len(r)-(w-1):])
}

func listWindow(sel, total, n int) (int, int) {
	start := min(max(sel-n/2, 0), max(total-n, 0))
	return start, min(start+n, total)
}

func (m *Model) projFormView() string {
	f := m.pform
	w := min(m.w-4, 78)
	inner := w - 6
	var tabs []string
	for i, n := range projTabNames {
		if i == f.tab {
			tabs = append(tabs, stAccent.Bold(true).Render("● "+n))
		} else {
			tabs = append(tabs, stDim.Render("○ "+n))
		}
	}
	body := []string{stBold.Render("New project") + stDim.Render("  from a GitHub repository or a directory on this machine"), "",
		strings.Join(tabs, "   "), ""}
	if f.tab == projLocal {
		where := "loading…"
		if f.haveDir {
			where = tailFit(tildePath(f.list.Path, m.home), inner-4)
			if f.list.Git {
				where += stGit.Render("  git")
			}
		}
		body = append(body, stFaint.Render("in ")+stText.Render(where), "")
	}
	body = append(body, f.filter.View(), "")

	rows := f.rows()
	n := max(3, min(10, m.h-20))
	start, end := listWindow(*f.sel(), len(rows), n)
	for i := start; i < end; i++ {
		r := rows[i]
		mark, name := "  ", stText.Render(ansi.Truncate(r.name, inner-4, "…"))
		if i == *f.sel() {
			mark, name = stAccent.Render("▸ "), stBold.Render(ansi.Truncate(r.name, inner-4, "…"))
		}
		line := mark + name
		switch r.kind {
		case rowDir:
			line = mark + name + stDim.Render("/")
		case rowHere:
			line = mark + stDim.Render("• ") + name
		}
		if r.git {
			line += " " + stGit.Render("git")
		}
		if r.hint != "" {
			room := inner - lipgloss.Width(line) - 2
			if room > 6 {
				line += "  " + stFaint.Render(ansi.Truncate(r.hint, room, "…"))
			}
		}
		body = append(body, line)
	}
	if len(rows) > end-start {
		body = append(body, stFaint.Render(fmt.Sprintf("  %d–%d of %d", start+1, end, len(rows))))
	}
	body = append(body, f.statusLine(len(rows)))
	if f.err != "" {
		body = append(body, "", lipgloss.NewStyle().Foreground(hRed).Render(ansi.Truncate("✗ "+f.err, inner*2, "…")))
	}
	hint := "enter adds · tab switches · esc cancels"
	if f.tab == projLocal {
		hint = "enter opens · ctrl+a adds · ← up · tab switches · esc cancels"
	}
	body = append(body, "", stDim.Render(hint))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(hAccent).Padding(1, 2).
		Width(w).Render(strings.Join(body, "\n"))
}

// statusLine says what the list is waiting for or why it's empty.
func (f *projForm) statusLine(rows int) string {
	switch {
	case f.busy:
		return stFaint.Render("  adding the project…")
	case f.tab == projGitHub && !f.reposLoaded:
		return stFaint.Render("  loading your repositories…")
	case f.tab == projGitHub && f.reposNote != "":
		return stFaint.Render("  " + f.reposNote + "; type owner/name or a URL")
	case f.tab == projLocal && f.loading:
		return stFaint.Render("  loading…")
	case f.tab == projLocal && f.haveDir && f.list.Truncated:
		return stFaint.Render("  showing the first folders only; type to filter")
	case rows == 0 && f.tab == projLocal && f.haveDir:
		return stFaint.Render("  no folders here")
	}
	return ""
}
