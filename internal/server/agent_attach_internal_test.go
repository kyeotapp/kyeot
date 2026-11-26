package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/store"
)

// wsPeer makes a real websocket pair and returns the server-side peer and the other end's connection.
func wsPeer(t *testing.T, srv *Server) (*peer, *websocket.Conn) {
	t.Helper()
	got := make(chan *peer, 1)
	hold := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		p := newPeer(srv.ctx, c)
		go p.writeLoop()
		got <- p
		<-hold
	}))
	t.Cleanup(func() { close(hold); ts.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return <-got, conn
}

func closedWith(t *testing.T, conn *websocket.Conn, want websocket.StatusCode) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := conn.Read(ctx)
	if got := websocket.CloseStatus(err); got != want {
		t.Fatalf("close status %d, want %d (%v)", got, want, err)
	}
}

// A release or token rotation slips in between the token check and hub registration. A connection with the old token must not survive.
func TestAttachAfterUnlinkOrRotateIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		race func(srv *Server, id string)
		code websocket.StatusCode
	}{
		{"unlink", func(srv *Server, id string) { srv.hub.UnlinkAgent(store.Owner, id) }, 4001},
		{"rotate", func(srv *Server, id string) { srv.hub.RotateAgentToken(store.Owner, id) }, 4002},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newLocal(t)
			a, token, err := srv.hub.CreateAgent(store.Owner, "race")
			if err != nil {
				t.Fatal(err)
			}
			auth, err := srv.store.AgentByToken(token) // the check serveAgent does first
			if err != nil {
				t.Fatal(err)
			}
			tc.race(srv, a.ID)
			p, conn := wsPeer(t, srv)

			if srv.hub.attachAgent(p, auth, token) {
				t.Fatal("stale token must not attach")
			}
			closedWith(t, conn, tc.code)
			srv.hub.mu.Lock()
			attached := srv.hub.agents[a.ID] != nil
			srv.hub.mu.Unlock()
			if attached {
				t.Fatal("stale peer registered in hub")
			}
			if cur, _ := srv.store.Agent(a.ID); cur.State != protocol.AgentOffline {
				t.Fatalf("state %q, want offline", cur.State)
			}
		})
	}
}

// A message sent late by a replaced or released connection is not processed.
func TestMessagesFromStalePeerAreDropped(t *testing.T) {
	srv := newLocal(t)
	a, token, _ := srv.hub.CreateAgent(store.Owner, "late words")
	auth, _ := srv.store.AgentByToken(token)
	p, _ := wsPeer(t, srv)
	if !srv.hub.attachAgent(p, auth, token) {
		t.Fatal("attach")
	}
	srv.hub.UnlinkAgent(store.Owner, a.ID)
	srv.hub.onAgentMessage(p, auth, []byte(`{"type":"task.open","task":"late-1","title":"after release"}`))
	if _, err := srv.store.Task("late-1"); err == nil {
		t.Fatal("unlinked peer opened a task")
	}

	b, tokB, _ := srv.hub.CreateAgent(store.Owner, "replacement")
	authB, _ := srv.store.AgentByToken(tokB)
	old, _ := wsPeer(t, srv)
	cur, _ := wsPeer(t, srv)
	srv.hub.attachAgent(old, authB, tokB)
	srv.hub.attachAgent(cur, authB, tokB)
	srv.hub.onAgentMessage(old, authB, []byte(`{"type":"task.open","task":"late-2","title":"old connection"}`))
	if _, err := srv.store.Task("late-2"); err == nil {
		t.Fatal("replaced peer opened a task")
	}
	srv.hub.onAgentMessage(cur, authB, []byte(`{"type":"task.open","task":"live-1","title":"new connection"}`))
	if t2, err := srv.store.Task("live-1"); err != nil || t2.AgentID != b.ID {
		t.Fatalf("current peer must still work: %v", err)
	}
}

// The owner's account is deleted between the token check and hub registration: the agent hears 4003, not "token rotated".
func TestAttachAfterAccountDeletedSaysAccountDeleted(t *testing.T) {
	srv := newLocal(t)
	owner, _, err := srv.store.CreateOwner("leaver")
	if err != nil {
		t.Fatal(err)
	}
	a, token, err := srv.hub.CreateAgent(owner, "orphan")
	if err != nil {
		t.Fatal(err)
	}
	auth, err := srv.store.AgentByToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.hub.DeleteOwner(owner); err != nil {
		t.Fatal(err)
	}
	p, conn := wsPeer(t, srv)
	if srv.hub.attachAgent(p, auth, token) {
		t.Fatal("deleted account's agent must not attach")
	}
	closedWith(t, conn, 4003)
	srv.hub.mu.Lock()
	attached := srv.hub.agents[a.ID] != nil
	srv.hub.mu.Unlock()
	if attached {
		t.Fatal("peer registered for a deleted account")
	}
}
