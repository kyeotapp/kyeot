package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

// invite has the first user issue an invite code, and a new phone gets a token with that code.
func invite(t *testing.T, h *decktest.Harness, name string) string {
	t.Helper()
	var inv struct{ Code string }
	if s := h.API("POST", "/api/owners/invite", nil, &inv); s != http.StatusOK || len(inv.Code) != 6 {
		t.Fatalf("invite: %d %+v", s, inv)
	}
	return redeem(t, h, inv.Code, name, http.StatusOK)
}

func redeem(t *testing.T, h *decktest.Harness, code, name string, want int) string {
	t.Helper()
	tok := redeemNoTerms(t, h, code, name, want)
	if tok != "" {
		h.AgreeTerms(tok)
	}
	return tok
}

// redeemNoTerms is a new user who has not yet agreed to the terms.
func redeemNoTerms(t *testing.T, h *decktest.Harness, code, name string, want int) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"code": code, "name": name})
	req, _ := http.NewRequest("POST", h.URL+"/pair/owner", bytes.NewReader(body))
	req.Header.Set("X-Kyeot-Pair", "1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct{ Token string }
	json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode != want {
		t.Fatalf("redeem: %d, want %d", res.StatusCode, want)
	}
	return out.Token
}

func TestInvitedOwnerIsSeparate(t *testing.T) {
	h := decktest.New(t)
	h.CreateAgent("Agent A")
	tok2 := invite(t, h, "Bob")

	var agents []protocol.Agent
	if s := h.APIAs(tok2, "GET", "/api/agents", nil, &agents); s != http.StatusOK || len(agents) != 0 {
		t.Fatalf("second owner sees agents: %d %+v", s, agents)
	}
	var inv struct{ Code string }
	if s := h.APIAs(tok2, "POST", "/api/owners/invite", nil, &inv); s != http.StatusForbidden {
		t.Fatalf("only first owner invites: %d", s)
	}
	if s := h.APIAs("wrong-token", "GET", "/api/agents", nil, nil); s != http.StatusUnauthorized {
		t.Fatalf("bad token: %d", s)
	}
}

func TestInviteCodeIsSingleUse(t *testing.T) {
	h := decktest.New(t)
	var inv struct{ Code string }
	h.API("POST", "/api/owners/invite", nil, &inv)
	redeem(t, h, inv.Code, "Bob", http.StatusOK)
	redeem(t, h, inv.Code, "Again", http.StatusUnauthorized)
}

func TestSharedRoomIsReadOnlyFeed(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	owner := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(owner, "idle", decktest.AgentState(a.Agent.ID, "idle"))

	tok2 := invite(t, h, "Bob")
	friend := h.ConnectClientAs(tok2)

	var room protocol.Room
	h.API("POST", "/api/rooms", map[string]any{"name": "Contest", "agentId": a.Agent.ID, "routineIds": []string{"job1"}}, &room)

	// before sharing: not visible to Bob
	if s := h.APIAs(tok2, "GET", "/api/rooms/"+room.ID+"/posts", nil, nil); s != http.StatusNotFound {
		t.Fatalf("posts before share: %d", s)
	}
	agent.Send(context.Background(), protocol.AgentMessage{Type: "routine.run", Routine: "job1", RoutineName: "contest-alert", Run: "r1", Text: "first result"})
	h.Quiet(friend, "task before share", 300*time.Millisecond, func(m protocol.ClientMessage) bool { return m.Type == "task" })

	// share
	var inv struct{ Code string }
	if s := h.API("POST", "/api/rooms/"+room.ID+"/invite", nil, &inv); s != http.StatusOK {
		t.Fatalf("room invite: %d", s)
	}
	if s := h.APIAs(tok2, "POST", "/api/rooms/"+room.ID+"/invite", nil, nil); s != http.StatusNotFound {
		t.Fatalf("non-owner room invite: %d", s)
	}
	var joined protocol.Room
	if s := h.APIAs(tok2, "POST", "/api/rooms/join", map[string]string{"code": inv.Code}, &joined); s != http.StatusOK || !joined.Shared || joined.ID != room.ID {
		t.Fatalf("join: %d %+v", s, joined)
	}

	var rooms []protocol.Room
	h.APIAs(tok2, "GET", "/api/rooms", nil, &rooms)
	if len(rooms) != 1 || !rooms[0].Shared || rooms[0].OwnerName != "me" {
		t.Fatalf("friend rooms: %+v", rooms)
	}
	var mine []protocol.Room
	h.API("GET", "/api/rooms", nil, &mine)
	if len(mine) != 1 || mine[0].Shared || mine[0].OwnerName != "" {
		t.Fatalf("owner rooms: %+v", mine)
	}

	// past posts and new posts
	var posts []struct {
		Task    protocol.Task `json:"task"`
		Preview string        `json:"preview"`
	}
	h.APIAs(tok2, "GET", "/api/rooms/"+room.ID+"/posts", nil, &posts)
	if len(posts) != 1 || posts[0].Preview != "first result" {
		t.Fatalf("friend posts: %+v", posts)
	}
	agent.Send(context.Background(), protocol.AgentMessage{Type: "routine.run", Routine: "job1", RoutineName: "contest-alert", Run: "r2", Text: "second result"})
	var newTask string
	h.WaitFor(friend, "routine task live", func(m protocol.ClientMessage) bool {
		if m.Type == "task" && m.Task.RoutineID == "job1" {
			newTask = m.Task.ID
			return true
		}
		return false
	})
	h.WaitFor(friend, "routine event live", func(m protocol.ClientMessage) bool { return m.Type == "event" && m.Event.TaskID == newTask })
	var events []json.RawMessage
	if s := h.APIAs(tok2, "GET", "/api/tasks/"+newTask+"/events", nil, &events); s != http.StatusOK || len(events) == 0 {
		t.Fatalf("friend events: %d %d", s, len(events))
	}

	// read only
	if s := h.APIAs(tok2, "POST", "/api/tasks/"+newTask+"/messages", map[string]string{"text": "me too"}, nil); s != http.StatusNotFound {
		t.Fatalf("member message to task: %d", s)
	}
	if s := h.APIAs(tok2, "POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "me too", "ask": true}, nil); s != http.StatusForbidden {
		t.Fatalf("member asks the agent without the role: %d", s)
	}
	if s := h.APIAs(tok2, "DELETE", "/api/rooms/"+room.ID, nil, nil); s != http.StatusNotFound {
		t.Fatalf("member delete room: %d", s)
	}

	// others' conversations outside the routine are not visible
	var other protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "secret talk"}, &other)
	if s := h.APIAs(tok2, "GET", "/api/tasks/"+other.ID+"/events", nil, nil); s != http.StatusNotFound {
		t.Fatalf("friend sees owner's private task: %d", s)
	}

	// member list, leaving
	var members []struct{ OwnerID, Name string }
	h.API("GET", "/api/rooms/"+room.ID+"/members", nil, &members)
	if len(members) != 1 || members[0].Name != "Bob" {
		t.Fatalf("members: %+v", members)
	}
	if s := h.APIAs(tok2, "DELETE", "/api/rooms/"+room.ID+"/members/me", nil, nil); s != http.StatusForbidden {
		t.Fatalf("member removes someone else: %d", s)
	}
	if s := h.APIAs(tok2, "DELETE", "/api/rooms/"+room.ID+"/members/"+members[0].OwnerID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("leave: %d", s)
	}
	h.WaitFor(friend, "room.removed", func(m protocol.ClientMessage) bool { return m.Type == "room.removed" && m.Room.ID == room.ID })
	if s := h.APIAs(tok2, "GET", "/api/rooms/"+room.ID+"/posts", nil, nil); s != http.StatusNotFound {
		t.Fatalf("posts after leave: %d", s)
	}
	if s := h.APIAs(tok2, "GET", "/api/tasks/"+newTask+"/events", nil, nil); s != http.StatusNotFound {
		t.Fatalf("events after leave: %d", s)
	}
}

func TestJoinOwnRoomRejected(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	var room protocol.Room
	h.API("POST", "/api/rooms", map[string]any{"name": "Contest", "agentId": a.Agent.ID}, &room)
	var inv struct{ Code string }
	h.API("POST", "/api/rooms/"+room.ID+"/invite", nil, &inv)
	if s := h.API("POST", "/api/rooms/join", map[string]string{"code": inv.Code}, nil); s != http.StatusBadRequest {
		t.Fatalf("join own room: %d", s)
	}
}

func TestOtherOwnerCannotFetchUploadByToken(t *testing.T) {
	h := decktest.New(t)
	var up protocol.Attachment
	if s := h.Post("/api/uploads?name=a.png", "image/png", []byte("PNGDATA"), &up); s != http.StatusCreated {
		t.Fatalf("upload: %d", s)
	}
	tok2 := invite(t, h, "Bob")
	if s := h.APIAs(tok2, "GET", "/api/uploads/"+up.ID, nil, nil); s != http.StatusNotFound {
		t.Fatalf("other owner fetched upload: %d", s)
	}
	if s := h.API("GET", "/api/uploads/"+up.ID, nil, nil); s != http.StatusOK {
		t.Fatalf("uploader fetch: %d", s)
	}
}

func TestRemoveMemberGuards(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	owner := h.ConnectClient()
	var room protocol.Room
	h.API("POST", "/api/rooms", map[string]any{"name": "Contest", "agentId": a.Agent.ID}, &room)
	h.WaitFor(owner, "room", func(m protocol.ClientMessage) bool { return m.Type == "room" && m.Room.ID == room.ID })
	tok2 := invite(t, h, "Bob")
	friend := h.ConnectClientAs(tok2)

	if s := h.API("DELETE", "/api/rooms/"+room.ID+"/members/me", nil, nil); s != http.StatusBadRequest {
		t.Fatalf("owner removes self: %d", s)
	}
	h.Quiet(owner, "room.removed to owner", 200*time.Millisecond, func(m protocol.ClientMessage) bool { return m.Type == "room.removed" })

	var friendID struct{ OwnerID string }
	var inv struct{ Code string }
	h.API("POST", "/api/rooms/"+room.ID+"/invite", nil, &inv)
	h.APIAs(tok2, "POST", "/api/rooms/join", map[string]string{"code": inv.Code}, nil)
	var members []struct{ OwnerID string }
	h.API("GET", "/api/rooms/"+room.ID+"/members", nil, &members)
	friendID.OwnerID = members[0].OwnerID
	h.API("DELETE", "/api/rooms/"+room.ID+"/members/"+friendID.OwnerID, nil, nil)
	h.WaitFor(friend, "removed", func(m protocol.ClientMessage) bool { return m.Type == "room.removed" })

	if s := h.API("DELETE", "/api/rooms/"+room.ID+"/members/"+friendID.OwnerID, nil, nil); s != http.StatusNotFound {
		t.Fatalf("remove non-member: %d", s)
	}
	h.Quiet(friend, "room.removed to non-member", 200*time.Millisecond, func(m protocol.ClientMessage) bool { return m.Type == "room.removed" })
}

func TestDeletedTaskLeavesSharedRoom(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	owner := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(owner, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	var room protocol.Room
	h.API("POST", "/api/rooms", map[string]any{"name": "Contest", "agentId": a.Agent.ID, "routineIds": []string{"job1"}}, &room)
	tok2 := invite(t, h, "Bob")
	friend := h.ConnectClientAs(tok2)
	var inv struct{ Code string }
	h.API("POST", "/api/rooms/"+room.ID+"/invite", nil, &inv)
	h.APIAs(tok2, "POST", "/api/rooms/join", map[string]string{"code": inv.Code}, nil)

	agent.Send(context.Background(), protocol.AgentMessage{Type: "routine.run", Routine: "job1", RoutineName: "contest-alert", Run: "r1", Text: "result"})
	var id string
	h.WaitFor(friend, "routine task", func(m protocol.ClientMessage) bool {
		if m.Type == "task" && m.Task.RoutineID == "job1" {
			id = m.Task.ID
			return true
		}
		return false
	})

	if s := h.API("DELETE", "/api/tasks/"+id, nil, nil); s != http.StatusNoContent {
		t.Fatalf("delete: %d", s)
	}
	h.WaitFor(friend, "task.removed to member", func(m protocol.ClientMessage) bool { return m.Type == "task.removed" && m.Task.ID == id })
	var posts []json.RawMessage
	for _, tok := range []string{decktest.OwnerToken, tok2} {
		h.APIAs(tok, "GET", "/api/rooms/"+room.ID+"/posts", nil, &posts)
		if len(posts) != 0 {
			t.Fatalf("deleted task still in room posts: %d", len(posts))
		}
	}
	if s := h.APIAs(tok2, "GET", "/api/tasks/"+id+"/events", nil, nil); s != http.StatusNotFound {
		t.Fatalf("member opens deleted task: %d", s)
	}
}

func TestMeNameShowsInMembers(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	var room protocol.Room
	h.API("POST", "/api/rooms", map[string]any{"name": "Contest", "agentId": a.Agent.ID}, &room)
	tok2 := invite(t, h, "Bob")

	var me struct {
		OwnerID, Name string
		CanInvite     bool
	}
	h.APIAs(tok2, "GET", "/api/me", nil, &me)
	if me.Name != "Bob" || me.OwnerID == "" || me.CanInvite {
		t.Fatalf("me: %+v", me)
	}
	var first struct{ CanInvite bool }
	if h.API("GET", "/api/me", nil, &first); !first.CanInvite {
		t.Fatal("first owner should be able to invite")
	}
	var patchedOwner struct {
		OwnerID, Name, Role string
		CanInvite           bool
	}
	h.API("PATCH", "/api/me", map[string]string{"name": "newowner"}, &patchedOwner)
	if patchedOwner.Name != "newowner" || !patchedOwner.CanInvite || patchedOwner.Role != "owner" {
		t.Fatalf("patched owner: %+v", patchedOwner)
	}

	if s := h.APIAs(tok2, "PATCH", "/api/me", map[string]string{"name": " "}, nil); s != http.StatusBadRequest {
		t.Fatalf("blank name: %d", s)
	}
	var patchedMe struct {
		OwnerID, Name, Role string
		CanInvite           bool
	}
	h.APIAs(tok2, "PATCH", "/api/me", map[string]string{"name": "June"}, &patchedMe)
	if patchedMe.Name != "June" || patchedMe.CanInvite || patchedMe.Role != "" {
		t.Fatalf("patched user: %+v", patchedMe)
	}
	var inv struct{ Code string }
	h.API("POST", "/api/rooms/"+room.ID+"/invite", nil, &inv)
	h.APIAs(tok2, "POST", "/api/rooms/join", map[string]string{"code": inv.Code}, nil)
	var members []struct{ OwnerID, Name string }
	h.API("GET", "/api/rooms/"+room.ID+"/members", nil, &members)
	if len(members) != 1 || members[0].Name != "June" || members[0].OwnerID != me.OwnerID {
		t.Fatalf("members: %+v", members)
	}
}

func TestRoomTalkAndAsk(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	owner := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(owner, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	var room protocol.Room
	h.API("POST", "/api/rooms", map[string]any{"name": "Contest", "agentId": a.Agent.ID}, &room)
	tok2 := invite(t, h, "Bob")
	friend := h.ConnectClientAs(tok2)
	var inv struct{ Code string }
	h.API("POST", "/api/rooms/"+room.ID+"/invite", nil, &inv)
	h.APIAs(tok2, "POST", "/api/rooms/join", map[string]string{"code": inv.Code}, nil)
	h.WaitFor(owner, "owner hears the join", func(m protocol.ClientMessage) bool { return m.Type == "room" && m.Room.ID == room.ID && !m.Room.Shared })
	var members []struct{ OwnerID, Role string }
	h.API("GET", "/api/rooms/"+room.ID+"/members", nil, &members)
	friendID := members[0].OwnerID
	if members[0].Role != "member" {
		t.Fatalf("default role: %+v", members[0])
	}
	tasks := func() int {
		var list []protocol.Task
		h.API("GET", "/api/tasks", nil, &list)
		return len(list)
	}
	before := tasks()

	// person to person: a member's post, the owner's ask:false post. The agent does not move
	if s := h.APIAs(tok2, "POST", "/api/rooms/"+room.ID+"/messages", map[string]string{"text": "seen this?"}, nil); s != http.StatusCreated {
		t.Fatalf("member talk: %d", s)
	}
	h.WaitFor(owner, "room.message to owner", func(m protocol.ClientMessage) bool {
		return m.Type == "room.message" && m.RoomMessage.Text == "seen this?" && m.RoomMessage.Name == "Bob"
	})
	if s := h.API("POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "yes, seen it", "ask": false}, nil); s != http.StatusCreated {
		t.Fatalf("owner talk: %d", s)
	}
	h.WaitFor(friend, "room.message to member", func(m protocol.ClientMessage) bool {
		return m.Type == "room.message" && m.RoomMessage.Text == "yes, seen it"
	})
	var msgs []protocol.RoomMessage
	h.APIAs(tok2, "GET", "/api/rooms/"+room.ID+"/messages", nil, &msgs)
	if len(msgs) != 2 || msgs[0].Text != "seen this?" || msgs[1].Name != "me" {
		t.Fatalf("messages: %+v", msgs)
	}
	if got := tasks(); got != before {
		t.Fatalf("talk started agent tasks: %d -> %d", before, got)
	}
	if s := h.APIAs(tok2, "POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "x", "ask": false, "attachments": []string{"nope"}}, nil); s != http.StatusBadRequest {
		t.Fatalf("talk with attachment: %d", s)
	}

	// permissions: a non-member, a wrong role, a non-owner
	if s := h.API("PATCH", "/api/rooms/"+room.ID+"/members/nobody", map[string]string{"role": "agent"}, nil); s != http.StatusNotFound {
		t.Fatalf("role for non-member: %d", s)
	}
	if s := h.API("PATCH", "/api/rooms/"+room.ID+"/members/"+friendID, map[string]string{"role": "admin"}, nil); s != http.StatusBadRequest {
		t.Fatalf("bad role: %d", s)
	}
	if s := h.APIAs(tok2, "PATCH", "/api/rooms/"+room.ID+"/members/"+friendID, map[string]string{"role": "agent"}, nil); s != http.StatusNotFound {
		t.Fatalf("member grants itself: %d", s)
	}

	// when the owner grants permission, a member can ask. The task is the owner's, and who called is recorded
	if s := h.API("PATCH", "/api/rooms/"+room.ID+"/members/"+friendID, map[string]string{"role": "agent"}, nil); s != http.StatusOK {
		t.Fatalf("grant: %d", s)
	}
	h.WaitFor(friend, "canAsk room", func(m protocol.ClientMessage) bool { return m.Type == "room" && m.Room.ID == room.ID && m.Room.CanAsk })
	var asked protocol.Task
	if s := h.APIAs(tok2, "POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "tidy up", "ask": true}, &asked); s != http.StatusCreated || asked.AskedBy != friendID {
		t.Fatalf("member asks: %d %+v", s, asked)
	}
	var events []protocol.Event
	h.API("GET", "/api/tasks/"+asked.ID+"/events", nil, &events)
	if len(events) == 0 || !bytes.Contains(events[0].Payload, []byte(`"byName":"Bob"`)) {
		t.Fatalf("asked-by not recorded: %+v", events)
	}
	// only the owner approves, even for a task a member called
	h.WaitFor(owner, "approval waits for the owner", decktest.TaskState(asked.ID, "waiting"))
	if s := h.APIAs(tok2, "POST", "/api/tasks/"+asked.ID+"/approvals/a1", map[string]string{"choice": "승인"}, nil); s != http.StatusNotFound {
		t.Fatalf("member approves: %d", s)
	}
	if s := h.API("POST", "/api/tasks/"+asked.ID+"/approvals/a1", map[string]string{"choice": "승인"}, nil); s != http.StatusNoContent {
		t.Fatalf("owner approves: %d", s)
	}
	h.WaitFor(owner, "agent done", decktest.TaskState(asked.ID, "done"))
	if s := h.APIAs(tok2, "POST", "/api/tasks/"+asked.ID+"/messages", map[string]string{"text": "shorter"}, nil); s != http.StatusNoContent {
		t.Fatalf("agent member follows up: %d", s)
	}
	if s := h.APIAs(tok2, "POST", "/api/tasks/"+asked.ID+"/messages", map[string]any{"text": "fixed", "editId": "1"}, nil); s != http.StatusForbidden {
		t.Fatalf("member edits: %d", s)
	}

	// once permission is withdrawn, asking again is not possible
	h.API("PATCH", "/api/rooms/"+room.ID+"/members/"+friendID, map[string]string{"role": "member"}, nil)
	if s := h.APIAs(tok2, "POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "Again", "ask": true}, nil); s != http.StatusForbidden {
		t.Fatalf("ask after revoke: %d", s)
	}
	if s := h.APIAs(tok2, "POST", "/api/tasks/"+asked.ID+"/messages", map[string]string{"text": "Again"}, nil); s != http.StatusNotFound {
		t.Fatalf("follow up after revoke: %d", s)
	}
	_ = agent

	// an old app's (no ask) owner post goes to the agent as it does now
	var legacy protocol.Task
	if s := h.API("POST", "/api/rooms/"+room.ID+"/messages", map[string]string{"text": "old app"}, &legacy); s != http.StatusCreated || legacy.ID == "" {
		t.Fatalf("legacy owner message: %d %+v", s, legacy)
	}
}

// Even if only the raw report is sent, the server shapes it for the app, infers features, and learns routines.
func TestReportIsNormalizedAndFeaturesInferred(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("bot")
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))

	var agents []protocol.Agent
	h.API("GET", "/api/agents", nil, &agents)
	if len(agents[0].Features) != 0 {
		t.Fatalf("features before any message: %v", agents[0].Features)
	}

	agent.Send(context.Background(), protocol.AgentMessage{Type: "report", Report: json.RawMessage(`{"routines":[{"id":"job1","name":"daily-news","state":"paused","cron":"0 9 * * *"}]}`)})
	h.WaitFor(c, "features after report", func(m protocol.ClientMessage) bool {
		return m.Type == "agent" && m.Agent.ID == a.Agent.ID && slices.Contains(m.Agent.Features, "routines") && !slices.Contains(m.Agent.Features, "costs")
	})
	var rep struct {
		Report map[string]any `json:"report"`
	}
	h.API("GET", "/api/agents/"+a.Agent.ID+"/report", nil, &rep)
	if _, ok := rep.Report["periods"].([]any); !ok {
		t.Fatalf("periods should be a list: %+v", rep.Report)
	}
	r := rep.Report["routines"].([]any)[0].(map[string]any)
	if r["state"] != "off" || r["on"] != false || r["schedule"] != "매일 09:00" || r["title"] != "daily-news" {
		t.Fatalf("normalized routine: %+v", r)
	}

	agent.Send(context.Background(), protocol.AgentMessage{Type: "routine.run", Routine: "job2", RoutineName: "weekly", Run: "r1", Text: "result"})
	h.WaitFor(c, "task from run", func(m protocol.ClientMessage) bool { return m.Type == "task" && m.Task.RoutineID == "job2" })
	var list []struct {
		ID, Title string
		Reported  bool
	}
	h.API("GET", "/api/agents/"+a.Agent.ID+"/routines", nil, &list)
	if len(list) != 2 || list[0].ID != "job1" || !list[0].Reported || list[1].ID != "job2" || list[1].Title != "weekly" || list[1].Reported {
		t.Fatalf("routines list: %+v", list)
	}

	agent.Send(context.Background(), protocol.AgentMessage{Type: "data.put", Name: "revenue", Data: json.RawMessage(`{"x":1}`)})
	h.WaitFor(c, "ui feature", func(m protocol.ClientMessage) bool {
		return m.Type == "agent" && slices.Contains(m.Agent.Features, "ui")
	})

	// a report that is not an object is only rejected, and the connection is kept
	agent.Send(context.Background(), protocol.AgentMessage{Type: "report", Report: json.RawMessage(`[]`)})
	agent.Send(context.Background(), protocol.AgentMessage{Type: "status", State: "working"})
	h.WaitFor(c, "agent still connected", decktest.AgentState(a.Agent.ID, "working"))
}

// features can also be announced in advance via hello.
func TestHelloFeatures(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("bot")
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	agent.Send(context.Background(), protocol.AgentMessage{Type: "hello", Kind: "mybot", Features: []string{"ui"}})
	h.WaitFor(c, "hello features", func(m protocol.ClientMessage) bool {
		return m.Type == "agent" && slices.Contains(m.Agent.Features, "ui")
	})
}

// rejoin uses an invite code while sending the token it was using.
func rejoin(t *testing.T, h *decktest.Harness, code, name, old string, want int) (token, ownerID string, rejoined bool) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"code": code, "name": name, "token": old})
	req, _ := http.NewRequest("POST", h.URL+"/pair/owner", bytes.NewReader(body))
	req.Header.Set("X-Kyeot-Pair", "1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Token    string
		OwnerID  string
		Rejoined bool
	}
	json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode != want {
		t.Fatalf("rejoin: %d, want %d", res.StatusCode, want)
	}
	return out.Token, out.OwnerID, out.Rejoined
}

func newInviteCode(t *testing.T, h *decktest.Harness) string {
	t.Helper()
	var inv struct{ Code string }
	if s := h.API("POST", "/api/owners/invite", nil, &inv); s != http.StatusOK {
		t.Fatalf("invite: %d", s)
	}
	return inv.Code
}

func meOf(t *testing.T, h *decktest.Harness, tok string) (id, name string, status int) {
	t.Helper()
	var me struct{ OwnerID, Name string }
	status = h.APIAs(tok, "GET", "/api/me", nil, &me)
	return me.OwnerID, me.Name, status
}

func countNamed(t *testing.T, h *decktest.Harness, name string) int {
	t.Helper()
	var list []struct{ Name string }
	h.API("GET", "/api/owners", nil, &list)
	n := 0
	for _, u := range list {
		if u.Name == name {
			n++
		}
	}
	return n
}

func TestRejoinKeepsSameAccount(t *testing.T) {
	h := decktest.New(t)
	old := invite(t, h, "Dan")
	id, _, _ := meOf(t, h, old)
	h.APIAs(old, "POST", "/api/agents", map[string]string{"name": "Danbot"}, nil)

	tok, got, rejoined := rejoin(t, h, newInviteCode(t, h), "othername", old, http.StatusOK)
	if !rejoined || got != id || tok != old {
		t.Fatalf("rejoin: rejoined=%v id=%s want %s, the token must stay the same", rejoined, got, id)
	}
	if _, _, s := meOf(t, h, old); s != http.StatusOK {
		t.Fatalf("the old token must keep working: %d", s)
	}
	if nid, name, s := meOf(t, h, tok); s != http.StatusOK || nid != id || name != "Dan" {
		t.Fatalf("new token: %d %s %s", s, nid, name)
	}
	var agents []protocol.Agent
	h.APIAs(tok, "GET", "/api/agents", nil, &agents)
	if len(agents) != 1 || agents[0].Name != "Danbot" {
		t.Fatalf("my agents must carry over: %+v", agents)
	}
	if n := countNamed(t, h, "Dan") + countNamed(t, h, "othername"); n != 1 {
		t.Fatalf("there must be one account: %d", n)
	}
}

func TestRejoinCodeStillSingleUse(t *testing.T) {
	h := decktest.New(t)
	old := invite(t, h, "Dan")
	code := newInviteCode(t, h)
	tok, _, _ := rejoin(t, h, code, "Dan", old, http.StatusOK)
	rejoin(t, h, code, "Dan", tok, http.StatusUnauthorized)
	if _, _, s := meOf(t, h, tok); s != http.StatusOK {
		t.Fatalf("even with the code blocked, the token in use must stay the same: %d", s)
	}
}

func TestRejoinWithBadTokenMakesNewAccount(t *testing.T) {
	h := decktest.New(t)
	invite(t, h, "Dan")
	tok, id, rejoined := rejoin(t, h, newInviteCode(t, h), "Newcomer", "no-such-token", http.StatusOK)
	if rejoined || tok == "" {
		t.Fatalf("an invalid token gives a new account: rejoined=%v", rejoined)
	}
	if got, name, _ := meOf(t, h, tok); got != id || name != "Newcomer" {
		t.Fatalf("new account: %s %s", got, name)
	}
}

func TestRejoinIgnoresOwnerTokens(t *testing.T) {
	h := decktest.New(t)
	ownerID, _, _ := meOf(t, h, decktest.OwnerToken)
	tok, id, rejoined := rejoin(t, h, newInviteCode(t, h), "ownerfake", decktest.OwnerToken, http.StatusOK)
	if rejoined || id == ownerID {
		t.Fatalf("ignore the owner token and make a new account: rejoined=%v", rejoined)
	}
	if _, _, s := meOf(t, h, decktest.OwnerToken); s != http.StatusOK {
		t.Fatalf("the owner token must stay alive: %d", s)
	}
	if got, role, _ := func() (string, string, int) {
		var me struct{ OwnerID, Role string }
		s := h.APIAs(tok, "GET", "/api/me", nil, &me)
		return me.OwnerID, me.Role, s
	}(); got == ownerID || role != "" {
		t.Fatalf("a new account is an ordinary user: %s role=%q", got, role)
	}
}

func TestRejoinRevokedTokenMakesNewAccount(t *testing.T) {
	h := decktest.New(t)
	old := invite(t, h, "Dan")
	id, _, _ := meOf(t, h, old)
	h.API("DELETE", "/api/owners/"+id, nil, nil)
	tok, got, rejoined := rejoin(t, h, newInviteCode(t, h), "Dan", old, http.StatusOK)
	if rejoined || got == id || tok == "" {
		t.Fatalf("a revoked account is not revived: rejoined=%v", rejoined)
	}
}

func TestRejoinKeepsAdminAndOtherDevices(t *testing.T) {
	h := decktest.New(t)
	old := invite(t, h, "Dan")
	id, _, _ := meOf(t, h, old)
	h.API("PATCH", "/api/owners/"+id, map[string]bool{"admin": true}, nil)
	tok, _, rejoined := rejoin(t, h, newInviteCode(t, h), "Dan", old, http.StatusOK)
	var me struct{ Role string }
	h.APIAs(tok, "GET", "/api/me", nil, &me)
	if !rejoined || me.Role != "admin" {
		t.Fatalf("admin rights carry over: rejoined=%v role=%q", rejoined, me.Role)
	}
}

func TestRejoinOldClientIgnoresField(t *testing.T) {
	h := decktest.New(t)
	old := invite(t, h, "Dan")
	tok := redeem(t, h, newInviteCode(t, h), "Dan2", http.StatusOK) // without a token field: current behavior
	if _, _, s := meOf(t, h, old); s != http.StatusOK {
		t.Fatalf("a request without the field does not touch the old token: %d", s)
	}
	if id, _, _ := meOf(t, h, tok); id == "" {
		t.Fatal("new account")
	}
}

// After a handover, the new owner's token is an ordinary token in owner_tokens. It is still not a target for same-account takeover.
func TestRejoinIgnoresHeirAfterHandover(t *testing.T) {
	h, _, _ := handoverHarness(t)
	heirTok := invite(t, h, "Alice")
	var heir struct{ OwnerID string }
	h.APIAs(heirTok, "GET", "/api/me", nil, &heir)
	if s := h.API("POST", "/api/owner/handover", map[string]string{"heirId": heir.OwnerID, "confirm": "지우기"}, nil); s != http.StatusNoContent {
		t.Fatalf("handover: %d", s)
	}
	var inv struct{ Code string }
	if s := h.APIAs(heirTok, "POST", "/api/owners/invite", nil, &inv); s != http.StatusOK {
		t.Fatalf("heir invite: %d", s)
	}
	tok, id, rejoined := rejoin(t, h, inv.Code, "Alice2", heirTok, http.StatusOK)
	if rejoined || id == heir.OwnerID || tok == "" {
		t.Fatalf("ignore the new owner token and make a new account: rejoined=%v", rejoined)
	}
	if s := h.APIAs(heirTok, "GET", "/api/owners", nil, nil); s != http.StatusOK {
		t.Fatalf("the new owner token must stay alive: %d", s)
	}
}
