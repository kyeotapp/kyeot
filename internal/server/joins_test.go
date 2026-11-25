package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

type joinRes struct{ Status, Token, OwnerID, RoomID, RequestID, Poll string }

func pairRoom(t *testing.T, h *decktest.Harness, code, name string) (int, joinRes) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"code": code, "name": name})
	req, _ := http.NewRequest("POST", h.URL+"/pair/room", bytes.NewReader(body))
	req.Header.Set("X-Kyeot-Pair", "1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out joinRes
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func pollJoin(t *testing.T, h *decktest.Harness, id, poll string) (int, joinRes) {
	t.Helper()
	req, _ := http.NewRequest("GET", h.URL+"/pair/room/"+id, nil)
	req.Header.Set("X-Kyeot-Pair", "1")
	req.Header.Set("X-Kyeot-Poll", poll)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out joinRes
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func roomCode(t *testing.T, h *decktest.Harness, token, roomID string) string {
	t.Helper()
	var c struct{ Code string }
	if s := h.APIAs(token, "POST", "/api/rooms/"+roomID+"/invite", nil, &c); s != http.StatusOK {
		t.Fatalf("room code: %d", s)
	}
	return c.Code
}

func TestAdminRoomCodeCreatesAccount(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	var room protocol.Room
	h.API("POST", "/api/rooms", map[string]any{"name": "Contest", "agentId": a.Agent.ID}, &room)

	s, r := pairRoom(t, h, roomCode(t, h, decktest.OwnerToken, room.ID), "Newcomer")
	if s != http.StatusOK || r.Status != "approved" || r.Token == "" || r.RoomID != room.ID {
		t.Fatalf("admin room code: %d %+v", s, r)
	}
	var rooms []protocol.Room
	h.APIAs(r.Token, "GET", "/api/rooms", nil, &rooms)
	if len(rooms) != 1 || rooms[0].ID != room.ID || !rooms[0].Shared {
		t.Fatalf("newcomer rooms: %+v", rooms)
	}
}

func TestMemberRoomCodeWaitsForAdmin(t *testing.T) {
	h := decktest.New(t)
	owner := h.ConnectClient()
	tok2 := invite(t, h, "Bob")
	a2 := struct {
		Agent protocol.Agent
		Token string
	}{}
	h.APIAs(tok2, "POST", "/api/agents", map[string]string{"name": "friendbot"}, &a2)
	var room protocol.Room
	if s := h.APIAs(tok2, "POST", "/api/rooms", map[string]any{"name": "friend room", "agentId": a2.Agent.ID}, &room); s != http.StatusCreated {
		t.Fatalf("friend room: %d", s)
	}

	s, r := pairRoom(t, h, roomCode(t, h, tok2, room.ID), "Carol")
	if s != http.StatusAccepted || r.Status != "pending" || r.Token != "" || r.Poll == "" {
		t.Fatalf("member room code should wait: %d %+v", s, r)
	}
	h.WaitFor(owner, "join.request to the owner", func(m protocol.ClientMessage) bool {
		return m.Type == "join.request" && m.Join.Name == "Carol" && m.Join.InviterName == "Bob" && m.Join.RoomName == "friend room"
	})
	if s, p := pollJoin(t, h, r.RequestID, r.Poll); s != http.StatusOK || p.Status != "pending" {
		t.Fatalf("poll pending: %d %+v", s, p)
	}
	if s, _ := pollJoin(t, h, r.RequestID, "wrong"); s != http.StatusNotFound {
		t.Fatalf("poll with wrong secret: %d", s)
	}
	if s := h.APIAs(tok2, "GET", "/api/joins", nil, nil); s != http.StatusForbidden {
		t.Fatalf("non-admin lists joins: %d", s)
	}
	var joins []protocol.JoinRequest
	h.API("GET", "/api/joins", nil, &joins)
	if len(joins) != 1 || joins[0].ID != r.RequestID {
		t.Fatalf("joins: %+v", joins)
	}
	if s := h.APIAs(tok2, "POST", "/api/joins/"+r.RequestID, map[string]bool{"approve": true}, nil); s != http.StatusForbidden {
		t.Fatalf("non-admin approves: %d", s)
	}
	if s := h.API("POST", "/api/joins/"+r.RequestID, map[string]bool{"approve": true}, nil); s != http.StatusOK {
		t.Fatalf("approve: %d", s)
	}
	h.WaitFor(owner, "join.done", func(m protocol.ClientMessage) bool { return m.Type == "join.done" && m.Join.ID == r.RequestID })
	s, p := pollJoin(t, h, r.RequestID, r.Poll)
	if s != http.StatusOK || p.Status != "approved" || p.Token == "" || p.RoomID != room.ID {
		t.Fatalf("poll approved: %d %+v", s, p)
	}
	var rooms []protocol.Room
	h.APIAs(p.Token, "GET", "/api/rooms", nil, &rooms)
	if len(rooms) != 1 || rooms[0].ID != room.ID {
		t.Fatalf("approved newcomer rooms: %+v", rooms)
	}
	if s, _ := pollJoin(t, h, r.RequestID, r.Poll); s != http.StatusNotFound {
		t.Fatalf("token handed out twice: %d", s)
	}

	// reject
	_, r2 := pairRoom(t, h, roomCode(t, h, tok2, room.ID), "Stranger")
	h.API("POST", "/api/joins/"+r2.RequestID, map[string]bool{"approve": false}, nil)
	if _, p := pollJoin(t, h, r2.RequestID, r2.Poll); p.Status != "rejected" || p.Token != "" {
		t.Fatalf("poll rejected: %+v", p)
	}
}

func TestOwnerGrantsAdmin(t *testing.T) {
	h := decktest.New(t)
	tok2 := invite(t, h, "Bob")
	tok3 := invite(t, h, "Carol")
	var users []struct {
		ID, Name string
		Admin    bool
	}
	h.API("GET", "/api/owners", nil, &users)
	ids := map[string]string{}
	for _, u := range users {
		ids[u.Name] = u.ID
	}
	if s := h.APIAs(tok2, "PATCH", "/api/owners/"+ids["Carol"], map[string]bool{"admin": true}, nil); s != http.StatusForbidden {
		t.Fatalf("non-owner grants admin: %d", s)
	}
	if s := h.API("PATCH", "/api/owners/me", map[string]bool{"admin": false}, nil); s != http.StatusBadRequest {
		t.Fatalf("owner demotes self: %d", s)
	}
	if s := h.API("PATCH", "/api/owners/"+ids["Bob"], map[string]bool{"admin": true}, nil); s != http.StatusNoContent {
		t.Fatalf("grant admin: %d", s)
	}
	var me struct {
		CanInvite bool
		Role      string
	}
	h.APIAs(tok2, "GET", "/api/me", nil, &me)
	if !me.CanInvite || me.Role != "admin" {
		t.Fatalf("admin me: %+v", me)
	}
	h.API("GET", "/api/owners", nil, &users)
	for _, u := range users {
		if u.Name == "Bob" && !u.Admin {
			t.Fatal("owner list should show admin")
		}
	}
	// an admin can remove ordinary users but not other admins or the owner
	h.API("PATCH", "/api/owners/"+ids["Carol"], map[string]bool{"admin": true}, nil)
	if s := h.APIAs(tok2, "DELETE", "/api/owners/"+ids["Carol"], nil, nil); s != http.StatusForbidden {
		t.Fatalf("admin revokes admin: %d", s)
	}
	h.API("PATCH", "/api/owners/"+ids["Carol"], map[string]bool{"admin": false}, nil)
	if s := h.APIAs(tok2, "DELETE", "/api/owners/"+ids["Carol"], nil, nil); s != http.StatusNoContent {
		t.Fatalf("admin revokes user: %d", s)
	}
	if s := h.APIAs(tok3, "GET", "/api/me", nil, nil); s != http.StatusUnauthorized {
		t.Fatalf("revoked token still works: %d", s)
	}
	// an admin's room code joins immediately
	a := struct {
		Agent protocol.Agent
		Token string
	}{}
	h.APIAs(tok2, "POST", "/api/agents", map[string]string{"name": "friendbot"}, &a)
	var room protocol.Room
	h.APIAs(tok2, "POST", "/api/rooms", map[string]any{"name": "friend room", "agentId": a.Agent.ID}, &room)
	if s, r := pairRoom(t, h, roomCode(t, h, tok2, room.ID), "Newcomer"); s != http.StatusOK || r.Status != "approved" {
		t.Fatalf("admin's room code: %d %+v", s, r)
	}
	// once permission is withdrawn, the invite list is not visible
	h.API("PATCH", "/api/owners/"+ids["Bob"], map[string]bool{"admin": false}, nil)
	if s := h.APIAs(tok2, "GET", "/api/owners", nil, nil); s != http.StatusForbidden {
		t.Fatalf("demoted lists users: %d", s)
	}
}

func TestClosedCodeWindowDropsCode(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	var room protocol.Room
	h.API("POST", "/api/rooms", map[string]any{"name": "Contest", "agentId": a.Agent.ID}, &room)
	code := roomCode(t, h, decktest.OwnerToken, room.ID)
	if s := h.API("DELETE", "/api/rooms/"+room.ID+"/invite", nil, nil); s != http.StatusNoContent {
		t.Fatalf("drop room code: %d", s)
	}
	if s, _ := pairRoom(t, h, code, "Newcomer"); s != http.StatusUnauthorized {
		t.Fatalf("dropped room code still works: %d", s)
	}
	var inv struct{ Code string }
	h.API("POST", "/api/owners/invite", nil, &inv)
	h.API("DELETE", "/api/owners/invite", nil, nil)
	redeem(t, h, inv.Code, "Bob", http.StatusUnauthorized)
}
