package server_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

// A member with permission who asks an offline agent gets 409 and no task is created. Person-to-person posts still work.
func TestMemberAsksOfflineAgent(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Offline bot") // agent not attached
	var room protocol.Room
	h.API("POST", "/api/rooms", map[string]any{"name": "Offline room", "agentId": a.Agent.ID}, &room)
	tok2 := invite(t, h, "Bob")
	var inv struct{ Code string }
	h.API("POST", "/api/rooms/"+room.ID+"/invite", nil, &inv)
	h.APIAs(tok2, "POST", "/api/rooms/join", map[string]string{"code": inv.Code}, nil)
	var members []struct{ OwnerID string }
	h.API("GET", "/api/rooms/"+room.ID+"/members", nil, &members)
	h.API("PATCH", "/api/rooms/"+room.ID+"/members/"+members[0].OwnerID, map[string]string{"role": "agent"}, nil)

	var before []protocol.Task
	h.API("GET", "/api/tasks", nil, &before)
	var out struct{ Error string }
	if s := h.APIAs(tok2, "POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "Are you there?", "ask": true}, &out); s != http.StatusConflict || out.Error != "agent is offline" {
		t.Fatalf("offline ask: %d %+v", s, out) // the app maps this text to its own localized notice
	}
	var after []protocol.Task
	h.API("GET", "/api/tasks", nil, &after)
	if len(after) != len(before) {
		t.Fatalf("offline ask made a task: %d -> %d", len(before), len(after))
	}
	if s := h.APIAs(tok2, "POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "I'll wait then", "ask": false}, nil); s != http.StatusCreated {
		t.Fatalf("talk in offline room: %d", s)
	}
}

// Removing from the server closes the open screen socket with 4003 "owner revoked"; the app shows the reconnect notice for that reason.
func TestRevokeClosesClientWithReason(t *testing.T) {
	h := decktest.New(t)
	tok2 := invite(t, h, "Bob")
	var me struct{ OwnerID string }
	h.APIAs(tok2, "GET", "/api/me", nil, &me)
	ctx, cancel := context.WithTimeout(context.Background(), decktest.WaitLimit)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.URL, "http")+"/client?token="+tok2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	read := make(chan error, 1)
	go func() { // a pong is only received if something is reading
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				read <- err
				return
			}
		}
	}()
	if err := conn.Ping(ctx); err != nil { // after the server registers it on the hub
		t.Fatal(err)
	}
	if s := h.API("DELETE", "/api/owners/"+me.OwnerID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("revoke: %d", s)
	}
	select {
	case err := <-read:
		var ce websocket.CloseError
		if !errors.As(err, &ce) || ce.Code != 4003 || ce.Reason != "owner revoked" {
			t.Fatalf("close: %v", err)
		}
	case <-time.After(decktest.WaitLimit):
		t.Fatal("socket stayed open after revoke")
	}
}
