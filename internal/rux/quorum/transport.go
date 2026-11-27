package quorum

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/raft"
)

// streamLayer carries raft's RPCs over WebSocket on /quorum/raft, so the quorum link is the
// same authenticated HTTP(S) listener as the rest of /quorum/*.
type streamLayer struct {
	self   string // our URL, raft's advertised address
	header func() http.Header
	client *http.Client
	accept chan net.Conn
	done   chan struct{}
	once   sync.Once
}

func newStreamLayer(self string, header func() http.Header, client *http.Client) *streamLayer {
	return &streamLayer{self: self, header: header, client: client, accept: make(chan net.Conn), done: make(chan struct{})}
}

func (s *streamLayer) Accept() (net.Conn, error) {
	select {
	case c := <-s.accept:
		return c, nil
	case <-s.done:
		return nil, errors.New("closed")
	}
}

func (s *streamLayer) Close() error { s.once.Do(func() { close(s.done) }); return nil }

type addr string

func (a addr) Network() string { return "quorum" }
func (a addr) String() string  { return string(a) }

func (s *streamLayer) Addr() net.Addr { return addr(s.self) }

func (s *streamLayer) Dial(address raft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c, _, err := websocket.Dial(ctx, wsURL(string(address))+"/quorum/raft", &websocket.DialOptions{HTTPHeader: s.header(), HTTPClient: s.client})
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(64 << 20)
	return websocket.NetConn(context.Background(), c, websocket.MessageBinary), nil
}

// serve hands an authenticated raft connection to the transport and blocks until it closes.
func (s *streamLayer) serve(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(64 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	conn := &closeNotify{Conn: websocket.NetConn(ctx, c, websocket.MessageBinary), done: make(chan struct{}), cancel: cancel}
	select {
	case s.accept <- conn:
		<-conn.done
	case <-s.done:
		c.Close(websocket.StatusGoingAway, "closing")
	case <-time.After(5 * time.Second):
		c.Close(websocket.StatusTryAgainLater, "busy")
	}
}

type closeNotify struct {
	net.Conn
	done   chan struct{}
	cancel context.CancelFunc
	once   sync.Once
}

func (c *closeNotify) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.done); c.cancel() })
	return err
}

func wsURL(u string) string {
	switch {
	case strings.HasPrefix(u, "https://"):
		return "wss://" + u[len("https://"):]
	case strings.HasPrefix(u, "http://"):
		return "ws://" + u[len("http://"):]
	}
	return "ws://" + u
}
