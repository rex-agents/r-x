package authtui

import (
	tea "github.com/charmbracelet/bubbletea"

	"strings"
	"testing"
)

func (f *fake) madeProjects() []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]string(nil), f.made...)
}

func TestNewProjectFromGitHub(t *testing.T) {
	f := newFake(t)
	m := boot(t, f, 140, 45)
	press(m, "p")
	if m.pform == nil {
		t.Fatal("p didn't open the new-project modal")
	}
	v := plain(m.View())
	for _, want := range []string{"New project", "GitHub", "Local directory", "acme/web", "acme/api", "acme/newrepo", "private · the site", "p new project"} {
		if want == "p new project" {
			continue // the pack footer is hidden behind the modal
		}
		if !strings.Contains(v, want) {
			t.Errorf("modal lacks %q:\n%s", want, v)
		}
	}
	press(m, "w", "e", "b")
	rows := m.pform.rows()
	if len(rows) != 2 || rows[0].name != "acme/web" || rows[1].name != "zed/webhooks" {
		t.Fatalf("filtered rows = %+v", rows)
	}
	press(m, "down", "up", "down")
	press(m, "enter") // zed/webhooks
	if got := f.madeProjects(); len(got) != 1 || got[0]["repo"] != "zed/webhooks" || got[0]["dir"] != "" {
		t.Fatalf("created = %+v", got)
	}
	if m.pform != nil {
		t.Fatal("the modal stayed open after the project was made")
	}
	if m.focus != colProject || m.selected(colProject).key != "zed-webhooks" {
		t.Fatalf("selection: focus %d, %+v", m.focus, m.selected(colProject))
	}
	if !strings.Contains(plain(m.View()), "zed/webhooks") {
		t.Error("the new project isn't listed")
	}
}

func TestNewProjectFreeFormRepo(t *testing.T) {
	f := newFake(t)
	m := boot(t, f, 140, 45)
	press(m, "p")
	for _, r := range "acme/other" {
		press(m, string(r))
	}
	rows := m.pform.rows()
	if len(rows) == 0 || rows[0].kind != rowFree || rows[0].value != "acme/other" {
		t.Fatalf("rows = %+v", rows)
	}
	press(m, "enter")
	if got := f.madeProjects(); len(got) != 1 || got[0]["repo"] != "acme/other" {
		t.Fatalf("created = %+v", got)
	}
	// A typed name that matches a listed repo exactly doesn't add a free row.
	press(m, "p")
	for _, r := range "acme/api" {
		press(m, string(r))
	}
	if rows := m.pform.rows(); len(rows) != 1 || rows[0].kind != rowRepo {
		t.Errorf("exact match rows = %+v", rows)
	}
	press(m, "esc")
	if m.pform != nil {
		t.Error("esc didn't close the modal")
	}
}

func TestNewProjectFromLocalDirectory(t *testing.T) {
	f := newFake(t)
	m := boot(t, f, 140, 45)
	press(m, "p", "tab")
	v := plain(m.View())
	for _, want := range []string{"~", "this directory", "code/", "docs/", "notes/"} {
		if !strings.Contains(v, want) {
			t.Errorf("home listing lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, ".config") {
		t.Error("a dot-directory is listed without being asked for")
	}
	// Filter, then descend with enter.
	press(m, "c", "o")
	rows := m.pform.rows()
	if len(rows) != 1 || rows[0].name != "code" {
		t.Fatalf("filtered = %+v", rows)
	}
	press(m, "enter")
	if m.pform.list.Path != "/home/u/code" || m.pform.filter.Value() != "" {
		t.Fatalf("after enter: %q filter %q", m.pform.list.Path, m.pform.filter.Value())
	}
	v = plain(m.View())
	if !strings.Contains(v, "~/code") || !strings.Contains(v, "app/ git") || !strings.Contains(v, "misc/") {
		t.Errorf("code listing:\n%s", v)
	}
	// Up and down again with backspace and right.
	press(m, "backspace")
	if m.pform.list.Path != "/home/u" {
		t.Fatalf("backspace went to %q", m.pform.list.Path)
	}
	press(m, "down", "right")
	if m.pform.list.Path != "/home/u/code" {
		t.Fatalf("right went to %q", m.pform.list.Path)
	}
	// ctrl+a adds the highlighted checkout: rows are [this directory, app, lib, misc].
	press(m, "down", "ctrl+a")
	got := f.madeProjects()
	if len(got) != 1 || got[0]["dir"] != "/home/u/code/app" || got[0]["repo"] != "" {
		t.Fatalf("created = %+v", got)
	}
	if m.pform != nil || m.selected(colProject).key != "app" {
		t.Fatalf("modal %v, selected %+v", m.pform, m.selected(colProject))
	}
	// The card and the preview show the path, with ~ for home.
	v = plain(m.View())
	if !strings.Contains(v, "~/code/app") {
		t.Errorf("the dir project's card lacks its path:\n%s", v)
	}
	if lines := strings.Join(m.previewLines(m.selected(colProject), 40), "\n"); !strings.Contains(plain(lines), "~/code/app") {
		t.Errorf("preview:\n%s", lines)
	}
}

func TestNewProjectInlineErrorAndPathEntry(t *testing.T) {
	f := newFake(t)
	m := boot(t, f, 140, 45)
	press(m, "p", "tab")
	// "this directory" is the home directory: not a git repository.
	press(m, "enter")
	if m.pform == nil || !strings.Contains(m.pform.err, "not a git repository; run git init and commit first") {
		t.Fatalf("err = %v", m.pform)
	}
	if v := plain(m.View()); !strings.Contains(v, "✗ /home/u is not a git repository") {
		t.Errorf("error not shown inline:\n%s", v)
	}
	// Typing clears it. A typed path becomes the first row.
	for _, r := range "~/code/lib" {
		press(m, string(r))
	}
	if m.pform.err != "" {
		t.Error("the error stayed after typing")
	}
	rows := m.pform.rows()
	if rows[0].kind != rowFree || rows[0].value != "~/code/lib" {
		t.Fatalf("rows[0] = %+v", rows[0])
	}
	press(m, "enter")
	got := f.madeProjects()
	if len(got) != 2 || got[1]["dir"] != "~/code/lib" {
		t.Fatalf("created = %+v", got)
	}
	if m.selected(colProject).key != "lib" {
		t.Errorf("selected %+v", m.selected(colProject))
	}
}

func TestNewProjectDotFilterListsHidden(t *testing.T) {
	f := newFake(t)
	m := boot(t, f, 140, 45)
	press(m, "p", "tab", ".")
	if !m.pform.hidden {
		t.Fatal("a dot filter should list hidden directories")
	}
	rows := m.pform.rows()
	if len(rows) != 1 || rows[0].name != ".config" {
		t.Fatalf("rows = %+v", rows)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if last := f.fsPaths[len(f.fsPaths)-1]; last != "/home/u|1" {
		t.Errorf("last request %q", last)
	}
}

func TestNewProjectModalDoesNotFreezeOrLeak(t *testing.T) {
	f := newFake(t)
	m := boot(t, f, 140, 45)
	// The open itself only returns commands; nothing has loaded yet.
	_, cmd := m.Update(keyRunes("p"))
	if cmd == nil || m.pform == nil || m.pform.reposLoaded {
		t.Fatalf("open: cmd %v form %+v", cmd != nil, m.pform)
	}
	if v := plain(m.View()); !strings.Contains(v, "loading your repositories") {
		t.Errorf("no loading state:\n%s", v)
	}
	// An answer for an old directory request is dropped.
	m.pform.tab = projLocal
	m.fetchDirs(m.pform, "", false)
	m.fetchDirs(m.pform, "/home/u/code", false)
	m.projUpdate(dirsMsg{seq: 1, list: DirListing{Path: "/stale"}})
	if m.pform.haveDir {
		t.Error("a stale directory answer was taken")
	}
	// Typing p inside the modal is just text; the thread form's keys don't open over it.
	press(m, "esc", "n")
	if m.form == nil || m.pform != nil {
		t.Errorf("n after esc: form %v pform %v", m.form, m.pform)
	}
}

func keyRunes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }
