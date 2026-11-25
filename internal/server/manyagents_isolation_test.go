package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

type taskTruth struct{ agent, owner, room, routine string }

func upload(t *testing.T, h *decktest.Harness, token, name string) string {
	t.Helper()
	req, _ := http.NewRequest("POST", h.URL+"/api/uploads?name="+name, bytes.NewReader([]byte("file "+name)))
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
	return a.ID
}

func getStatus(t *testing.T, h *decktest.Harness, token, path string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", h.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

// Three people (owner + 2 invitees) each attach several agents, and everyone at once pours out tasks, approvals, follow-ups,
// self-opened tasks, routine results, ui.put, and data.put. Then everything the screens and agents received is checked exhaustively.
// Every agent uses the same screen id, bundle name, and routine id — they must be separated by agent, not by name.
func TestManyAgentsStayIsolated(t *testing.T) {
	h := decktest.New(t)
	owners := newRigOwners(t, h, 3, 3)
	for _, o := range owners {
		o.connect(t, h)
	}
	for _, o := range owners {
		for _, a := range o.agents {
			a.start(t, h)
		}
	}
	allIdle(t, owners)

	// share the owner's first agent's room with the second person in the agent role
	host, guest := owners[0], owners[1]
	shared := host.agents[0]
	room := makeRoom(t, h, "Shared room", shared.id, "r")
	guestID := shareRoom(t, h, room.ID, guest.token)
	h.API("PATCH", "/api/rooms/"+room.ID+"/members/"+guestID, map[string]string{"role": "agent"}, nil)

	uploads := map[string]string{} // an attachment attaches to one post only, so upload separately per agent
	for _, o := range owners {
		for _, a := range o.agents {
			uploads[a.id] = upload(t, h, o.token, o.name+".txt")
		}
	}

	var wg sync.WaitGroup
	for _, o := range owners {
		for _, a := range o.agents {
			wg.Add(2)
			go func() { // person side
				defer wg.Done()
				var task protocol.Task
				if s := h.APIAs(o.token, "POST", "/api/tasks", map[string]any{"agentId": a.id, "text": "to:" + a.id + " first task", "attachments": []string{uploads[a.id]}}, &task); s != http.StatusCreated {
					t.Errorf("start %s: %d", a.id, s)
					return
				}
				o.client.waitFor(t, task.ID+" waiting", decktest.TaskState(task.ID, protocol.TaskWaiting))
				if s := h.APIAs(o.token, "POST", "/api/tasks/"+task.ID+"/approvals/ap", map[string]string{"choice": "Approve"}, nil); s != http.StatusNoContent {
					t.Errorf("approve %s: %d", task.ID, s)
				}
				o.client.waitFor(t, task.ID+" done", decktest.TaskState(task.ID, protocol.TaskDone))
				if s := h.APIAs(o.token, "POST", "/api/tasks/"+task.ID+"/messages", map[string]string{"text": "to:" + a.id + " continuing"}, nil); s != http.StatusAccepted && s != http.StatusOK && s != http.StatusNoContent {
					t.Errorf("follow up %s: %d", task.ID, s)
				}
			}()
			go func() { // agent side
				defer wg.Done()
				ctx := context.Background()
				from := "from:" + a.id
				own := a.id + "-own"
				a.open(own)
				for _, m := range []protocol.AgentMessage{
					{Type: "task.open", Task: own, Title: "self-opened task"},
					{Type: "message", Task: own, Text: from + " done alone"},
					{Type: "task.title", Task: own, Title: "Title " + a.id[:6]},
					{Type: "task.end", Task: own, Result: protocol.TaskDone},
					{Type: "routine.run", Routine: "r", RoutineName: "same name routine", Run: "run1", Text: from + " routine result"},
				} {
					a.fa.Send(ctx, m)
				}
				a.fa.Send(ctx, map[string]any{"type": "ui.put", "req": "s", "screen": json.RawMessage(`{"version":1,"id":"mine","title":"t","icon":"star","blocks":[{"type":"stat","label":"` + a.id + `","value":1}]}`)})
				a.fa.Send(ctx, map[string]any{"type": "data.put", "req": "d", "name": "b", "data": map[string]string{"agent": a.id}})
			}()
		}
	}
	// shared room: the guest asks, and the owner writes a human post
	var roomTask protocol.Task
	if s := h.APIAs(guest.token, "POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "to:" + shared.id + " room question", "ask": true}, &roomTask); s != http.StatusCreated {
		t.Fatalf("guest asks room: %d", s)
	}
	h.API("POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "human post", "ask": false}, nil)
	wg.Wait()

	// receive every agent's ui.put and data.put replies, until the continued tasks finish
	for _, o := range owners {
		for _, a := range o.agents {
			for _, req := range []string{"s", "d"} {
				if m := waitAck(t, a, req); m.Reason != "" {
					t.Errorf("agent %s %s rejected: %s", a.id, req, m.Reason)
				}
			}
			a.fa.Send(context.Background(), map[string]any{"type": "ui.list", "req": "l"})
			waitAck(t, a, "")
		}
	}
	guest.client.waitFor(t, "room task done", decktest.TaskState(roomTask.ID, protocol.TaskDone))

	// truth table: each person's own tasks as seen over REST
	truth := map[string]taskTruth{}
	for _, o := range owners {
		for _, a := range o.agents {
			var list []protocol.Task
			h.APIAs(o.token, "GET", "/api/tasks?agent="+a.id, nil, &list)
			for _, tk := range list {
				if tk.AgentID != a.id {
					t.Errorf("%s task list for %s has task of %s", o.name, a.id, tk.AgentID)
				}
				truth[tk.ID] = taskTruth{a.id, o.id, tk.RoomID, tk.RoutineID}
			}
			// 2 person-started (the first task is reopened by the follow-up, so it is the same task) + self-opened + routine result
			if len(list) < 3 {
				t.Errorf("%s agent %s: %d tasks, want ≥3", o.name, a.id, len(list))
			}
			for _, m := range []string{"/ui"} {
				var st struct {
					Screens []struct {
						ID   string          `json:"id"`
						Spec json.RawMessage `json:"spec"`
					} `json:"screens"`
					Data map[string]struct {
						Value json.RawMessage `json:"value"`
					} `json:"data"`
				}
				h.APIAs(o.token, "GET", "/api/agents/"+a.id+m, nil, &st)
				for _, s := range st.Screens {
					if s.ID == "mine" && !bytes.Contains(s.Spec, []byte(a.id)) {
						t.Errorf("agent %s screen mine holds %s", a.id, s.Spec)
					}
				}
				if v := st.Data["b"].Value; !bytes.Contains(v, []byte(a.id)) {
					t.Errorf("agent %s bundle b = %s", a.id, v)
				}
			}
		}
	}

	// exhaustive screen check
	agentOwner := map[string]*rigOwner{}
	for _, o := range owners {
		for _, a := range o.agents {
			agentOwner[a.id] = o
		}
	}
	counts := map[string]int{}
	for _, o := range owners {
		mine := func(agent string) bool { return agentOwner[agent] == o }
		viaRoom := func(tr taskTruth) bool {
			return o == guest && tr.agent == shared.id && (tr.room == room.ID || tr.routine == "r")
		}
		checkTask := func(where, taskID string, payload []byte) {
			tr, ok := truth[taskID]
			if !ok {
				t.Errorf("%s got %s for unknown task %s", o.name, where, taskID)
				return
			}
			if !mine(tr.agent) && !viaRoom(tr) {
				t.Errorf("LEAK %s got %s of task %s (agent %s, owner %s)", o.name, where, taskID, tr.agent, tr.owner)
			}
			if i := bytes.Index(payload, []byte("from:")); i >= 0 {
				if !bytes.HasPrefix(payload[i+5:], []byte(tr.agent)) {
					t.Errorf("MIX %s %s of task %s (agent %s) carries %s", o.name, where, taskID, tr.agent, payload[i:min(len(payload), i+50)])
				}
			}
		}
		for _, m := range o.client.snapshot() {
			counts[o.name]++
			switch m.Type {
			case "agent":
				if !mine(m.Agent.ID) {
					t.Errorf("LEAK %s got agent %s", o.name, m.Agent.ID)
				}
			case "task":
				if truth[m.Task.ID].agent != m.Task.AgentID && truth[m.Task.ID].agent != "" {
					t.Errorf("MIX task %s says agent %s, truth %s", m.Task.ID, m.Task.AgentID, truth[m.Task.ID].agent)
				}
				checkTask("task", m.Task.ID, nil)
			case "event":
				checkTask("event "+m.Event.Type, m.Event.TaskID, m.Event.Payload)
			case "draft":
				checkTask("draft", m.TaskID, []byte(m.Text))
			case "report", "ui":
				if !mine(m.AgentID) {
					t.Errorf("LEAK %s got %s of %s", o.name, m.Type, m.AgentID)
				}
			case "room":
				if !mine(m.Room.AgentID) && !(o == guest && m.Room.ID == room.ID) {
					t.Errorf("LEAK %s got room %s of agent %s", o.name, m.Room.ID, m.Room.AgentID)
				}
			case "room.message":
				if m.RoomMessage.RoomID != room.ID || (o != host && o != guest) {
					t.Errorf("LEAK %s got room.message %+v", o.name, m.RoomMessage)
				}
			default:
				t.Errorf("%s got unexpected type %s", o.name, m.Type)
			}
		}
	}

	// exhaustive agent check: every task and post received is its own
	for _, o := range owners {
		for _, a := range o.agents {
			for _, m := range a.snapshot() {
				switch m.Type {
				case "task.start", "user_message", "approval_result", "cancel":
					if tr, ok := truth[m.Task]; !ok || tr.agent != a.id || !a.knows(m.Task) {
						t.Errorf("LEAK agent %s got %s for task %s (truth %+v)", a.id, m.Type, m.Task, tr)
					}
					if m.Type != "approval_result" && !strings.Contains(m.Text, "to:"+a.id) {
						t.Errorf("MIX agent %s got %s text %q", a.id, m.Type, m.Text)
					}
				case "ui.ack":
				case "ui.state":
					var st struct {
						Data map[string]struct {
							Value json.RawMessage `json:"value"`
						} `json:"data"`
					}
					json.Unmarshal(m.UI, &st)
					for name, d := range st.Data {
						if name == "b" && !bytes.Contains(d.Value, []byte(a.id)) {
							t.Errorf("MIX agent %s ui.state bundle b = %s", a.id, d.Value)
						}
					}
				default:
					t.Errorf("agent %s got unexpected %s %+v", a.id, m.Type, m)
				}
			}
		}
	}
	t.Logf("client messages checked: %v, tasks %d", counts, len(truth))
}

func waitAck(t *testing.T, a *rigAgent, req string) protocol.ServerMessage {
	t.Helper()
	pred := func(m protocol.ServerMessage) bool {
		if req == "" {
			return m.Type == "ui.state"
		}
		return m.Type == "ui.ack" && m.Req == req
	}
	for range 1500 {
		for _, m := range a.snapshot() {
			if pred(m) {
				return m
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("agent %s: no ack %q", a.id, req)
	return protocol.ServerMessage{}
}

// Call someone else's things directly by id. Person token, someone else's agent token, and websocket agent messages, all of them.
func TestManyAgentsCrossAccessRejected(t *testing.T) {
	h := decktest.New(t)
	owners := newRigOwners(t, h, 3, 2)
	for _, o := range owners {
		o.connect(t, h)
		for _, a := range o.agents {
			a.start(t, h)
		}
	}
	allIdle(t, owners)

	type victim struct {
		o           *rigOwner
		a           *rigAgent
		task, file  string
		room        string
		eventsCount int
	}
	victims := map[*rigOwner]*victim{}
	for _, o := range owners {
		a := o.agents[0]
		v := &victim{o: o, a: a, file: upload(t, h, o.token, "secret.txt")}
		var task protocol.Task
		h.APIAs(o.token, "POST", "/api/tasks", map[string]any{"agentId": a.id, "text": "to:" + a.id + " secret"}, &task)
		v.task = task.ID
		o.client.waitFor(t, "victim waiting", decktest.TaskState(task.ID, protocol.TaskWaiting))
		a.fa.Send(context.Background(), map[string]any{"type": "data.put", "req": "d", "name": "b", "data": map[string]string{"agent": a.id}})
		waitAck(t, a, "d")
		var r protocol.Room
		h.APIAs(o.token, "POST", "/api/rooms", map[string]any{"name": "Room " + o.name, "agentId": a.id}, &r)
		v.room = r.ID
		victims[o] = v
	}
	events := func(v *victim) int {
		var ev []protocol.Event
		h.APIAs(v.o.token, "GET", "/api/tasks/"+v.task+"/events", nil, &ev)
		return len(ev)
	}
	for _, v := range victims {
		v.eventsCount = events(v)
	}

	reqs := func(v *victim, mine string) [][3]string {
		a, tk := "/api/agents/"+v.a.id, "/api/tasks/"+v.task
		return [][3]string{
			{"GET", a + "/ui", ""}, {"GET", a + "/report", ""}, {"GET", a + "/routines", ""},
			{"POST", a + "/ui/screens", `{"version":1,"id":"x","title":"t","icon":"i","blocks":[{"type":"divider"}]}`},
			{"DELETE", a + "/ui/data/b", ""}, {"PUT", a + "/ui/tabs", `{"tabs":[]}`}, {"PUT", a + "/ui/hidden", `{"hidden":[]}`}, {"PUT", a + "/ui/order", `{"order":[]}`},
			{"PUT", a + "/avatar", `{"avatar":"preset:cat"}`}, {"POST", a + "/token", ""}, {"DELETE", a, ""},
			{"POST", a + "/routines", `{"name":"n","schedule":"every 2h","prompt":"p"}`}, {"POST", a + "/routines/r/run", ""},
			{"POST", a + "/data/b/refresh", ""}, {"POST", a + "/pair", ""},
			{"GET", tk + "/events", ""}, {"POST", tk + "/messages", `{"text":"butting in"}`}, {"POST", tk + "/approvals/ap", `{"choice":"Approve"}`},
			{"POST", tk + "/cancel", ""}, {"POST", tk + "/reactions", `{"eventId":"1","emoji":"👍"}`}, {"PATCH", tk, `{"title":"x"}`}, {"DELETE", tk, ""},
			{"POST", "/api/tasks", `{"agentId":"` + v.a.id + `","text":"someone else's agent"}`},
			{"GET", "/api/rooms/" + v.room + "/posts", ""}, {"POST", "/api/rooms/" + v.room + "/messages", `{"text":"x"}`},
			{"PATCH", "/api/rooms/" + v.room, `{"name":"x"}`}, {"DELETE", "/api/rooms/" + v.room, ""},
			{"POST", "/api/tasks", `{"agentId":"` + mine + `","text":"x","attachments":["` + v.file + `"]}`}, // someone else's attachment on my agent
		}
	}
	do := func(token, method, path, body string) int {
		req, _ := http.NewRequest(method, h.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	checked := 0
	for _, attacker := range owners {
		for _, v := range victims {
			if v.o == attacker {
				continue
			}
			for _, r := range reqs(v, attacker.agents[0].id) {
				for _, tok := range []string{attacker.token, attacker.agents[1].token} {
					checked++
					s := do(tok, r[0], r[1], r[2])
					if strings.Contains(r[2], "attachments") && tok == attacker.token && s == http.StatusBadRequest {
						continue // unknown attachment
					}
					if s != http.StatusNotFound && s != http.StatusForbidden && s != http.StatusUnauthorized {
						t.Errorf("%s (%s) %s %s → %d", attacker.name, map[bool]string{true: "person", false: "agent"}[tok == attacker.token], r[0], r[1], s)
					}
				}
			}
			// shared attachment: without a signature, with someone else's token
			for _, tok := range []string{attacker.token, attacker.agents[0].token} {
				checked++
				if s := getStatus(t, h, tok, "/api/uploads/"+v.file); s != http.StatusNotFound {
					t.Errorf("%s reads %s's upload: %d", attacker.name, v.o.name, s)
				}
			}
			// websocket: my agent touches someone else's tasks and rooms
			ag := attacker.agents[1]
			ctx := context.Background()
			ok := true
			for _, m := range []protocol.AgentMessage{
				{Type: "message", Task: v.task, Text: "from:" + ag.id + " butting in"},
				{Type: "tool", Task: v.task, Call: "x", Phase: "started", Name: "x"},
				{Type: "tool", Task: v.task, Call: "x", Phase: "completed", Name: "x", OK: &ok},
				{Type: "approval", Task: v.task, ID: "evil", Question: "q", Options: []string{"a", "b"}},
				{Type: "task.title", Task: v.task, Title: "title changed by another"},
				{Type: "draft", Task: v.task, Text: "from:" + ag.id},
				{Type: "reaction", Task: v.task, Emoji: "👎"},
				{Type: "task.end", Task: v.task, Result: protocol.TaskFailed},
			} {
				ag.fa.Send(ctx, m)
			}
			ag.fa.Send(ctx, protocol.AgentMessage{Type: "task.open", Task: v.task, Title: "hijack"})
			ag.fa.Send(ctx, protocol.AgentMessage{Type: "room.rename", ID: v.room, Name: "room changed by another"})
			ag.fa.Send(ctx, map[string]any{"type": "data.remove", "req": "rm-" + v.o.name, "name": "b"})
			ag.waitError(t, "unknown room "+v.room)
			ag.waitError(t, "task id already exists")
			waitAck(t, ag, "rm-"+v.o.name)
		}
	}

	for _, v := range victims {
		if n := events(v); n != v.eventsCount {
			t.Errorf("%s task events changed %d → %d", v.o.name, v.eventsCount, n)
		}
		var list []protocol.Task
		h.APIAs(v.o.token, "GET", "/api/tasks?agent="+v.a.id, nil, &list)
		for _, tk := range list {
			if tk.ID == v.task && (tk.State != protocol.TaskWaiting || strings.Contains(tk.Title, "another")) {
				t.Errorf("%s task touched: %+v", v.o.name, tk)
			}
		}
		var rooms []protocol.Room
		h.APIAs(v.o.token, "GET", "/api/rooms", nil, &rooms)
		for _, r := range rooms {
			if r.ID == v.room && r.Name != "Room "+v.o.name {
				t.Errorf("room renamed by other: %+v", r)
			}
		}
		var st struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		h.APIAs(v.o.token, "GET", "/api/agents/"+v.a.id+"/ui", nil, &st)
		if _, ok := st.Data["b"]; !ok {
			t.Errorf("%s bundle b removed by other agent", v.o.name)
		}
		if s := getStatus(t, h, v.o.token, "/api/agents/"+v.a.id+"/report"); s != http.StatusOK {
			t.Errorf("%s agent gone after attacks: %d", v.o.name, s)
		}
	}
	// even another agent of the same owner cannot fetch an attachment not attached to the task
	o := owners[0]
	if s := getStatus(t, h, o.agents[1].token, "/api/uploads/"+victims[o].file); s != http.StatusNotFound {
		t.Errorf("same owner, other agent reads upload: %d", s)
	}
	t.Logf("cross requests checked: %d", checked)
}
