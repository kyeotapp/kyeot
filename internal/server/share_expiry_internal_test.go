package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/store"
)

func call(t *testing.T, srv *Server, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("X-Kyeot-Pair", "1")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// sharedRoomFor creates one owner room and one invited user (not an admin) who has a room.
func sharedRoomFor(t *testing.T, srv *Server) (roomID, userTok, userRoom string) {
	t.Helper()
	a, _, err := srv.hub.CreateAgent(store.Owner, "Agent A")
	if err != nil {
		t.Fatal(err)
	}
	room, err := srv.store.CreateRoom(store.Owner, "Contest", a.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	uid, tok, err := srv.store.CreateOwner("Alice")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := srv.hub.CreateAgent(uid, "Agent B")
	if err != nil {
		t.Fatal(err)
	}
	ur, err := srv.store.CreateRoom(uid, "Alice's room", b.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return room.ID, tok, ur.ID
}

func TestRoomCodeExpires(t *testing.T) {
	srv := newLocal(t)
	roomID, tok, _ := sharedRoomFor(t, srv)
	now := time.Now()
	srv.roomInvites.now = func() time.Time { return now }

	_, inv := call(t, srv, "POST", "/api/rooms/"+roomID+"/invite", testOwner, nil)
	now = now.Add(pairTTL + time.Second)
	if code, _ := call(t, srv, "POST", "/api/rooms/join", tok, map[string]any{"code": inv["code"]}); code != http.StatusUnauthorized {
		t.Fatalf("expired code joins: %d", code)
	}
	if code, _ := call(t, srv, "POST", "/pair/room", "", map[string]any{"code": inv["code"], "name": "Carol"}); code != http.StatusUnauthorized {
		t.Fatalf("expired code makes an account: %d", code)
	}
	if list, _ := srv.store.RoomMembers(roomID); len(list) != 0 {
		t.Fatalf("members: %+v", list)
	}

	_, inv = call(t, srv, "POST", "/api/rooms/"+roomID+"/invite", testOwner, nil)
	now = now.Add(pairTTL - time.Second)
	if code, _ := call(t, srv, "POST", "/api/rooms/join", tok, map[string]any{"code": inv["code"]}); code != http.StatusOK {
		t.Fatalf("code just before expiry: %d", code)
	}
}

func TestJoinRequestExpires(t *testing.T) {
	srv := newLocal(t)
	_, tok, userRoom := sharedRoomFor(t, srv)
	_, inv := call(t, srv, "POST", "/api/rooms/"+userRoom+"/invite", tok, nil)
	code, req := call(t, srv, "POST", "/pair/room", "", map[string]any{"code": inv["code"], "name": "Bob"})
	if code != http.StatusAccepted {
		t.Fatalf("pending: %d %+v", code, req)
	}
	id := req["requestId"].(string)

	srv.joins.mu.Lock()
	srv.joins.m[id].expires = time.Now().Add(-time.Second)
	srv.joins.mu.Unlock()

	if n := len(srv.joins.pending()); n != 0 {
		t.Fatalf("expired request still pending: %d", n)
	}
	if code, _ := call(t, srv, "POST", "/api/joins/"+id, testOwner, map[string]bool{"approve": true}); code != http.StatusNotFound {
		t.Fatalf("approve expired: %d", code)
	}
	if code, _ := call(t, srv, "GET", "/pair/room/"+id, "", nil); code != http.StatusNotFound {
		t.Fatalf("poll expired: %d", code)
	}
	if list, _ := srv.store.Invitees(); len(list) != 1 {
		t.Fatalf("account made from an expired request: %+v", list)
	}
}

// A person invite code also creates no account after 10 minutes.
func TestOwnerInviteCodeExpires(t *testing.T) {
	srv := newLocal(t)
	now := time.Now()
	srv.ownerInvites.now = func() time.Time { return now }
	_, inv := call(t, srv, "POST", "/api/owners/invite", testOwner, nil)
	now = now.Add(pairTTL + time.Second)
	if code, _ := call(t, srv, "POST", "/pair/owner", "", map[string]any{"code": inv["code"], "name": "Latecomer"}); code != http.StatusUnauthorized {
		t.Fatalf("expired owner invite: %d", code)
	}
	if list, _ := srv.store.Invitees(); len(list) != 0 {
		t.Fatalf("invitees: %+v", list)
	}
}
