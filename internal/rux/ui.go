package rux

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// Agents show UI in their thread with `rux ui`: a json-render style spec
// (https://json-render.dev) whose element types come from the catalog below,
// rendered by the app (web/ui.js) with its own design system. A spec sent with
// --ask is a question: the user's answer goes back to the agent as a message.

// uiCatalog lists the component types the app renders. Keep in sync with
// web/ui.js and catalogDoc in share.go.
var uiCatalog = map[string]bool{
	"Card": true, "Stack": true, "Grid": true, "Tabs": true, "Heading": true, "Text": true, "Badge": true, "Divider": true,
	"Stat": true, "Meter": true, "Table": true, "List": true, "KeyValue": true, "Timeline": true,
	"Chart": true, "Graph": true,
	"Choice": true, "TextInput": true, "Toggle": true, "Select": true, "Button": true, "Buttons": true,
	"Code": true, "Diff": true, "Image": true,
}

type uiElement struct {
	Type     string          `json:"type"`
	Props    json.RawMessage `json:"props,omitempty"`
	Children json.RawMessage `json:"children,omitempty"`
}

type uiSpec struct {
	Root     string                     `json:"root"`
	Elements map[string]json.RawMessage `json:"elements"`
	State    json.RawMessage            `json:"state,omitempty"`
}

// normalizeSpec accepts the json-render flat form ({root, elements}) or a
// nested tree ({type, props, children: [...]}) and returns the flat form.
func normalizeSpec(raw json.RawMessage) (json.RawMessage, error) {
	var probe struct {
		Root     string          `json:"root"`
		Elements json.RawMessage `json:"elements"`
		Type     string          `json:"type"`
		State    json.RawMessage `json:"state"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("spec isn't a JSON object: %v", err)
	}
	if probe.Root == "" && probe.Type != "" { // nested tree
		flat := uiSpec{Elements: map[string]json.RawMessage{}, State: probe.State}
		n := 0
		var walk func(json.RawMessage) (string, error)
		walk = func(node json.RawMessage) (string, error) {
			var e struct {
				Type     string            `json:"type"`
				Props    json.RawMessage   `json:"props,omitempty"`
				Children []json.RawMessage `json:"children,omitempty"`
				Visible  json.RawMessage   `json:"visible,omitempty"`
				On       json.RawMessage   `json:"on,omitempty"`
			}
			if err := json.Unmarshal(node, &e); err != nil {
				return "", err
			}
			n++
			id := fmt.Sprintf("e%d", n)
			var kids []string
			for _, c := range e.Children {
				k, err := walk(c)
				if err != nil {
					return "", err
				}
				kids = append(kids, k)
			}
			out := map[string]any{"type": e.Type}
			if len(e.Props) > 0 {
				out["props"] = e.Props
			}
			if len(kids) > 0 {
				out["children"] = kids
			}
			if len(e.Visible) > 0 {
				out["visible"] = e.Visible
			}
			if len(e.On) > 0 {
				out["on"] = e.On
			}
			b, _ := json.Marshal(out)
			flat.Elements[id] = b
			return id, nil
		}
		root, err := walk(raw)
		if err != nil {
			return nil, fmt.Errorf("bad element: %v", err)
		}
		flat.Root = root
		raw, _ = json.Marshal(flat)
	}
	var spec uiSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("bad spec: %v", err)
	}
	if spec.Root == "" || spec.Elements[spec.Root] == nil {
		return nil, fmt.Errorf(`spec needs "root" naming one of its "elements"`)
	}
	var unknown []string
	for id, el := range spec.Elements {
		var e uiElement
		if err := json.Unmarshal(el, &e); err != nil {
			return nil, fmt.Errorf("element %q: %v", id, err)
		}
		if !uiCatalog[e.Type] {
			unknown = append(unknown, fmt.Sprintf("%s (%q)", id, e.Type))
		}
		var kids []string
		if len(e.Children) > 0 && json.Unmarshal(e.Children, &kids) != nil {
			return nil, fmt.Errorf("element %q: children must be a list of element ids", id)
		}
		for _, k := range kids {
			if spec.Elements[k] == nil {
				return nil, fmt.Errorf("element %q has child %q, which isn't in elements", id, k)
			}
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown component types: %s (run `rux ui --catalog`)", strings.Join(unknown, ", "))
	}
	return raw, nil
}

// handleUIPost is on the runner listener: POST /ui?thread=ID with the thread
// token and {"spec": ..., "ask": bool, "title": "..."}.
func (s *server) handleUIPost(w http.ResponseWriter, r *http.Request) {
	t := s.get(r.URL.Query().Get("thread"))
	if t == nil {
		http.Error(w, "forbidden", 403)
		return
	}
	t.mu.Lock()
	tok := t.meta.Token
	t.mu.Unlock()
	if subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")), []byte(tok)) != 1 {
		http.Error(w, "forbidden", 403)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		http.Error(w, "spec too large (2 MB limit; put big data like diffs in files)", 413)
		return
	}
	var req struct {
		Spec  json.RawMessage `json:"spec"`
		Ask   bool            `json:"ask"`
		Title string          `json:"title"`
	}
	if err := json.Unmarshal(body, &req); err != nil || len(req.Spec) == 0 {
		http.Error(w, "expected {\"spec\": ...}", 400)
		return
	}
	spec, err := normalizeSpec(req.Spec)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	id := "ui-" + randHex(4)
	t.event(ruxEv("rux_ui", "ui", id, "title", clipText(req.Title, 200), "ask", req.Ask, "spec", spec))
	writeJSONResp(w, 201, map[string]string{"ui": id})
}

// handleUIAnswer records the user's answer to a UI block (a form submit or a
// button) and sends it to the thread's agent as a message.
func (s *server) handleUIAnswer(w http.ResponseWriter, r *http.Request) {
	t := s.get(r.PathValue("id"))
	if t == nil {
		httpErr(w, 404, "no such thread")
		return
	}
	var req struct {
		Action string          `json:"action"`
		Params json.RawMessage `json:"params,omitempty"`
		State  json.RawMessage `json:"state,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		httpErr(w, 400, "bad JSON")
		return
	}
	ui := r.PathValue("ui")
	req.Action = cmpOr(strings.TrimSpace(req.Action), "submit")
	t.mu.Lock()
	defer t.mu.Unlock()
	title := ""
	found := false
	for _, l := range t.events {
		var se storedEvent
		if json.Unmarshal(l, &se) != nil {
			continue
		}
		var e struct{ Type, UI, Title string }
		if json.Unmarshal(se.Ev, &e) == nil && e.Type == "rux_ui" && e.UI == ui {
			found, title = true, e.Title
		}
	}
	if !found {
		httpErr(w, 404, "no such UI in this thread")
		return
	}
	if s.answerApproval(ui, req.Action) {
		t.appendLocked("rux", 0, ruxEv("rux_ui_answer", "ui", ui, "action", req.Action))
		writeJSONResp(w, 202, map[string]bool{"ok": true})
		return
	}
	if t.send == nil {
		httpErr(w, 409, "the thread's runner isn't connected")
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%sThe user answered your UI %s", wakePrefix, ui)
	if title != "" {
		fmt.Fprintf(&b, " (%q)", title)
	}
	fmt.Fprintf(&b, ": action %q", req.Action)
	if len(req.Params) > 0 && string(req.Params) != "null" {
		fmt.Fprintf(&b, ", params %s", req.Params)
	}
	if len(req.State) > 0 && string(req.State) != "null" && string(req.State) != "{}" {
		fmt.Fprintf(&b, "\nform values: %s", req.State)
	}
	select {
	case t.send <- wireMsg{T: "user", Text: b.String()}:
	default:
		httpErr(w, 503, "runner busy")
		return
	}
	t.appendLocked("rux", 0, ruxEv("rux_ui_answer", "ui", ui, "action", req.Action, "params", req.Params, "state", req.State))
	writeJSONResp(w, 202, map[string]bool{"ok": true})
}
