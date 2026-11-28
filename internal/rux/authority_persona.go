package rux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/rex-agents/r-x/internal/rux/authapi"
)

// The persona is who an authority is: the name its user gave it and the mascot a model drew for
// it. The name lives in ~/.rux/authority.json ("name") and the mascot in ~/.rux/mascot.json. An
// authority with no name hasn't been through the first-run setup, so clients show the setup screen.
//
//	GET    /api/persona          the name, whether it's set up, and the mascot (if any)
//	PUT    /api/persona/name     {"name": "jarvis"}: lowercase letters, digits, hyphens, 1-32
//	POST   /api/persona/mascot   {"prompt": "...", "save": false}: draw a mascot, preview or keep
//	PUT    /api/persona/mascot   a Mascot to keep (a preview the user accepted)
//	DELETE /api/persona/mascot   back to the T-rex

var personaNameRe = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// mascotModel is the model that draws mascots: fast, and good enough for a few lines of dots.
const mascotModel = "claude-haiku-5-5"

// mascotMaxTokens leaves room for a 40x16 mask in JSON and a few words around it.
const mascotMaxTokens = 8000

// checkPersonaName says why name can't be an authority's name, or "" when it can.
func checkPersonaName(name string) string {
	if !personaNameRe.MatchString(name) {
		return "a name is 1 to 32 lowercase letters, digits and hyphens, like jarvis or dev-box"
	}
	if name == "you" { // agents' transcripts call the user "you"
		return "\"you\" is how agents refer to the user; pick another name"
	}
	return ""
}

// personaName is the name the user chose, or "" before the first-run setup.
func (s *server) personaName() string {
	s.nameMu.RLock()
	defer s.nameMu.RUnlock()
	return s.name
}

func (s *server) persona() authapi.Persona {
	h, _ := os.Hostname()
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	p := authapi.Persona{Name: s.personaName(), HostName: h, Mascot: loadMascot(s.dataDir),
		Default: &authapi.Mascot{Mask: trexMask, Colors: defaultMascotColors}}
	p.Setup = p.Name != ""
	return p
}

func (s *server) handlePersona(w http.ResponseWriter, r *http.Request) {
	writeJSONResp(w, 200, s.persona())
}

func (s *server) handlePersonaName(w http.ResponseWriter, r *http.Request) {
	var req authapi.PersonaName
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		httpErr(w, 400, "bad request")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if msg := checkPersonaName(req.Name); msg != "" {
		httpErr(w, 400, msg)
		return
	}
	if err := s.setPersonaName(req.Name); err != nil {
		var re *reqError
		if errors.As(err, &re) {
			httpErr(w, re.code, re.msg)
			return
		}
		httpErr(w, 500, err.Error())
		return
	}
	s.personaChanged()
	writeJSONResp(w, 200, s.persona())
}

// setPersonaName names the authority, live: it's saved in authority.json, and the pack code,
// which asks authName each time, uses it from now on. An authority that's in a pack keeps its
// name, since the other members know it by that name; leaving the pack first lets it be renamed.
func (s *server) setPersonaName(name string) error {
	if s.node != nil && s.node.InPack() {
		if cur := s.node.Name(); cur != name {
			return &reqError{409, fmt.Sprintf("this authority is in a pack as %q, and the other members know it by that name. "+
				"Run `rux quorum leave`, rename it, then join the pack again", cur)}
		}
	}
	if s.node != nil && !s.node.InPack() {
		if err := s.node.SetName(name); err != nil {
			return &reqError{409, err.Error()}
		}
	}
	if err := saveAuthorityName(s.dataDir, name); err != nil {
		return err
	}
	s.nameMu.Lock()
	s.name = name
	s.nameMu.Unlock()
	return nil
}

// saveAuthorityName writes the name into authority.json, keeping its other settings.
func saveAuthorityName(dir, name string) error {
	cfg := map[string]json.RawMessage{}
	if b, err := os.ReadFile(authorityPath(dir, "json")); err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return fmt.Errorf("%s isn't valid JSON (%v); fix it, then rename", authorityPath(dir, "json"), err)
		}
	}
	cfg["name"], _ = json.Marshal(name)
	b, _ := json.MarshalIndent(cfg, "", "  ")
	return writeFileAtomic(authorityPath(dir, "json"), append(b, '\n'))
}

func (s *server) handleMascotGenerate(w http.ResponseWriter, r *http.Request) {
	var req authapi.MascotRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil {
		httpErr(w, 400, "bad request")
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		httpErr(w, 400, "describe what your agent should look like")
		return
	}
	if len(req.Prompt) > 1000 {
		httpErr(w, 400, "keep the description under 1000 characters")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	m, err := s.generateMascot(ctx, req.Prompt)
	if err != nil {
		httpErr(w, 502, "couldn't draw the mascot: "+err.Error()+". The T-rex stays")
		return
	}
	if req.Save {
		if err := saveMascot(s.dataDir, m); err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		s.personaChanged()
	}
	writeJSONResp(w, 200, m)
}

func (s *server) handleMascotSave(w http.ResponseWriter, r *http.Request) {
	var m authapi.Mascot
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&m); err != nil {
		httpErr(w, 400, "bad request")
		return
	}
	if err := validateMascot(&m); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if m.Created == "" {
		m.Created = time.Now().UTC().Format(time.RFC3339)
	}
	if err := saveMascot(s.dataDir, m); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.personaChanged()
	writeJSONResp(w, 200, m)
}

func (s *server) handleMascotReset(w http.ResponseWriter, r *http.Request) {
	if err := os.Remove(mascotPath(s.dataDir)); err != nil && !os.IsNotExist(err) {
		httpErr(w, 500, err.Error())
		return
	}
	s.personaChanged()
	writeJSONResp(w, 200, s.persona())
}

// generateMascot asks the model for a mascot and checks the answer, asking once more, with what
// was wrong, if the first isn't usable.
func (s *server) generateMascot(ctx context.Context, desc string) (authapi.Mascot, error) {
	ask := s.askModel
	if ask == nil {
		ask = s.askAnthropic
	}
	system := fmt.Sprintf(mascotSystem, strings.Join(trexMask, "\n"))
	user := mascotPrompt(desc)
	var lastErr error
	for try := 0; try < 2; try++ {
		reply, model, err := ask(ctx, system, user)
		if err != nil {
			return authapi.Mascot{}, err
		}
		m, err := parseMascot(reply)
		if err == nil {
			m.Prompt, m.Model, m.Created = desc, model, time.Now().UTC().Format(time.RFC3339)
			return m, nil
		}
		log.Printf("mascot: model %s, reply rejected (%v): %q", model, err, truncate(reply, 300))
		lastErr = err
		user = mascotPrompt(desc) + "\n\nYour last reply was rejected: " + err.Error() + ". Reply with only the JSON object, fixed."
	}
	return authapi.Mascot{}, lastErr
}

// askAnthropic sends one message to Claude with the authority's own model access: the same vault
// source the model proxy uses for threads, so no key ever leaves the authority.
func (s *server) askAnthropic(ctx context.Context, system, user string) (reply, model string, err error) {
	vault := s.vaultFor(&threadMeta{})
	s.mu.Lock()
	src := s.vaults[vault]
	s.mu.Unlock()
	if src == nil {
		return "", "", fmt.Errorf("vault source %q isn't available on this authority", vault)
	}
	cred, err := src.Credential(ctx, "persona", "anthropic")
	if err != nil {
		return "", "", err
	}
	model = cmpOr(os.Getenv("RUX_MASCOT_MODEL"), mascotModel)
	sys := []map[string]string{{"type": "text", "text": system}}
	if cred.Kind == "oauth" { // a Claude Code subscription token must say whom it speaks for
		sys = append([]map[string]string{{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."}}, sys...)
	}
	// No thinking: a mask in JSON is a few thousand tokens, and thinking blocks (on by default for the
	// newer models) share max_tokens with the answer, which can leave no text at all.
	body, _ := json.Marshal(map[string]any{"model": model, "max_tokens": mascotMaxTokens, "system": sys,
		"thinking": map[string]string{"type": "disabled"},
		"messages": []map[string]string{{"role": "user", "content": user}}})
	req, err := http.NewRequestWithContext(ctx, "POST", cmpOr(os.Getenv("RUX_ANTHROPIC_URL"), anthropicHost)+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	injectCredential(req.Header, "anthropic", cred)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error struct{ Type, Message string }
		}
		json.Unmarshal(data, &e)
		log.Printf("mascot: %s answered %s: %s", model, resp.Status, truncate(string(data), 300))
		return "", "", fmt.Errorf("the model %s answered %s%s", model, resp.Status, cmpOr(": "+e.Error.Message, ""))
	}
	var out struct {
		Content    []struct{ Type, Text string }
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", "", err
	}
	var b strings.Builder
	var kinds []string
	for _, c := range out.Content {
		kinds = append(kinds, c.Type)
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	reply = b.String()
	if reply == "" || out.StopReason == "max_tokens" || out.StopReason == "refusal" {
		log.Printf("mascot: %s stop_reason=%q blocks=%v reply=%q", model, out.StopReason, kinds, truncate(reply, 300))
	}
	switch {
	case out.StopReason == "refusal":
		return "", "", fmt.Errorf("the model %s declined to draw that", model)
	case out.StopReason == "max_tokens":
		return "", "", fmt.Errorf("the model %s stopped at max_tokens before finishing the drawing", model)
	case strings.TrimSpace(reply) == "":
		return "", "", fmt.Errorf("the model %s sent no text (stop_reason %q, blocks %v)", model, out.StopReason, kinds)
	}
	return reply, model, nil
}

// truncate shortens s for a log line.
func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
