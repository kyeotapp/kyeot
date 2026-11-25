package hermesbridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

// A failed write drops that connection. A following task.end does not overtake the earlier reply and queues behind it.
func TestSendFailureDropsConn(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			if _, _, err := c.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	defer ts.Close()
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	b := New(Config{})
	b.conn = conn
	conn.CloseNow() // the next write fails
	if b.send(protocol.AgentMessage{Type: "message", Task: "t1", Text: "reply"}) {
		t.Fatal("claims to have sent on a closed connection")
	}
	b.mu.Lock()
	held := b.conn
	b.mu.Unlock()
	if held != nil {
		t.Fatal("still holds the connection after a write failure")
	}
	b.send(protocol.AgentMessage{Type: "task.end", Task: "t1"})
	if len(b.pending) != 2 || b.pending[0].Type != "message" || b.pending[1].Type != "task.end" {
		t.Fatalf("queue order: %+v", b.pending)
	}
}
