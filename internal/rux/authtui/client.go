// Package authtui is the rux authority TUI: a node graph of the pack (leader → project agents →
// threads, with a thread's sub-agents one step further) and a monitor of clients, hosts and guests. It is only a client of the authority HTTP
// API (see docs/rux/authority.md): GET /api/pack, the SSE feed GET /api/pack/stream,
// /api/threads and thread events. It holds no private state paths.
package authtui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/rex-agents/r-x/internal/rux/authapi"
)

// Client talks to one authority's HTTP API.
type Client struct {
	Base string
	HTTP *http.Client
	// Device names this machine to the authority (default: the host name).
	Device string
}

// NewClient returns a client for the authority at baseURL.
func NewClient(baseURL string) *Client {
	return &Client{Base: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: 15 * time.Second}}
}

func (c *Client) do(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.Base+path, body)
	if err != nil {
		return err
	}
	c.identify(req)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: %s %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Pack is GET /api/pack.
func (c *Client) Pack() (authapi.Pack, error) {
	var p authapi.Pack
	err := c.do("GET", "/api/pack", nil, &p)
	return p, err
}

// Project is the part of GET /api/projects the TUI shows.
type Project struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Repo     string `json:"repo"`
	Threads  int    `json:"threads"`
	Archived int    `json:"archived_threads"`
}

// Projects is GET /api/projects.
func (c *Client) Projects() ([]Project, error) {
	var ps []Project
	err := c.do("GET", "/api/projects", nil, &ps)
	return ps, err
}

// Threads is GET /api/threads?authority=NAME (empty name: the whole pack).
func (c *Client) Threads(authority string) ([]authapi.ThreadSummary, error) {
	var ts []authapi.ThreadSummary
	path := "/api/threads"
	if authority != "" {
		path += "?authority=" + url.QueryEscape(authority)
	}
	err := c.do("GET", path, nil, &ts)
	return ts, err
}

// StoredEvent is one entry of a thread's event log.
type StoredEvent struct {
	ID int64           `json:"id"`
	TS int64           `json:"ts"`
	Ev json.RawMessage `json:"ev"`
}

// ThreadEvents is GET /api/threads/{id}/events/page: the latest events of a thread.
func (c *Client) ThreadEvents(id string, min int) ([]StoredEvent, error) {
	var out struct {
		Events []StoredEvent `json:"events"`
	}
	err := c.do("GET", fmt.Sprintf("/api/threads/%s/events/page?min=%d", url.PathEscape(id), min), nil, &out)
	return out.Events, err
}

// Send is POST /api/threads/{id}/messages.
func (c *Client) Send(id, text string) error {
	return c.do("POST", "/api/threads/"+url.PathEscape(id)+"/messages", map[string]string{"text": text}, nil)
}

// CreateReq is the body of POST /api/threads.
type CreateReq struct {
	Project   string            `json:"project,omitempty"`
	Prompt    string            `json:"prompt"`
	Placement authapi.Placement `json:"placement"`
	Vault     string            `json:"vault,omitempty"`
}

// CreateThread is POST /api/threads. The response is a thread; only its id is used.
func (c *Client) CreateThread(r CreateReq) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := c.do("POST", "/api/threads", r, &out)
	return out.ID, err
}

// SSE is one server-sent event.
type SSE struct{ Name, Data string }

// Stream follows an SSE endpoint until ctx ends or the connection drops, calling fn per event.
func (c *Client) Stream(ctx context.Context, path string, fn func(SSE)) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.Base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	c.identify(req)
	hc := &http.Client{} // no overall timeout: the stream is long-lived
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	var ev SSE
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if ev.Data != "" {
				fn(ev)
			}
			ev = SSE{}
		case strings.HasPrefix(line, "event:"):
			ev.Name = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			d := strings.TrimPrefix(line[5:], " ")
			if ev.Data != "" {
				ev.Data += "\n"
			}
			ev.Data += d
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.EOF
}

// SubAgentEvents is GET /api/threads/{id}/subagents/{sub}/events/page: a sub-agent's conversation
// (read-only).
func (c *Client) SubAgentEvents(id, sub string, min int) ([]StoredEvent, error) {
	var out struct {
		Events []StoredEvent `json:"events"`
	}
	err := c.do("GET", fmt.Sprintf("/api/threads/%s/subagents/%s/events/page?min=%d", url.PathEscape(id), url.PathEscape(sub), min), nil, &out)
	return out.Events, err
}

// Interrupt is POST /api/threads/{id}/interrupt: it ends the thread's current turn.
func (c *Client) Interrupt(id string) error {
	return c.do("POST", "/api/threads/"+url.PathEscape(id)+"/interrupt", nil, nil)
}

// StartManager is POST /api/projects/{id}/manager: it starts the project's manager with its
// first instruction. The response is the manager thread; only its id is used.
func (c *Client) StartManager(project, text string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := c.do("POST", "/api/projects/"+url.PathEscape(project)+"/manager", map[string]string{"text": text}, &out)
	return out.ID, err
}

// identify tells the authority what this client is, so the pack's monitor lists it as a TUI on
// this machine.
func (c *Client) identify(req *http.Request) {
	req.Header.Set("X-Rux-Client", "tui")
	if c.Device == "" {
		c.Device, _ = os.Hostname()
	}
	req.Header.Set("X-Rux-Device", c.Device)
}
