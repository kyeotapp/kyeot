package server_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

type post struct {
	Task    protocol.Task `json:"task"`
	Preview string        `json:"preview"`
}

func routinesOfPosts(posts []post) []string {
	var out []string
	for _, p := range posts {
		out = append(out, p.Task.RoutineID)
	}
	slices.Sort(out)
	return out
}

func TestRoomWithManyRoutines(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	ctx := context.Background()

	var room protocol.Room
	body := map[string]any{"name": "Morning", "agentId": a.Agent.ID, "routineIds": []string{"news", " weather ", "news", ""}}
	if s := h.API("POST", "/api/rooms", body, &room); s != http.StatusCreated {
		t.Fatalf("create: %d", s)
	}
	if !slices.Equal(room.RoutineIDs, []string{"news", "weather"}) || room.RoutineID != "news" {
		t.Fatalf("room routines: %+v", room)
	}
	for i, r := range []string{"news", "weather", "other"} {
		agent.Send(ctx, protocol.AgentMessage{Type: "routine.run", Routine: r, Run: fmt.Sprint(i), Text: r})
		h.WaitFor(c, r+" run", func(m protocol.ClientMessage) bool { return m.Type == "task" && m.Task.RoutineID == r })
	}
	var posts []post
	h.API("GET", "/api/rooms/"+room.ID+"/posts", nil, &posts)
	if got := routinesOfPosts(posts); !slices.Equal(got, []string{"news", "weather"}) {
		t.Fatalf("feed: %v", got)
	}

	// Manage: drop news and add other. Notifies open screens
	if s := h.API("PUT", "/api/rooms/"+room.ID+"/routines", map[string]any{"routineIds": []string{"weather", "other"}}, &room); s != http.StatusOK {
		t.Fatalf("put: %d", s)
	}
	h.WaitFor(c, "room update", func(m protocol.ClientMessage) bool {
		return m.Type == "room" && m.Room.ID == room.ID && slices.Equal(m.Room.RoutineIDs, []string{"weather", "other"})
	})
	h.API("GET", "/api/rooms/"+room.ID+"/posts", nil, &posts)
	if got := routinesOfPosts(posts); !slices.Equal(got, []string{"other", "weather"}) {
		t.Fatalf("feed after put: %v", got)
	}
	var rooms []protocol.Room
	h.API("GET", "/api/rooms", nil, &rooms)
	if len(rooms) != 1 || rooms[0].RoutineID != "weather" || !slices.Equal(rooms[0].RoutineIDs, []string{"weather", "other"}) {
		t.Fatalf("list: %+v", rooms)
	}
	if s := h.API("PUT", "/api/rooms/"+room.ID+"/routines", map[string]any{"routineIds": []string{}}, &room); s != http.StatusOK || len(room.RoutineIDs) != 0 || room.RoutineIDs == nil {
		t.Fatalf("empty: %d %+v", s, room)
	}
}

func TestRoomRoutinesAPIGuards(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	var room protocol.Room
	h.API("POST", "/api/rooms", map[string]any{"name": "Chat", "agentId": a.Agent.ID}, &room)
	if room.RoutineIDs == nil || len(room.RoutineIDs) != 0 {
		t.Fatalf("no routines should be []: %+v", room)
	}
	path := "/api/rooms/" + room.ID + "/routines"
	many := make([]string, 21)
	for i := range many {
		many[i] = fmt.Sprint("r", i)
	}
	cases := []struct {
		name  string
		token string
		path  string
		body  any
		want  int
	}{
		{"no token", "", path, map[string]any{"routineIds": []string{"x"}}, http.StatusUnauthorized},
		{"wrong token", "wrong-token-wrong-token-wrong", path, map[string]any{"routineIds": []string{"x"}}, http.StatusUnauthorized},
		{"missing room", "-", "/api/rooms/nope/routines", map[string]any{"routineIds": []string{"x"}}, http.StatusNotFound},
		{"no routineIds", "-", path, map[string]any{"routineId": "x"}, http.StatusBadRequest},
		{"too many", "-", path, map[string]any{"routineIds": many}, http.StatusBadRequest},
		{"id too long", "-", path, map[string]any{"routineIds": []string{string(make([]byte, 200))}}, http.StatusBadRequest},
	}
	for _, c := range cases {
		var s int
		if c.token == "-" {
			s = h.API("PUT", c.path, c.body, nil)
		} else {
			s = h.APIAs(c.token, "PUT", c.path, c.body, nil)
		}
		if s != c.want {
			t.Errorf("%s: %d, want %d", c.name, s, c.want)
		}
	}
	if s := h.API("POST", "/api/rooms", map[string]any{"name": "x", "agentId": a.Agent.ID, "routineIds": many}, nil); s != http.StatusBadRequest {
		t.Errorf("create with too many: %d", s)
	}
	var rooms []protocol.Room
	h.API("GET", "/api/rooms", nil, &rooms)
	if len(rooms) != 1 || len(rooms[0].RoutineIDs) != 0 {
		t.Fatalf("rejected requests changed the room: %+v", rooms)
	}
}

func TestAgentCreatesRoomWithRoutines(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	agent.Send(context.Background(), protocol.AgentMessage{Type: "room.create", Name: "News", Routine: "old", Routines: []string{"a", "b"}})
	h.WaitFor(c, "room", func(m protocol.ClientMessage) bool {
		return m.Type == "room" && slices.Equal(m.Room.RoutineIDs, []string{"a", "b", "old"}) && m.Room.RoutineID == "a"
	})
}

func TestOwnerRenamesRoom(t *testing.T) {
	h := decktest.New(t)
	created := h.CreateAgent("Agent A")
	var room struct{ ID, Name string }
	if st := h.API("POST", "/api/rooms", map[string]any{"name": "# First name", "agentId": created.Agent.ID}, &room); st != http.StatusCreated || room.Name != "First name" {
		t.Fatalf("create: %d %q", st, room.Name)
	}
	var got struct{ Name string }
	if st := h.API("PATCH", "/api/rooms/"+room.ID, map[string]string{"name": "  New   name "}, &got); st != http.StatusOK || got.Name != "New name" {
		t.Fatalf("rename: %d %q", st, got.Name)
	}
	if st := h.API("PATCH", "/api/rooms/"+room.ID, map[string]string{"name": " # "}, nil); st != http.StatusBadRequest {
		t.Fatalf("empty name: %d", st)
	}
	if st := h.API("PATCH", "/api/rooms/nope", map[string]string{"name": "x"}, nil); st != http.StatusNotFound {
		t.Fatalf("unknown room: %d", st)
	}
	if st := h.APIAs("not-the-owner-token-000000", "PATCH", "/api/rooms/"+room.ID, map[string]string{"name": "x"}, nil); st != http.StatusUnauthorized {
		t.Fatalf("non-owner: %d", st)
	}
}
