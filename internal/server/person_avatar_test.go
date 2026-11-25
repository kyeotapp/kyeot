package server_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/server"
)

type personMe struct {
	OwnerID, Name, Avatar, Role string
}

type person struct {
	OwnerID, Name, Avatar, Role string
}

func TestPersonAvatarPatchAndShow(t *testing.T) {
	h := decktest.New(t)
	if !slices.Contains(server.Features, "people.avatar") {
		t.Fatal("Features lacks people.avatar")
	}

	var me personMe
	if s := h.API("GET", "/api/me", nil, &me); s != http.StatusOK || me.Avatar != "" {
		t.Fatalf("default picture is empty: %d %+v", s, me)
	}
	if s := h.API("PATCH", "/api/me", map[string]string{"avatar": "color:orange"}, &me); s != http.StatusOK || me.Avatar != "color:orange" || me.Name != "me" {
		t.Fatalf("changing only the color keeps the name: %d %+v", s, me)
	}
	photo := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("png"))
	if s := h.API("PATCH", "/api/me", map[string]string{"avatar": photo, "name": "Owner"}, &me); s != http.StatusOK || me.Avatar != photo || me.Name != "Owner" {
		t.Fatalf("photo and name together: %d %+v", s, me)
	}
	for _, bad := range []map[string]any{
		{"avatar": "color:red"},
		{"avatar": "preset:fox"},
		{"avatar": "data:image/gif;base64,AAAA"},
		{"avatar": "data:image/png;base64," + strings.Repeat("A", 200_000)},
		{"avatar": 3},
		{},
		{"name": ""},
	} {
		if s := h.API("PATCH", "/api/me", bad, nil); s != http.StatusBadRequest {
			t.Errorf("%v: %d, want 400", bad, s)
		}
	}
	h.API("GET", "/api/me", nil, &me)
	if me.Avatar != photo || me.Name != "Owner" {
		t.Fatalf("a rejected request changed it: %+v", me)
	}
	if s := h.API("PATCH", "/api/me", map[string]string{"avatar": ""}, &me); s != http.StatusOK || me.Avatar != "" {
		t.Fatalf("empty value reverts to the first letter: %d %+v", s, me)
	}

	// Of the five places, those the server provides: user list, room people (owner included), member permission response
	tok2 := invite(t, h, "Bob")
	h.APIAs(tok2, "PATCH", "/api/me", map[string]string{"avatar": "color:teal"}, nil)
	h.API("PATCH", "/api/me", map[string]string{"avatar": "color:green"}, nil)
	var users []person
	h.API("GET", "/api/owners", nil, &users)
	if len(users) != 1 || users[0].Avatar != "color:teal" {
		t.Fatalf("user list: %+v", users)
	}

	a := h.CreateAgent("Agent A")
	var room protocol.Room
	h.API("POST", "/api/rooms", map[string]any{"name": "Contest", "agentId": a.Agent.ID}, &room)
	var inv struct{ Code string }
	h.API("POST", "/api/rooms/"+room.ID+"/invite", nil, &inv)
	h.APIAs(tok2, "POST", "/api/rooms/join", map[string]string{"code": inv.Code}, nil)

	// The member list carries no pictures (photos are heavy); pictures come only from /people
	var members []person
	h.API("GET", "/api/rooms/"+room.ID+"/members", nil, &members)
	if len(members) != 1 || members[0].Avatar != "" || members[0].Name != "Bob" {
		t.Fatalf("member list carried a picture: %+v", members)
	}
	for _, tok := range []string{"", tok2} {
		var people struct {
			Owner   person
			Members []person
		}
		var s int
		if tok == "" {
			s = h.API("GET", "/api/rooms/"+room.ID+"/people", nil, &people)
		} else {
			s = h.APIAs(tok, "GET", "/api/rooms/"+room.ID+"/people", nil, &people)
		}
		if s != http.StatusOK || people.Owner.Avatar != "color:green" || people.Owner.Name != "Owner" || len(people.Members) != 1 || people.Members[0].Avatar != "color:teal" {
			t.Fatalf("room people (%q): %d %+v", tok, s, people)
		}
	}
	var changed person
	h.API("PATCH", "/api/rooms/"+room.ID+"/members/"+members[0].OwnerID, map[string]string{"role": "agent"}, &changed)
	if changed.Avatar != "color:teal" {
		t.Fatalf("permission response: %+v", changed)
	}
	tok3 := invite(t, h, "Carol")
	if s := h.APIAs(tok3, "GET", "/api/rooms/"+room.ID+"/people", nil, nil); s != http.StatusNotFound {
		t.Fatalf("person outside the room: %d", s)
	}
	// The websocket room and the room list carry no pictures (down to the inner member slot)
	for _, tok := range []string{"", tok2} {
		var raw json.RawMessage
		if tok == "" {
			h.API("GET", "/api/rooms", nil, &raw)
		} else {
			h.APIAs(tok, "GET", "/api/rooms", nil, &raw)
		}
		if strings.Contains(strings.ToLower(string(raw)), "avatar") || strings.Contains(string(raw), "color:teal") {
			t.Fatalf("picture in room list (%q): %s", tok, raw)
		}
	}
}

// Deleting the account removes the picture from the DB too. The owner's picture stays.
func TestPersonAvatarGoesWithAccount(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "kyeot.db")
	h := decktest.NewWith(t, func(o *server.Options) { o.DBPath = dbPath })
	tok2 := invite(t, h, "Bob")
	photo := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("friend photo marker"))
	if s := h.APIAs(tok2, "PATCH", "/api/me", map[string]string{"avatar": photo}, nil); s != http.StatusOK {
		t.Fatalf("patch: %d", s)
	}
	h.API("PATCH", "/api/me", map[string]string{"avatar": "color:pink"}, nil)
	if hits := rowsContaining(t, dbPath, photo); len(hits) == 0 {
		t.Fatal("not saved")
	}
	if s := h.APIAs(tok2, "DELETE", "/api/me", nil, nil); s != http.StatusNoContent {
		t.Fatalf("delete: %d", s)
	}
	if hits := rowsContaining(t, dbPath, photo); len(hits) > 0 {
		t.Fatalf("the deleted user's picture remains: %v", hits)
	}
	var me personMe
	h.API("GET", "/api/me", nil, &me)
	if me.Avatar != "color:pink" {
		t.Fatalf("the owner's picture was deleted too: %+v", me)
	}
}
