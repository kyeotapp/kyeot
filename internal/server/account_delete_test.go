package server_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/fakeagent"
	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/server"
)

func uploadAs(t *testing.T, h *decktest.Harness, token, name, body string) protocol.Attachment {
	t.Helper()
	req, _ := http.NewRequest("POST", h.URL+"/api/uploads?name="+name, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "text/plain")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var a protocol.Attachment
	json.NewDecoder(res.Body).Decode(&a)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("upload: %d", res.StatusCode)
	}
	return a
}

// rowsContaining returns, as "table: row", every row in any DB table where needle appears in any column.
func rowsContaining(t *testing.T, path string, needles ...string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	if len(tables) < 15 {
		t.Fatalf("tables: %v", tables)
	}
	var hits []string
	for _, tb := range tables {
		rs, err := db.Query(`SELECT * FROM ` + tb)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rs.Columns()
		for rs.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			rs.Scan(ptrs...)
			line := fmt.Sprintf("%s", vals)
			for _, n := range needles {
				if n != "" && strings.Contains(line, n) {
					hits = append(hits, tb+": "+line)
					break
				}
			}
		}
		rs.Close()
	}
	return hits
}

// An invited user deletes their own account. Everything of theirs disappears; only posts in others' rooms remain, as "deleted user".
func TestDeleteMyAccount(t *testing.T) {
	dir := t.TempDir()
	dbPath, upDir := filepath.Join(dir, "kyeot.db"), filepath.Join(dir, "uploads")
	h := decktest.NewWith(t, func(o *server.Options) { o.DBPath, o.UploadDir = dbPath, upDir })

	// owner: agent and room
	mine := h.CreateAgent("Agent A")
	owner := h.ConnectClient()
	h.StartFakeAgent(mine.Token)
	h.WaitFor(owner, "Agent A idle", decktest.AgentState(mine.Agent.ID, "idle"))
	r1 := makeRoom(t, h, "Contest", mine.Agent.ID)

	// person to delete: admin, own agent, tasks, attachments, rooms, devices, codes, posts in others' rooms
	const name = "leaver"
	tok2 := invite(t, h, name)
	var me struct{ OwnerID string }
	h.APIAs(tok2, "GET", "/api/me", nil, &me)
	friendID := me.OwnerID
	if s := h.API("PATCH", "/api/owners/"+friendID, map[string]bool{"admin": true}, nil); s != http.StatusNoContent {
		t.Fatalf("admin: %d", s)
	}
	var fa decktest.Created
	if s := h.APIAs(tok2, "POST", "/api/agents", map[string]string{"name": "friendbot"}, &fa); s != http.StatusCreated {
		t.Fatalf("friend agent: %d", s)
	}
	friendClient := h.ConnectClientAs(tok2)
	fake := h.StartFakeAgent(fa.Token)
	h.WaitFor(friendClient, "friendbot idle", decktest.AgentState(fa.Agent.ID, "idle"))
	fake.Send(context.Background(), protocol.AgentMessage{Type: "data.put", Name: "revenue", Data: json.RawMessage(`{"x":1}`)})
	recv(t, fake.Acks)
	fake.Send(context.Background(), map[string]any{"type": "ui.put", "screen": json.RawMessage(`{"version":1,"id":"mine","title":"t","icon":"i","blocks":[{"type":"divider"}]}`)})
	recv(t, fake.Acks)
	fake.Send(context.Background(), protocol.AgentMessage{Type: "report", Report: json.RawMessage(`{"routines":[{"id":"job1","name":"daily","state":"active","cron":"0 9 * * *"}]}`)})
	h.WaitFor(friendClient, "report", func(m protocol.ClientMessage) bool {
		return m.Type == "agent" && m.Agent.ID == fa.Agent.ID && slices.Contains(m.Agent.Features, "routines")
	})
	fake.Send(context.Background(), protocol.AgentMessage{Type: "routine.run", Routine: "job1", RoutineName: "daily", Run: "r1", Text: "routine result"})
	h.WaitFor(friendClient, "routine task", func(m protocol.ClientMessage) bool { return m.Task != nil && m.Task.RoutineID == "job1" })
	fake.Send(context.Background(), protocol.AgentMessage{Type: "routine.run", Routine: "job1", Run: "r0", Quiet: true})
	for i := 0; ; i++ {
		var runs []protocol.RoutineRun
		if h.APIAs(tok2, "GET", "/api/agents/"+fa.Agent.ID+"/routines/job1/history", nil, &runs); len(runs) == 1 {
			break
		}
		if i > 100 {
			t.Fatal("quiet run not recorded")
		}
		time.Sleep(20 * time.Millisecond)
	}
	ownFile := uploadAs(t, h, tok2, "mine.txt", "my file")
	loose := uploadAs(t, h, tok2, "loose.txt", "unattached file")
	var ownTask protocol.Task
	if s := h.APIAs(tok2, "POST", "/api/tasks", map[string]any{"agentId": fa.Agent.ID, "text": "my task", "attachments": []string{ownFile.ID}}, &ownTask); s != http.StatusCreated {
		t.Fatalf("own task: %d", s)
	}
	rf := protocol.Room{}
	if s := h.APIAs(tok2, "POST", "/api/rooms", map[string]any{"name": "friend room", "agentId": fa.Agent.ID, "routineIds": []string{"job1"}}, &rf); s != http.StatusCreated {
		t.Fatalf("friend room: %d", s)
	}
	tok3 := invite(t, h, "colleague")
	colleague := h.ConnectClientAs(tok3)
	if s := h.APIAs(tok3, "POST", "/api/rooms/join", map[string]string{"code": roomCode(t, h, tok2, rf.ID)}, nil); s != http.StatusOK {
		t.Fatalf("colleague joins: %d", s)
	}
	h.APIAs(tok3, "POST", "/api/rooms/"+rf.ID+"/messages", map[string]any{"text": "friend room post", "ask": false}, nil)
	pendingRoomCode := roomCode(t, h, tok2, rf.ID)
	var ownerCode struct{ Code string }
	if s := h.APIAs(tok2, "POST", "/api/owners/invite", nil, &ownerCode); s != http.StatusOK {
		t.Fatalf("admin invite: %d", s)
	}
	if s := h.APIAs(tok2, "POST", "/api/devices", map[string]string{"token": strings.Repeat("ab", 32), "platform": "ios", "env": "sandbox"}, nil); s != http.StatusNoContent && s != http.StatusOK {
		t.Fatalf("device: %d", s)
	}
	shareRoom(t, h, r1.ID, tok2)
	if s := h.APIAs(tok2, "POST", "/api/rooms/"+r1.ID+"/messages", map[string]any{"text": "kept post", "ask": false}, nil); s != http.StatusCreated {
		t.Fatalf("room message: %d", s)
	}
	h.API("PATCH", "/api/rooms/"+r1.ID+"/members/"+friendID, map[string]string{"role": "agent"}, nil)
	kept := uploadAs(t, h, tok2, "kept.txt", "kept attachment")
	var asked protocol.Task
	if s := h.APIAs(tok2, "POST", "/api/rooms/"+r1.ID+"/messages", map[string]any{"text": "tidy up", "ask": true, "attachments": []string{kept.ID}}, &asked); s != http.StatusCreated {
		t.Fatalf("ask: %d", s)
	}
	for _, f := range []string{ownFile.ID, loose.ID, kept.ID} {
		if _, err := os.Stat(filepath.Join(upDir, f)); err != nil {
			t.Fatalf("file before: %v", err)
		}
	}
	before := map[string]bool{}
	for _, hit := range rowsContaining(t, dbPath, friendID, fa.Agent.ID, rf.ID, ownTask.ID) {
		before[strings.SplitN(hit, ":", 2)[0]] = true
	}
	for _, tb := range []string{"owners", "owner_tokens", "agents", "tasks", "events", "rooms", "room_members", "room_messages", "room_routines", "ui_screens", "ui_layouts", "ui_data", "agent_routines", "routine_history", "devices", "uploads"} {
		if !before[tb] {
			t.Fatalf("setup has no %s row to prove deletion: %v", tb, before)
		}
	}

	// the owner account is not deleted from the app
	var rej struct{ Error string }
	if s := h.API("DELETE", "/api/me", nil, &rej); s != http.StatusForbidden || !strings.Contains(rej.Error, "uninstall --purge") {
		t.Fatalf("owner delete: %d %q", s, rej.Error)
	}

	clientConn := dialWS(t, h, "/client", tok2)
	agentConn := dialWS(t, h, "/agent", fa.Token) // stands in for the fake agent connection

	watch := h.ConnectClient() // connect fresh so earlier room notices do not mix in

	// twice at once
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func() { defer wg.Done(); codes[i] = h.APIAs(tok2, "DELETE", "/api/me", nil, nil) }()
	}
	wg.Wait()
	slices.Sort(codes)
	if codes[0] != http.StatusNoContent || (codes[1] != http.StatusUnauthorized && codes[1] != http.StatusNotFound) {
		t.Fatalf("double delete: %v", codes)
	}

	// the connection drops and the token dies
	if c := closeCode(t, clientConn); c != 4003 {
		t.Fatalf("client close %d", c)
	}
	if c := closeCode(t, agentConn); c != 4003 {
		t.Fatalf("agent close %d", c)
	}
	if s := h.APIAs(tok2, "GET", "/api/agents", nil, nil); s != http.StatusUnauthorized {
		t.Fatalf("token after delete: %d", s)
	}
	if _, err := fakeagent.Dial(context.Background(), h.URL, fa.Token); err == nil {
		t.Fatal("agent token still connects")
	}

	// members of that person's room lose the room, and codes they issued die
	h.WaitFor(colleague, "room.removed", func(m protocol.ClientMessage) bool { return m.Type == "room.removed" && m.Room.ID == rf.ID })
	if s := h.APIAs(tok3, "POST", "/api/rooms/join", map[string]string{"code": pendingRoomCode}, nil); s != http.StatusUnauthorized {
		t.Fatalf("room code after delete: %d", s)
	}
	redeem(t, h, ownerCode.Code, "pickedcode", http.StatusUnauthorized)
	h.WaitFor(watch, "R1 members refreshed", func(m protocol.ClientMessage) bool { return m.Type == "room" && m.Room.ID == r1.ID })

	// nowhere in the DB are that person's id, name, agents, tasks, rooms, or attachments
	if hits := rowsContaining(t, dbPath, friendID, name, fa.Agent.ID, ownTask.ID, rf.ID, ownFile.ID, loose.ID); len(hits) > 0 {
		t.Fatalf("rows left:\n%s", strings.Join(hits, "\n"))
	}
	for _, f := range []string{ownFile.ID, loose.ID} {
		if _, err := os.Stat(filepath.Join(upDir, f)); !os.IsNotExist(err) {
			t.Fatalf("file %s still on disk: %v", f, err)
		}
	}

	// posts in others' rooms remain, and the author becomes "deleted user"
	var msgs []protocol.RoomMessage
	h.API("GET", "/api/rooms/"+r1.ID+"/messages", nil, &msgs)
	if len(msgs) != 1 || msgs[0].Text != "kept post" || msgs[0].Name != "탈퇴한 사용자" || msgs[0].OwnerID == friendID {
		t.Fatalf("kept message: %+v", msgs)
	}
	var tasks []protocol.Task
	h.API("GET", "/api/tasks", nil, &tasks)
	i := slices.IndexFunc(tasks, func(x protocol.Task) bool { return x.ID == asked.ID })
	if i < 0 || tasks[i].AskedBy == "" || tasks[i].AskedBy == friendID {
		t.Fatalf("asked task: %+v", tasks)
	}
	var events []protocol.Event
	h.API("GET", "/api/tasks/"+asked.ID+"/events", nil, &events)
	if len(events) == 0 || !bytes.Contains(events[0].Payload, []byte(`"byName":"탈퇴한 사용자"`)) {
		t.Fatalf("kept event: %s", events[0].Payload)
	}
	var p struct{ Attachments []protocol.Attachment }
	json.Unmarshal(events[0].Payload, &p)
	if len(p.Attachments) != 1 || p.Attachments[0].ID != kept.ID {
		t.Fatalf("kept attachment: %s", events[0].Payload)
	}
	res, err := http.Get(h.URL + p.Attachments[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("kept file: %d", res.StatusCode)
	}

	// rejoining makes a new person, not linked to the old posts
	tok4 := invite(t, h, name)
	var again struct{ OwnerID string }
	h.APIAs(tok4, "GET", "/api/me", nil, &again)
	if again.OwnerID == "" || again.OwnerID == friendID {
		t.Fatalf("new id: %q", again.OwnerID)
	}
	var list []protocol.Agent
	if h.APIAs(tok4, "GET", "/api/agents", nil, &list); len(list) != 0 {
		t.Fatalf("old agents came back: %+v", list)
	}
	if s := h.APIAs(tok4, "GET", "/api/rooms/"+r1.ID+"/messages", nil, nil); s != http.StatusNotFound {
		t.Fatalf("old room visible: %d", s)
	}
	h.API("GET", "/api/rooms/"+r1.ID+"/messages", nil, &msgs)
	if msgs[0].Name != "탈퇴한 사용자" || msgs[0].OwnerID == again.OwnerID {
		t.Fatalf("old message joined new account: %+v", msgs[0])
	}
}

// Even if two people delete, there is only one "deleted user" and nobody can join under that name.
func TestDeletedAuthorIsInert(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	r1 := makeRoom(t, h, "Contest", a.Agent.ID)
	for _, n := range []string{"one", "two"} {
		tok := invite(t, h, n)
		shareRoom(t, h, r1.ID, tok)
		h.APIAs(tok, "POST", "/api/rooms/"+r1.ID+"/messages", map[string]any{"text": n + " post", "ask": false}, nil)
		if s := h.APIAs(tok, "DELETE", "/api/me", nil, nil); s != http.StatusNoContent {
			t.Fatalf("delete %s: %d", n, s)
		}
	}
	var msgs []protocol.RoomMessage
	h.API("GET", "/api/rooms/"+r1.ID+"/messages", nil, &msgs)
	if len(msgs) != 2 || msgs[0].OwnerID != msgs[1].OwnerID || msgs[1].Name != "탈퇴한 사용자" {
		t.Fatalf("messages: %+v", msgs)
	}
	gone := msgs[0].OwnerID
	var users []map[string]any
	if h.API("GET", "/api/owners", nil, &users); len(users) != 0 {
		t.Fatalf("users: %v", users)
	}
	if s := h.API("DELETE", "/api/owners/"+gone, nil, nil); s != http.StatusNotFound {
		t.Fatalf("revoke gone: %d", s)
	}
	if s := h.API("PATCH", "/api/owners/"+gone, map[string]bool{"admin": true}, nil); s != http.StatusNotFound {
		t.Fatalf("admin gone: %d", s)
	}
	var members []map[string]any
	if h.API("GET", "/api/rooms/"+r1.ID+"/members", nil, &members); len(members) != 0 {
		t.Fatalf("members: %v", members)
	}
}
