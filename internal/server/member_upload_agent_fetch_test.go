package server_test

import (
	"net/http"
	"testing"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

// When a room member attaches their own file and calls the owner's agent, that task's agent fetches the file with its token. The owner's other agents cannot.
func TestMemberUploadFetchableByRoomAgent(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	other := h.CreateAgent("Agent B")
	h.StartFakeAgent(a.Token)
	waitState(t, h.URL, a.Agent.ID, protocol.AgentIdle)
	room := makeRoom(t, h, "Shared", a.Agent.ID)
	tokB := invite(t, h, "Bob")
	friendID := shareRoom(t, h, room.ID, tokB)
	if s := h.API("PATCH", "/api/rooms/"+room.ID+"/members/"+friendID, map[string]string{"role": "agent"}, nil); s != http.StatusOK {
		t.Fatalf("grant: %d", s)
	}

	f := uploadAs(t, h, tokB, "memo.txt", "member memo")
	var asked protocol.Task
	if s := h.APIAs(tokB, "POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "read it", "ask": true, "attachments": []string{f.ID}}, &asked); s != http.StatusCreated || asked.AskedBy != friendID {
		t.Fatalf("member asks: %d %+v", s, asked)
	}
	if s, _, b := fetchAs(t, h, a.Token, "/api/uploads/"+f.ID); s != http.StatusOK || string(b) != "member memo" {
		t.Fatalf("task agent fetches member's file: %d %q", s, b)
	}
	if s, _, _ := fetchAs(t, h, other.Token, "/api/uploads/"+f.ID); s != http.StatusNotFound {
		t.Fatalf("owner's other agent: %d", s)
	}
}
