package rux

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/coder/websocket"
)

// wireMsg is the single envelope used on the runner <-> server WebSocket.
//
// runner -> server:
//
//	hello  {info}          first message after connecting
//	event  {seq, ev}       one event; ev is a claude stream-json line or a rux_* event
//
// server -> runner:
//
//	sync      {have}       reply to hello: highest seq the server has stored
//	user      {text}       a user message for claude
//	interrupt {}           SIGINT the running claude turn
type wireMsg struct {
	T    string            `json:"t"`
	Seq  int64             `json:"seq,omitempty"`
	Ev   json.RawMessage   `json:"ev,omitempty"`
	Have int64             `json:"have"`
	Text string            `json:"text,omitempty"`
	Info map[string]string `json:"info,omitempty"`
	// Images the user attached to a "user" message.
	Files []attachment `json:"files,omitempty"`
}

type attachment struct {
	Name string `json:"name"`
	Mime string `json:"mime"`
	Data []byte `json:"data"` // base64 in JSON
	URL  string `json:"url"`  // where the app shows it
}

const wsReadLimit = 256 << 20 // tool results can be large

// ruxEv builds a rux-generated event: {"type": typ, k1: v1, ...}.
func ruxEv(typ string, kv ...any) json.RawMessage {
	m := map[string]any{"type": typ}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	b, _ := json.Marshal(m)
	return b
}

func writeJSON(ctx context.Context, c *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, b)
}

func readJSON(ctx context.Context, c *websocket.Conn, v any) error {
	_, b, err := c.Read(ctx)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
