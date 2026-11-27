package rux

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The model picker lists what Claude Code itself offers this account: it asks
// a short-lived `claude -p` on the host with the stream-json list_models
// control request, and caches the answer.
type claudeModel struct {
	Value       string   `json:"value"`
	Resolved    string   `json:"resolved,omitempty"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Efforts     []string `json:"efforts,omitempty"`
}

var allEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// fallbackModels is used when the host has no working claude to ask.
var fallbackModels = []claudeModel{
	{Value: "default", Name: "Default", Efforts: allEfforts},
	{Value: "opus", Name: "Opus", Efforts: allEfforts},
	{Value: "sonnet", Name: "Sonnet", Efforts: allEfforts},
	{Value: "haiku", Name: "Haiku"},
}

type modelCache struct {
	claudeBin, dir string

	mu     sync.Mutex
	models []claudeModel
	at     time.Time
	err    error
}

func (c *modelCache) list() ([]claudeModel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.models != nil && time.Since(c.at) < time.Hour {
		return c.models, c.err
	}
	models, err := queryModels(c.claudeBin, c.dir)
	if err != nil || len(models) == 0 {
		if err == nil {
			err = errors.New("claude listed no models")
		}
		// Keep an older good list if there is one; retry in a few minutes.
		if c.models == nil || c.err != nil {
			c.models = fallbackModels
		}
		c.err, c.at = err, time.Now().Add(-time.Hour+5*time.Minute)
		return c.models, err
	}
	c.models, c.err, c.at = models, nil, time.Now()
	return models, nil
}

func queryModels(claudeBin, dir string) ([]claudeModel, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	os.MkdirAll(dir, 0o700)
	cmd := exec.CommandContext(ctx, claudeBin, "-p", "--input-format", "stream-json", "--output-format", "stream-json",
		"--verbose", "--no-session-persistence")
	cmd.Dir = dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() { stdin.Close(); cmd.Process.Kill(); cmd.Wait() }()
	stdin.Write([]byte(`{"type":"control_request","request_id":"rux-models","request":{"subtype":"list_models"}}` + "\n"))
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		var resp struct {
			Type     string
			Response struct {
				Subtype   string
				RequestID string `json:"request_id"`
				Error     string
				Response  struct {
					Models []struct {
						Value, ResolvedModel, DisplayName, Description string
						SupportsEffort                                 bool
						SupportedEffortLevels                          []string
					}
				}
			}
		}
		if json.Unmarshal(sc.Bytes(), &resp) != nil || resp.Type != "control_response" || resp.Response.RequestID != "rux-models" {
			continue
		}
		if resp.Response.Subtype != "success" {
			return nil, errors.New("list_models: " + resp.Response.Error)
		}
		var out []claudeModel
		for _, m := range resp.Response.Response.Models {
			cm := claudeModel{Value: m.Value, Resolved: m.ResolvedModel, Name: m.DisplayName, Description: m.Description}
			if m.SupportsEffort {
				cm.Efforts = m.SupportedEffortLevels
			}
			out = append(out, cm)
		}
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("claude exited without listing models")
}

// defaultModel is what "default" means: the model Claude Code on this host
// picks for the account. Guests only have a token, and their claude can pick a
// different default (Sonnet instead of Opus), so rux passes this explicitly.
func (s *server) defaultModel() string {
	models, _ := s.models.list()
	resolved := ""
	for _, m := range models {
		if m.Value == "default" {
			resolved = m.Resolved
		}
	}
	if resolved == "" {
		return ""
	}
	for _, m := range models { // prefer the alias (opus) over the full ID
		if m.Value != "default" && m.Resolved == resolved && !strings.HasPrefix(m.Value, "claude-") {
			return m.Value
		}
	}
	return resolved
}

// effectiveModel is the model to run a thread with.
func (s *server) effectiveModel(model string) string { return cmpOr(model, s.defaultModel()) }

func (s *server) handleModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.models.list()
	resp := map[string]any{"models": models}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSONResp(w, 200, resp)
}

var modelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\[\]-]{0,63}$`)

// validSettings normalizes a model and effort; empty means Claude's default.
func validSettings(model, effort string) (string, string, error) {
	model, effort = strings.TrimSpace(model), strings.TrimSpace(effort)
	if model == "default" {
		model = ""
	}
	if model != "" && !modelRe.MatchString(model) {
		return "", "", errors.New("invalid model")
	}
	if effort != "" {
		ok := false
		for _, e := range allEfforts {
			ok = ok || e == effort
		}
		if !ok {
			return "", "", errors.New("invalid effort")
		}
	}
	return model, effort, nil
}

// handleSettings changes a thread's model and effort. They're kept in the
// thread and sent to the runner now if it's connected, or when it next is.
func (s *server) handleSettings(w http.ResponseWriter, r *http.Request) {
	t := s.get(r.PathValue("id"))
	if t == nil {
		httpErr(w, 404, "no such thread")
		return
	}
	var req struct{ Model, Effort string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, "bad JSON")
		return
	}
	model, effort, err := validSettings(req.Model, req.Effort)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if model == t.meta.Model && effort == t.meta.Effort {
		writeJSONResp(w, 200, map[string]bool{"ok": true})
		return
	}
	t.meta.Model, t.meta.Effort = model, effort
	t.saveMeta()
	t.appendLocked("rux", 0, ruxEv("rux_settings", "model", model, "effort", effort))
	if t.send != nil {
		select {
		case t.send <- s.settingsMsg(&t.meta):
		default:
		}
	}
	writeJSONResp(w, 200, map[string]bool{"ok": true})
}

// settingsMsg is what the runner needs to start claude the way the thread and
// its project say. Safe to call with the thread's lock held.
func (s *server) settingsMsg(m *threadMeta) wireMsg {
	return wireMsg{T: "settings", Info: map[string]string{"model": s.effectiveModel(m.Model), "effort": m.Effort, "notes": s.projectNotes(m.Project), "project": s.projectName(m.Project)}}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
