package server_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

// Room sharing overlapping with several features. members_test.go and joins_test.go cover each feature alone.

// shareRoom shares the owner's room with the tok user and returns that user's owner id.
func shareRoom(t *testing.T, h *decktest.Harness, roomID, tok string) string {
	t.Helper()
	if s := h.APIAs(tok, "POST", "/api/rooms/join", map[string]string{"code": roomCode(t, h, decktest.OwnerToken, roomID)}, nil); s != http.StatusOK {
		t.Fatalf("join: %d", s)
	}
	var me struct{ OwnerID string }
	h.APIAs(tok, "GET", "/api/me", nil, &me)
	return me.OwnerID
}

func makeRoom(t *testing.T, h *decktest.Harness, name, agentID string, routines ...string) protocol.Room {
	t.Helper()
	var room protocol.Room
	if s := h.API("POST", "/api/rooms", map[string]any{"name": name, "agentId": agentID, "routineIds": routines}, &room); s != http.StatusCreated {
		t.Fatalf("room %s: %d", name, s)
	}
	return room
}

func isTask(id string) func(protocol.ClientMessage) bool {
	return func(m protocol.ClientMessage) bool { return m.Task != nil && m.Task.ID == id }
}

func TestSharingOneAgentRoomHidesTheOtherAgent(t *testing.T) {
	h := decktest.New(t)
	a, b := h.CreateAgent("Agent A"), h.CreateAgent("Agent B")
	owner := h.ConnectClient()
	agentA, agentB := h.StartFakeAgent(a.Token), h.StartFakeAgent(b.Token)
	h.WaitAll(owner, "A, B idle", decktest.AgentState(a.Agent.ID, "idle"), decktest.AgentState(b.Agent.ID, "idle"))
	roomA := makeRoom(t, h, "Room A", a.Agent.ID, "jobA")
	roomB := makeRoom(t, h, "Room B", b.Agent.ID, "jobB")

	agentB.Send(context.Background(), protocol.AgentMessage{Type: "routine.run", Routine: "jobB", RoutineName: "B routine", Run: "b1", Text: "B result"})
	var bTask string
	h.WaitFor(owner, "B routine task", func(m protocol.ClientMessage) bool {
		if m.Type == "task" && m.Task.RoutineID == "jobB" {
			bTask = m.Task.ID
			return true
		}
		return false
	})
	h.API("POST", "/api/rooms/"+roomB.ID+"/messages", map[string]any{"text": "Room B people message", "ask": false}, nil)

	tok2 := invite(t, h, "Alice")
	friendID := shareRoom(t, h, roomA.ID, tok2)
	friend := h.ConnectClientAs(tok2)
	h.API("PATCH", "/api/rooms/"+roomA.ID+"/members/"+friendID, map[string]string{"role": "agent"}, nil)

	var rooms []protocol.Room
	h.APIAs(tok2, "GET", "/api/rooms", nil, &rooms)
	if len(rooms) != 1 || rooms[0].ID != roomA.ID || !rooms[0].CanAsk {
		t.Fatalf("friend sees rooms: %+v", rooms)
	}
	for _, p := range []string{"/posts", "/messages", "/members"} {
		if s := h.APIAs(tok2, "GET", "/api/rooms/"+roomB.ID+p, nil, nil); s != http.StatusNotFound {
			t.Fatalf("friend reads B room %s: %d", p, s)
		}
	}
	if s := h.APIAs(tok2, "GET", "/api/tasks/"+bTask+"/events", nil, nil); s != http.StatusNotFound {
		t.Fatalf("friend reads B routine task: %d", s)
	}
	if s := h.APIAs(tok2, "POST", "/api/rooms/"+roomB.ID+"/messages", map[string]any{"text": "intrusion", "ask": false}, nil); s != http.StatusNotFound {
		t.Fatalf("friend posts in B room: %d", s)
	}
	if s := h.APIAs(tok2, "POST", "/api/tasks/"+bTask+"/messages", map[string]string{"text": "To B"}, nil); s != http.StatusNotFound {
		t.Fatalf("friend follows up B task: %d", s)
	}
	var mine []protocol.Task
	if h.APIAs(tok2, "GET", "/api/tasks", nil, &mine); len(mine) != 0 {
		t.Fatalf("friend task list: %+v", mine)
	}

	// Things newly created on the B side do not reach Alice's screen
	agentB.Send(context.Background(), protocol.AgentMessage{Type: "routine.run", Routine: "jobB", RoutineName: "B routine", Run: "b2", Text: "B second"})
	h.API("POST", "/api/rooms/"+roomB.ID+"/messages", map[string]any{"text": "Room B second message", "ask": false}, nil)
	h.API("PATCH", "/api/rooms/"+roomB.ID, map[string]string{"name": "Room B2"}, nil)
	h.WaitFor(owner, "B room renamed", func(m protocol.ClientMessage) bool { return m.Type == "room" && m.Room.Name == "Room B2" })
	h.Quiet(friend, "anything from B", 300*time.Millisecond, func(m protocol.ClientMessage) bool {
		return (m.Task != nil && m.Task.AgentID == b.Agent.ID) || (m.Room != nil && m.Room.ID == roomB.ID) ||
			(m.RoomMessage != nil && m.RoomMessage.RoomID == roomB.ID)
	})

	// If Alice asks in room A, it goes only to A
	var asked protocol.Task
	if s := h.APIAs(tok2, "POST", "/api/rooms/"+roomA.ID+"/messages", map[string]any{"text": "Tidy up", "ask": true}, &asked); s != http.StatusCreated {
		t.Fatalf("ask in A: %d", s)
	}
	if asked.AgentID != a.Agent.ID || asked.RoomID != roomA.ID || asked.AskedBy != friendID {
		t.Fatalf("asked task went to: %+v", asked)
	}
	h.WaitFor(friend, "friend sees its own ask", isTask(asked.ID))
	var bTasks []protocol.Task
	h.API("GET", "/api/tasks?agent="+b.Agent.ID, nil, &bTasks)
	for _, x := range bTasks {
		if x.ID == asked.ID || x.AskedBy != "" {
			t.Fatalf("friend's ask reached B: %+v", x)
		}
	}
	_ = agentA
}

func TestMemberHitsOwnerOnlyAPIs(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	owner := h.ConnectClient()
	h.StartFakeAgent(a.Token)
	h.WaitFor(owner, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	room := makeRoom(t, h, "Contest", a.Agent.ID, "job1")
	other := makeRoom(t, h, "Private", a.Agent.ID)
	var private protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "Secret sentence zq7"}, &private)

	tok2 := invite(t, h, "Alice")
	friendID := shareRoom(t, h, room.ID, tok2)
	// Even a member who can ask the agent is blocked from owner-only actions
	h.API("PATCH", "/api/rooms/"+room.ID+"/members/"+friendID, map[string]string{"role": "agent"}, nil)
	tok3 := invite(t, h, "Bob")
	thirdID := shareRoom(t, h, room.ID, tok3)

	ag := "/api/agents/" + a.Agent.ID
	cases := []struct {
		method, path string
		body         any
		want         int
	}{
		{"GET", ag + "/report", nil, 404},
		{"GET", ag + "/routines", nil, 404},
		{"GET", ag + "/ui", nil, 404},
		{"POST", ag + "/token", nil, 404},
		{"POST", ag + "/pair", nil, 404},
		{"PUT", ag + "/avatar", map[string]string{"avatar": "preset:cat"}, 404},
		{"POST", ag + "/routines", map[string]string{"name": "x", "schedule": "daily", "prompt": "p"}, 404},
		{"POST", ag + "/routines/job1/toggle", map[string]string{"on": "false"}, 404},
		{"POST", ag + "/routines/job1/run", nil, 404},
		{"DELETE", ag + "/routines/job1", nil, 404},
		{"PUT", ag + "/ui/tabs", map[string]any{"tabs": []string{}}, 404},
		{"DELETE", ag, nil, 404},
		{"POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "Sneaky"}, 404},
		{"PATCH", "/api/tasks/" + private.ID, map[string]string{"title": "x"}, 404},
		{"POST", "/api/tasks/" + private.ID + "/cancel", nil, 404},
		{"DELETE", "/api/tasks/" + private.ID, nil, 404},
		{"GET", "/api/tasks/" + private.ID + "/events", nil, 404},
		{"PATCH", "/api/rooms/" + room.ID, map[string]string{"name": "Takeover"}, 404},
		{"PUT", "/api/rooms/" + room.ID + "/routines", map[string]any{"routineIds": []string{}}, 404},
		{"POST", "/api/rooms/" + room.ID + "/invite", nil, 404},
		{"DELETE", "/api/rooms/" + room.ID + "/invite", nil, 404},
		{"PATCH", "/api/rooms/" + room.ID + "/members/" + thirdID, map[string]string{"role": "agent"}, 404},
		{"PATCH", "/api/rooms/" + room.ID + "/members/" + friendID, map[string]string{"role": "agent"}, 404},
		{"DELETE", "/api/rooms/" + room.ID + "/members/" + thirdID, nil, 403},
		{"DELETE", "/api/rooms/" + room.ID, nil, 404},
		{"GET", "/api/rooms/" + other.ID + "/posts", nil, 404},
		{"POST", "/api/rooms", map[string]string{"name": "With another's agent", "agentId": a.Agent.ID}, 404},
		{"POST", "/api/owners/invite", nil, 403},
		{"DELETE", "/api/owners/invite", nil, 403},
		{"GET", "/api/owners", nil, 403},
		{"DELETE", "/api/owners/" + thirdID, nil, 403},
		{"PATCH", "/api/owners/" + friendID, map[string]bool{"admin": true}, 403},
		{"GET", "/api/joins", nil, 403},
		{"POST", "/api/joins/x", map[string]bool{"approve": true}, 403},
	}
	for _, c := range cases {
		if s := h.APIAs(tok2, c.method, c.path, c.body, nil); s != c.want {
			t.Errorf("member %s %s: %d, want %d", c.method, c.path, s, c.want)
		}
	}

	var agents []protocol.Agent
	if h.APIAs(tok2, "GET", "/api/agents", nil, &agents); len(agents) != 0 {
		t.Fatalf("member lists owner's agents: %+v", agents)
	}
	var found []protocol.Event
	if h.APIAs(tok2, "GET", "/api/search?q=zq7", nil, &found); len(found) != 0 {
		t.Fatalf("member searches owner's private task: %+v", found)
	}
	var pending []protocol.Event
	if h.APIAs(tok2, "GET", "/api/approvals", nil, &pending); len(pending) != 0 {
		t.Fatalf("member sees owner's approvals: %+v", pending)
	}

	// Nothing was changed by the attempts above
	var rooms []protocol.Room
	h.API("GET", "/api/rooms", nil, &rooms)
	if len(rooms) != 2 || rooms[0].Name != "Contest" || len(rooms[0].RoutineIDs) != 1 {
		t.Fatalf("owner rooms changed: %+v", rooms)
	}
	var members []struct{ OwnerID, Role string }
	h.API("GET", "/api/rooms/"+room.ID+"/members", nil, &members)
	if len(members) != 2 || members[1].OwnerID != thirdID || members[1].Role != "member" {
		t.Fatalf("members changed: %+v", members)
	}
	if h.API("GET", "/api/agents", nil, &agents); len(agents) != 1 {
		t.Fatalf("owner agents changed: %+v", agents)
	}
}

func TestInviteLifecycle(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	owner := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(owner, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	room := makeRoom(t, h, "Contest", a.Agent.ID, "job1")
	tok2, tok3 := invite(t, h, "Alice"), invite(t, h, "Bob")

	// One use only: another person can't join with the same code
	code := roomCode(t, h, decktest.OwnerToken, room.ID)
	if s := h.APIAs(tok2, "POST", "/api/rooms/join", map[string]string{"code": code}, nil); s != http.StatusOK {
		t.Fatalf("join: %d", s)
	}
	if s := h.APIAs(tok3, "POST", "/api/rooms/join", map[string]string{"code": code}, nil); s != http.StatusUnauthorized {
		t.Fatalf("reused code: %d", s)
	}
	// Issuing a new code kills the old one
	old := roomCode(t, h, decktest.OwnerToken, room.ID)
	fresh := roomCode(t, h, decktest.OwnerToken, room.ID)
	if s := h.APIAs(tok3, "POST", "/api/rooms/join", map[string]string{"code": old}, nil); s != http.StatusUnauthorized {
		t.Fatalf("superseded code: %d", s)
	}
	// The same person joining twice leaves one member, role unchanged
	var me struct{ OwnerID string }
	h.APIAs(tok2, "GET", "/api/me", nil, &me)
	h.API("PATCH", "/api/rooms/"+room.ID+"/members/"+me.OwnerID, map[string]string{"role": "agent"}, nil)
	if s := h.APIAs(tok2, "POST", "/api/rooms/join", map[string]string{"code": fresh}, nil); s != http.StatusOK {
		t.Fatalf("join again: %d", s)
	}
	var members []struct{ OwnerID, Role string }
	h.API("GET", "/api/rooms/"+room.ID+"/members", nil, &members)
	if len(members) != 1 || members[0].Role != "agent" {
		t.Fatalf("double join: %+v", members)
	}

	// After removal, room messages no longer go to an open websocket either
	shareRoom(t, h, room.ID, tok3)
	friend, third := h.ConnectClientAs(tok2), h.ConnectClientAs(tok3)
	h.API("DELETE", "/api/rooms/"+room.ID+"/members/"+me.OwnerID, nil, nil)
	h.WaitFor(friend, "room.removed", func(m protocol.ClientMessage) bool { return m.Type == "room.removed" && m.Room.ID == room.ID })
	agent.Send(context.Background(), protocol.AgentMessage{Type: "routine.run", Routine: "job1", RoutineName: "Alert", Run: "r1", Text: "result"})
	var runTask string
	h.WaitFor(third, "remaining member gets the post", func(m protocol.ClientMessage) bool {
		if m.Type == "task" && m.Task.RoutineID == "job1" {
			runTask = m.Task.ID
			return true
		}
		return false
	})
	h.API("POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "To the remaining person", "ask": false}, nil)
	h.WaitFor(third, "room message", func(m protocol.ClientMessage) bool { return m.Type == "room.message" })
	h.Quiet(friend, "removed member hears the room", 300*time.Millisecond, func(m protocol.ClientMessage) bool {
		return (m.Task != nil && m.Task.ID == runTask) || (m.Event != nil && m.Event.TaskID == runTask) || m.Type == "room.message" || m.Type == "room"
	})
	if s := h.APIAs(tok2, "POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "Still there?", "ask": true}, nil); s != http.StatusNotFound {
		t.Fatalf("removed agent-member asks: %d", s)
	}

	// Deleting the room: members are notified, reads and writes are blocked, and live codes are useless
	pendingCode := roomCode(t, h, decktest.OwnerToken, room.ID)
	if s := h.API("DELETE", "/api/rooms/"+room.ID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("delete room: %d", s)
	}
	h.WaitFor(third, "room.removed on delete", func(m protocol.ClientMessage) bool { return m.Type == "room.removed" && m.Room.ID == room.ID })
	for _, p := range []string{"/posts", "/messages", "/members"} {
		if s := h.APIAs(tok3, "GET", "/api/rooms/"+room.ID+p, nil, nil); s != http.StatusNotFound {
			t.Fatalf("after delete %s: %d", p, s)
		}
	}
	if s := h.APIAs(tok3, "GET", "/api/tasks/"+runTask+"/events", nil, nil); s != http.StatusNotFound {
		t.Fatalf("routine post after room delete: %d", s)
	}
	var rooms []protocol.Room
	if h.APIAs(tok3, "GET", "/api/rooms", nil, &rooms); len(rooms) != 0 {
		t.Fatalf("rooms after delete: %+v", rooms)
	}
	if s := h.APIAs(tok2, "POST", "/api/rooms/join", map[string]string{"code": pendingCode}, nil); s != http.StatusNotFound {
		t.Fatalf("code of deleted room: %d", s)
	}
	if h.APIAs(tok2, "GET", "/api/rooms", nil, &rooms); len(rooms) != 0 {
		t.Fatalf("joined a deleted room: %+v", rooms)
	}
	agent.Send(context.Background(), protocol.AgentMessage{Type: "routine.run", Routine: "job1", RoutineName: "Alert", Run: "r2", Text: "No room"})
	h.WaitFor(owner, "owner still gets the run", func(m protocol.ClientMessage) bool {
		return m.Type == "task" && m.Task.RoutineID == "job1" && m.Task.ID != runTask
	})
	h.Quiet(third, "post from deleted room", 300*time.Millisecond, func(m protocol.ClientMessage) bool { return m.Type == "task" })
}

func TestRevokedUserAndPendingInvites(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	room := makeRoom(t, h, "Contest", a.Agent.ID)
	tok2 := invite(t, h, "Alice")
	friendID := shareRoom(t, h, room.ID, tok2)

	// A code Alice made for her own room, and a person awaiting approval
	var fa struct {
		Agent protocol.Agent
		Token string
	}
	h.APIAs(tok2, "POST", "/api/agents", map[string]string{"name": "Agent B"}, &fa)
	var fRoom protocol.Room
	h.APIAs(tok2, "POST", "/api/rooms", map[string]any{"name": "Alice's room", "agentId": fa.Agent.ID}, &fRoom)
	_, waiting := pairRoom(t, h, roomCode(t, h, tok2, fRoom.ID), "Waiting person")
	if waiting.Status != "pending" {
		t.Fatalf("pending: %+v", waiting)
	}
	liveCode := roomCode(t, h, tok2, fRoom.ID) // a new code discards the room's old code, so issue it last

	if s := h.API("DELETE", "/api/owners/"+friendID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("revoke: %d", s)
	}
	// A room code issued by a withdrawn person is dead
	if s, _ := pairRoom(t, h, liveCode, "Carol"); s != http.StatusUnauthorized {
		t.Fatalf("revoked user's room code: %d", s)
	}
	if s := h.APIAs(tok2, "GET", "/api/rooms/"+room.ID+"/posts", nil, nil); s != http.StatusUnauthorized {
		t.Fatalf("revoked user reads room: %d", s)
	}
	var members []any
	if h.API("GET", "/api/rooms/"+room.ID+"/members", nil, &members); len(members) != 0 {
		t.Fatalf("revoked user still a member: %+v", members)
	}
}

func TestAdminApprovalRaces(t *testing.T) {
	h := decktest.New(t)
	tok2 := invite(t, h, "Alice")
	tok3 := invite(t, h, "Admin")
	var users []struct{ ID, Name string }
	h.API("GET", "/api/owners", nil, &users)
	for _, u := range users {
		if u.Name == "Admin" {
			h.API("PATCH", "/api/owners/"+u.ID, map[string]bool{"admin": true}, nil)
		}
	}
	var fa struct {
		Agent protocol.Agent
		Token string
	}
	h.APIAs(tok2, "POST", "/api/agents", map[string]string{"name": "Agent B"}, &fa)
	var room protocol.Room
	h.APIAs(tok2, "POST", "/api/rooms", map[string]any{"name": "Alice's room", "agentId": fa.Agent.ID}, &room)

	// Before approval: no key. A second request with the same code is not allowed
	code := roomCode(t, h, tok2, room.ID)
	s, r := pairRoom(t, h, code, "Bob")
	if s != http.StatusAccepted || r.Token != "" {
		t.Fatalf("pending: %d %+v", s, r)
	}
	if s, _ := pairRoom(t, h, code, "again"); s != http.StatusUnauthorized {
		t.Fatalf("code reused while pending: %d", s)
	}
	var members []any
	if h.APIAs(tok2, "GET", "/api/rooms/"+room.ID+"/members", nil, &members); len(members) != 0 {
		t.Fatalf("pending person is already a member: %+v", members)
	}

	// If two admins press at once, it is handled only once
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for _, tok := range []string{decktest.OwnerToken, tok3} {
		wg.Add(1)
		go func(tok string) {
			defer wg.Done()
			results <- h.APIAs(tok, "POST", "/api/joins/"+r.RequestID, map[string]bool{"approve": true}, nil)
		}(tok)
	}
	wg.Wait()
	close(results)
	ok := 0
	for s := range results {
		if s == http.StatusOK {
			ok++
		} else if s != http.StatusNotFound {
			t.Fatalf("second approval: %d", s)
		}
	}
	if ok != 1 {
		t.Fatalf("approvals succeeded %d times", ok)
	}
	if h.APIAs(tok2, "GET", "/api/rooms/"+room.ID+"/members", nil, &members); len(members) != 1 {
		t.Fatalf("members after double approve: %+v", members)
	}
	h.API("GET", "/api/owners", nil, &users)
	if len(users) != 3 {
		t.Fatalf("accounts after double approve: %+v", users)
	}

	// If the room is deleted while waiting, approving ends in rejection
	_, r2 := pairRoom(t, h, roomCode(t, h, tok2, room.ID), "Latecomer")
	h.APIAs(tok2, "DELETE", "/api/rooms/"+room.ID, nil, nil)
	var ans struct{ Status string }
	if h.API("POST", "/api/joins/"+r2.RequestID, map[string]bool{"approve": true}, &ans); ans.Status != "rejected" {
		t.Fatalf("approve into deleted room: %+v", ans)
	}
	if _, p := pollJoin(t, h, r2.RequestID, r2.Poll); p.Status != "rejected" || p.Token != "" {
		t.Fatalf("poll after deleted room: %+v", p)
	}
	h.API("GET", "/api/owners", nil, &users)
	if len(users) != 3 {
		t.Fatalf("account made for deleted room: %+v", users)
	}
}

func TestPendingJoinsAreCapped(t *testing.T) {
	h := decktest.New(t)
	tok2 := invite(t, h, "Alice")
	var fa struct {
		Agent protocol.Agent
		Token string
	}
	h.APIAs(tok2, "POST", "/api/agents", map[string]string{"name": "Agent B"}, &fa)
	var room protocol.Room
	h.APIAs(tok2, "POST", "/api/rooms", map[string]any{"name": "Alice's room", "agentId": fa.Agent.ID}, &room)
	for i := 0; i < 20; i++ {
		if s, _ := pairRoom(t, h, roomCode(t, h, tok2, room.ID), "Person"); s != http.StatusAccepted {
			t.Fatalf("request %d: %d", i, s)
		}
	}
	code := roomCode(t, h, tok2, room.ID)
	if s, _ := pairRoom(t, h, code, "Overflow"); s != http.StatusTooManyRequests {
		t.Fatalf("21st request: %d", s)
	}
	var joins []protocol.JoinRequest
	if h.API("GET", "/api/joins", nil, &joins); len(joins) != 20 {
		t.Fatalf("pending: %d", len(joins))
	}
}

func TestUnlinkedAgentRoomIsGoneForMembers(t *testing.T) {
	h := decktest.New(t)
	a, b := h.CreateAgent("Agent A"), h.CreateAgent("Agent B")
	roomA := makeRoom(t, h, "Room A", a.Agent.ID)
	roomB := makeRoom(t, h, "Room B", b.Agent.ID)
	tok2 := invite(t, h, "Alice")
	shareRoom(t, h, roomA.ID, tok2)
	shareRoom(t, h, roomB.ID, tok2)
	leftover := roomCode(t, h, decktest.OwnerToken, roomA.ID)
	friend := h.ConnectClientAs(tok2)

	if s := h.API("DELETE", "/api/agents/"+a.Agent.ID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("unlink: %d", s)
	}
	h.WaitFor(friend, "room.removed for A", func(m protocol.ClientMessage) bool {
		return m.Type == "room.removed" && m.Room.ID == roomA.ID && m.Room.Shared
	})
	h.Quiet(friend, "room.removed for B", 200*time.Millisecond, func(m protocol.ClientMessage) bool {
		return m.Type == "room.removed" && m.Room.ID == roomB.ID
	})
	var rooms []protocol.Room
	h.APIAs(tok2, "GET", "/api/rooms", nil, &rooms)
	if len(rooms) != 1 || rooms[0].ID != roomB.ID {
		t.Fatalf("member rooms after unlink: %+v", rooms)
	}
	// An orphan room does not open by id either
	for _, p := range []string{"/posts", "/messages", "/members"} {
		if s := h.APIAs(tok2, "GET", "/api/rooms/"+roomA.ID+p, nil, nil); s != http.StatusNotFound {
			t.Errorf("orphan room %s: %d", p, s)
		}
	}
	if s := h.APIAs(tok2, "POST", "/api/rooms/"+roomA.ID+"/messages", map[string]any{"text": "Anyone there", "ask": false}, nil); s != http.StatusNotFound {
		t.Errorf("post into orphan room: %d", s)
	}
	// Neither a code issued before the release nor a new code after it can join
	tok3 := invite(t, h, "Bob")
	if s := h.APIAs(tok3, "POST", "/api/rooms/join", map[string]string{"code": leftover}, nil); s != http.StatusNotFound {
		t.Errorf("join orphan room with old code: %d", s)
	}
	if s := h.API("POST", "/api/rooms/"+roomA.ID+"/invite", nil, nil); s != http.StatusNotFound {
		t.Errorf("new code for orphan room: %d", s)
	}
}

func TestRepairedAgentKeepsRoomSharing(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	owner := h.ConnectClient()
	h.StartFakeAgent(a.Token)
	h.WaitFor(owner, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	room := makeRoom(t, h, "Contest", a.Agent.ID, "job1")
	tok2 := invite(t, h, "Alice")
	shareRoom(t, h, room.ID, tok2)
	friend := h.ConnectClientAs(tok2)

	var rot struct{ Token string }
	if s := h.API("POST", "/api/agents/"+a.Agent.ID+"/token", nil, &rot); s != http.StatusOK {
		t.Fatalf("rotate: %d", s)
	}
	h.WaitFor(owner, "old connection dropped", decktest.AgentState(a.Agent.ID, "offline"))
	again := h.StartFakeAgent(rot.Token)
	h.WaitFor(owner, "reconnected", decktest.AgentState(a.Agent.ID, "idle"))
	again.Send(context.Background(), protocol.AgentMessage{Type: "routine.run", Routine: "job1", RoutineName: "Alert", Run: "r1", Text: "After reattach"})
	h.WaitFor(friend, "member still gets posts", func(m protocol.ClientMessage) bool { return m.Type == "task" && m.Task.RoutineID == "job1" })
	var members []any
	if h.API("GET", "/api/rooms/"+room.ID+"/members", nil, &members); len(members) != 1 {
		t.Fatalf("members after rotate: %+v", members)
	}
}

func TestSameRoomCodeRacedOnce(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	room := makeRoom(t, h, "Contest", a.Agent.ID)
	const n = 8
	toks := make([]string, n)
	for i := range toks {
		toks[i] = invite(t, h, "Person")
	}

	// n people with accounts using the same code
	code := roomCode(t, h, decktest.OwnerToken, room.ID)
	codes := make(chan int, n)
	var wg sync.WaitGroup
	for _, tok := range toks {
		wg.Add(1)
		go func(tok string) {
			defer wg.Done()
			codes <- h.APIAs(tok, "POST", "/api/rooms/join", map[string]string{"code": code}, nil)
		}(tok)
	}
	wg.Wait()
	close(codes)
	if ok := countOK(codes); ok != 1 {
		t.Fatalf("join succeeded %d times", ok)
	}
	var members []any
	if h.API("GET", "/api/rooms/"+room.ID+"/members", nil, &members); len(members) != 1 {
		t.Fatalf("members: %d", len(members))
	}

	// n people without accounts using the admin code: only one account is created
	var before []any
	h.API("GET", "/api/owners", nil, &before)
	code = roomCode(t, h, decktest.OwnerToken, room.ID)
	codes = make(chan int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, _ := pairRoom(t, h, code, "Carol")
			codes <- s
		}()
	}
	wg.Wait()
	close(codes)
	if ok := countOK(codes); ok != 1 {
		t.Fatalf("pair/room succeeded %d times", ok)
	}
	var after []any
	if h.API("GET", "/api/owners", nil, &after); len(after) != len(before)+1 {
		t.Fatalf("accounts %d -> %d", len(before), len(after))
	}
}

func countOK(ch <-chan int) int {
	ok := 0
	for s := range ch {
		if s == http.StatusOK {
			ok++
		}
	}
	return ok
}

func TestRevokedOwnersRoomIsGoneForMembers(t *testing.T) {
	h := decktest.New(t)
	admin := h.ConnectClient()
	tok2 := invite(t, h, "Alice")
	var fa struct {
		Agent protocol.Agent
		Token string
	}
	h.APIAs(tok2, "POST", "/api/agents", map[string]string{"name": "Agent B"}, &fa)
	friend := h.ConnectClientAs(tok2)
	h.StartFakeAgent(fa.Token)
	h.WaitFor(friend, "friend agent idle", decktest.AgentState(fa.Agent.ID, "idle"))
	var fRoom protocol.Room
	h.APIAs(tok2, "POST", "/api/rooms", map[string]any{"name": "Alice's room", "agentId": fa.Agent.ID}, &fRoom)
	var asked protocol.Task
	if s := h.APIAs(tok2, "POST", "/api/rooms/"+fRoom.ID+"/messages", map[string]any{"text": "Tidy up", "ask": true}, &asked); s != http.StatusCreated {
		t.Fatalf("ask: %d", s)
	}

	tok3 := invite(t, h, "Bob")
	if s := h.APIAs(tok3, "POST", "/api/rooms/join", map[string]string{"code": roomCode(t, h, tok2, fRoom.ID)}, nil); s != http.StatusOK {
		t.Fatalf("join: %d", s)
	}
	colleague := h.ConnectClientAs(tok3)
	if s := h.APIAs(tok3, "GET", "/api/tasks/"+asked.ID+"/events", nil, nil); s != http.StatusOK {
		t.Fatalf("member reads room task before revoke: %d", s)
	}
	_, waiting := pairRoom(t, h, roomCode(t, h, tok2, fRoom.ID), "Waiting")
	if waiting.Status != "pending" {
		t.Fatalf("pending: %+v", waiting)
	}
	var me struct{ OwnerID string }
	h.APIAs(tok2, "GET", "/api/me", nil, &me)

	if s := h.API("DELETE", "/api/owners/"+me.OwnerID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("revoke: %d", s)
	}
	h.WaitFor(colleague, "room.removed", func(m protocol.ClientMessage) bool {
		return m.Type == "room.removed" && m.Room.ID == fRoom.ID
	})
	h.WaitFor(admin, "dropped join", func(m protocol.ClientMessage) bool {
		return m.Type == "join.done" && m.Join.ID == waiting.RequestID
	})
	var rooms []protocol.Room
	if h.APIAs(tok3, "GET", "/api/rooms", nil, &rooms); len(rooms) != 0 {
		t.Fatalf("member rooms after owner revoked: %+v", rooms)
	}
	for _, p := range []string{"/posts", "/messages", "/members"} {
		if s := h.APIAs(tok3, "GET", "/api/rooms/"+fRoom.ID+p, nil, nil); s != http.StatusNotFound {
			t.Errorf("revoked owner's room %s: %d", p, s)
		}
	}
	if s := h.APIAs(tok3, "POST", "/api/rooms/"+fRoom.ID+"/messages", map[string]any{"text": "Anyone there", "ask": false}, nil); s != http.StatusNotFound {
		t.Errorf("post into revoked owner's room: %d", s)
	}
	if s := h.APIAs(tok3, "GET", "/api/tasks/"+asked.ID+"/events", nil, nil); s != http.StatusNotFound {
		t.Errorf("member reads revoked owner's task: %d", s)
	}
	var ans struct{ Status string }
	if s := h.API("POST", "/api/joins/"+waiting.RequestID, map[string]bool{"approve": true}, &ans); s != http.StatusNotFound {
		t.Errorf("approve dropped join: %d %+v", s, ans)
	}
	if s, _ := pollJoin(t, h, waiting.RequestID, waiting.Poll); s != http.StatusNotFound {
		t.Errorf("poll dropped join: %d", s)
	}
	var pending []protocol.JoinRequest
	if h.API("GET", "/api/joins", nil, &pending); len(pending) != 0 {
		t.Errorf("joins after revoke: %+v", pending)
	}
}

func TestServerSensorsAreAdminOnly(t *testing.T) {
	s := newSensorServer(t)
	h := s.Harness
	eventually(t, "server bundle", func() bool { return samples(s.serverData()) >= 1 })
	a := h.CreateAgent("Agent A")
	room := makeRoom(t, h, "Contest", a.Agent.ID)
	tok2 := invite(t, h, "Alice")
	friendID := shareRoom(t, h, room.ID, tok2)

	d0 := s.disks.Load()
	var got map[string]any
	if c := h.APIAs(tok2, "GET", "/api/data", nil, &got); c != http.StatusForbidden || got["netwatch"] != nil || got["disk"] != nil {
		t.Fatalf("member reads server data: %d %v", c, got)
	}
	if c := h.APIAs(tok2, "POST", "/api/data/disk/refresh", nil, nil); c != http.StatusForbidden {
		t.Fatalf("member refreshes server data: %d", c)
	}
	if s.disks.Load() != d0 {
		t.Fatal("member's refresh measured the server")
	}

	h.API("PATCH", "/api/owners/"+friendID, map[string]bool{"admin": true}, nil)
	if c := h.APIAs(tok2, "GET", "/api/data", nil, &got); c != http.StatusOK || got["netwatch"] == nil {
		t.Fatalf("admin reads server data: %d %v", c, got)
	}
	if c := h.APIAs(tok2, "POST", "/api/data/disk/refresh", nil, nil); c != http.StatusOK {
		t.Fatalf("admin refreshes server data: %d", c)
	}
}

// An invited user who attached their own agent also can't view server sensor values or trigger re-measurement through that agent's screen
func TestInvitedAgentGetsNoServerSensors(t *testing.T) {
	s := newSensorServer(t)
	h := s.Harness
	eventually(t, "server bundle", func() bool { return samples(s.serverData()) >= 1 })
	tok2 := invite(t, h, "Alice")
	var mine decktest.Created
	if c := h.APIAs(tok2, "POST", "/api/agents", map[string]string{"name": "Agent B"}, &mine); c != http.StatusCreated {
		t.Fatalf("create: %d", c)
	}
	var ui struct {
		Data map[string]any `json:"data"`
	}
	h.APIAs(tok2, "GET", "/api/agents/"+mine.Agent.ID+"/ui", nil, &ui)
	if _, ok := ui.Data["disk"]; ok {
		t.Fatal("server disk bundle was added to an invited user's agent screen")
	}
	d0 := s.disks.Load()
	h.APIAs(tok2, "POST", "/api/agents/"+mine.Agent.ID+"/data/disk/refresh", nil, nil)
	if s.disks.Load() != d0 {
		t.Fatal("the server re-measured the disk at an invited user's request")
	}

	owner := h.CreateAgent("Agent A")
	if _, ok := s.agentData(owner.Agent.ID)["disk"]; !ok {
		t.Fatal("the owner's agent screen must have the server disk bundle")
	}
}

// A member can't read, by id, messages (tasks) in a room of a released agent
func TestUnlinkedAgentRoomTaskHidden(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	owner := h.ConnectClient()
	h.StartFakeAgent(a.Token)
	h.WaitFor(owner, "agent idle", decktest.AgentState(a.Agent.ID, "idle"))
	room := makeRoom(t, h, "Room A", a.Agent.ID)
	var asked protocol.Task
	if s := h.API("POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "Tidy up", "ask": true}, &asked); s != http.StatusCreated {
		t.Fatalf("ask: %d", s)
	}
	tok2 := invite(t, h, "Alice")
	shareRoom(t, h, room.ID, tok2)
	if s := h.APIAs(tok2, "GET", "/api/tasks/"+asked.ID+"/events", nil, nil); s != http.StatusOK {
		t.Fatalf("member reads room task before unlink: %d", s)
	}
	if s := h.API("DELETE", "/api/agents/"+a.Agent.ID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("unlink: %d", s)
	}
	if s := h.APIAs(tok2, "GET", "/api/tasks/"+asked.ID+"/events", nil, nil); s == http.StatusOK {
		t.Fatal("a member reads a task in a released agent's room")
	}
}
