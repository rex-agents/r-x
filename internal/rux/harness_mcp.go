package rux

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/rex-agents/r-x/internal/harness"
)

// A Claude Code lead runs its own tools, so the harness gives it the tools that run
// sub-agents as an MCP server on localhost, which only it has the token for.

type leadMCP struct {
	url, token string
}

func (r *hrun) serveLeadMCP() (*leadMCP, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	m := &leadMCP{url: "http://" + ln.Addr().String() + "/mcp", token: hex.EncodeToString(b)}
	tools := map[string]harness.Tool{}
	var list []map[string]any
	for _, t := range r.leadTools() {
		tools[t.Spec.Name] = t
		list = append(list, map[string]any{"name": t.Spec.Name, "description": t.Spec.Description, "inputSchema": t.Spec.Schema})
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ") != m.token {
			http.Error(w, "forbidden", 403)
			return
		}
		var rpc struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, req.Body, 8<<20)).Decode(&rpc) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		if len(rpc.ID) == 0 {
			w.WriteHeader(202)
			return
		}
		reply := func(result any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": result})
		}
		switch rpc.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(rpc.Params, &p)
			reply(map[string]any{"protocolVersion": cmpOr(p.ProtocolVersion, "2025-06-18"),
				"capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo":   map[string]any{"name": "rux", "version": Version}})
		case "ping":
			reply(map[string]any{})
		case "tools/list":
			reply(map[string]any{"tools": list})
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(rpc.Params, &p)
			t, ok := tools[p.Name]
			out, isErr := "", false
			if !ok {
				out, isErr = "no tool named "+p.Name, true
			} else {
				ctx, cancel := context.WithCancel(r.ctx)
				go func() { <-req.Context().Done(); cancel() }()
				res, err := t.Run(ctx, cmpRaw(p.Arguments))
				cancel()
				out = res
				if err != nil {
					out, isErr = err.Error(), true
				}
			}
			reply(map[string]any{"content": []map[string]any{{"type": "text", "text": cmpOr(out, "(no output)")}}, "isError": isErr})
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID,
				"error": map[string]any{"code": -32601, "message": "method not found"}})
		}
	})
	go func() { _ = http.Serve(ln, h) }()
	go func() { <-r.ctx.Done(); ln.Close() }()
	return m, nil
}

func (m *leadMCP) config() string {
	b, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"rux": map[string]any{
		"type": "http", "url": m.url, "headers": map[string]string{"Authorization": "Bearer " + m.token}}}})
	return string(b)
}

func cmpRaw(b json.RawMessage) json.RawMessage {
	if len(b) == 0 || string(b) == "null" {
		return json.RawMessage("{}")
	}
	return b
}
