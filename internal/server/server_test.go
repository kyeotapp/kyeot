package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/fakeagent"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

func TestRejectsWrongOwnerToken(t *testing.T) {
	h := decktest.New(t)
	if s := h.APIAs("wrong-token", "GET", "/api/agents", nil, nil); s != http.StatusUnauthorized {
		t.Fatalf("api: got %d", s)
	}
	_, res, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(h.URL, "http")+"/client?token=wrong", nil)
	if err == nil || res == nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("client ws: err=%v res=%v", err, res)
	}
}

func TestQueryTokenOnlyForWebSocket(t *testing.T) {
	h := decktest.New(t)
	res, err := http.Get(h.URL + "/api/agents?token=" + decktest.OwnerToken)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("REST accepted the token in the URL: %d", res.StatusCode)
	}
	req, _ := http.NewRequest("GET", h.URL+"/api/agents?token="+decktest.OwnerToken, nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("REST with an Upgrade header accepted the token in the URL: %d", res.StatusCode)
	}
	if s := h.API("GET", "/api/agents", nil, nil); s != http.StatusOK {
		t.Fatalf("header token: %d", s)
	}
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(h.URL, "http")+"/client?token="+decktest.OwnerToken, nil)
	if err != nil {
		t.Fatalf("websocket must accept the token in the URL: %v", err)
	}
	conn.CloseNow()
}

// The phone app sends the token to /client in a header, not in the URL.
func TestClientWebSocketHeaderToken(t *testing.T) {
	h := decktest.New(t)
	ws := "ws" + strings.TrimPrefix(h.URL, "http") + "/client"
	conn, _, err := websocket.Dial(context.Background(), ws, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + decktest.OwnerToken}}})
	if err != nil {
		t.Fatalf("must connect with a header token: %v", err)
	}
	conn.CloseNow()
	_, res, err := websocket.Dial(context.Background(), ws, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer wrong"}}})
	if err == nil || res == nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong header token: err=%v res=%v", err, res)
	}
}

func TestRejectsUnknownAgentToken(t *testing.T) {
	h := decktest.New(t)
	if _, err := fakeagent.Dial(context.Background(), h.URL, "not-a-real-token"); err == nil {
		t.Fatal("expected dial to fail")
	}
}

func TestFullTaskFlow(t *testing.T) {
	h := decktest.New(t)
	agentA := h.CreateAgent("Agent A")
	if agentA.Token == "" {
		t.Fatal("token missing")
	}
	var listed []map[string]any
	h.API("GET", "/api/agents", nil, &listed)
	if raw, _ := json.Marshal(listed); bytes.Contains(raw, []byte(agentA.Token)) {
		t.Fatal("agent list leaks token")
	}

	c := h.ConnectClient()
	agent := h.StartFakeAgent(agentA.Token)
	h.WaitFor(c, "agent idle", decktest.AgentState(agentA.Agent.ID, "idle"))

	var task protocol.Task
	if s := h.API("POST", "/api/tasks", map[string]string{"agentId": agentA.Agent.ID, "text": "Book dinner tomorrow evening"}, &task); s != http.StatusCreated {
		t.Fatalf("start task: %d", s)
	}
	h.WaitFor(c, "task waiting", decktest.TaskState(task.ID, "waiting"))

	var pending []protocol.Event
	h.API("GET", "/api/approvals", nil, &pending)
	if len(pending) != 1 {
		t.Fatalf("pending approvals: %d", len(pending))
	}

	approve := "/api/tasks/" + task.ID + "/approvals/a1"
	if s := h.API("POST", approve, map[string]string{"choice": "anything"}, nil); s != http.StatusBadRequest {
		t.Fatalf("invalid choice: %d", s)
	}
	if s := h.API("POST", approve, map[string]string{"choice": "승인"}, nil); s != http.StatusNoContent {
		t.Fatalf("approve: %d", s)
	}
	if s := h.API("POST", approve, map[string]string{"choice": "승인"}, nil); s != http.StatusNotFound {
		t.Fatalf("double approve: %d", s)
	}
	h.WaitFor(c, "task done", decktest.TaskState(task.ID, "done"))

	var events []protocol.Event
	h.API("GET", "/api/tasks/"+task.ID+"/events", nil, &events)
	var types []string
	for _, e := range events {
		types = append(types, e.Type)
	}
	want := "task.start tool tool approval approval_result message task.end"
	if got := strings.Join(types, " "); got != want {
		t.Fatalf("events:\n got  %s\n want %s", got, want)
	}
	h.API("GET", "/api/approvals", nil, &pending)
	if len(pending) != 0 {
		t.Fatalf("pending after approve: %d", len(pending))
	}

	agent.Close()
	h.WaitFor(c, "agent offline", decktest.AgentState(agentA.Agent.ID, "offline"))
	if s := h.API("POST", "/api/tasks", map[string]string{"agentId": agentA.Agent.ID, "text": "again"}, nil); s != http.StatusConflict {
		t.Fatalf("offline start: %d", s)
	}
}

func TestAgentCannotWriteToOthersTask(t *testing.T) {
	h := decktest.New(t)
	a, b := h.CreateAgent("A"), h.CreateAgent("B")
	c := h.ConnectClient()
	h.StartFakeAgent(a.Token)
	agentB := h.StartFakeAgent(b.Token)
	h.WaitAll(c, "A, B idle", decktest.AgentState(a.Agent.ID, "idle"), decktest.AgentState(b.Agent.ID, "idle"))

	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "A's work"}, &task)
	agentB.Send(context.Background(), protocol.AgentMessage{Type: "message", Task: task.ID, Text: "intrusion"})

	select {
	case reason := <-agentB.Errors:
		if reason != "unknown task "+task.ID {
			t.Fatalf("reason: %s", reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("B got no error")
	}
	var events []protocol.Event
	h.API("GET", "/api/tasks/"+task.ID+"/events", nil, &events)
	for _, e := range events {
		if bytes.Contains(e.Payload, []byte("intrusion")) {
			t.Fatal("B's message was stored on A's task")
		}
	}
}

func TestCancelledTaskStaysCancelled(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("C")
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))

	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "Task to cancel"}, &task)
	h.WaitFor(c, "waiting", decktest.TaskState(task.ID, "waiting"))
	if s := h.API("POST", "/api/tasks/"+task.ID+"/cancel", nil, nil); s != http.StatusNoContent {
		t.Fatalf("cancel: %d", s)
	}
	agent.Send(context.Background(), protocol.AgentMessage{Type: "task.end", Task: task.ID, Result: "done"})
	h.WaitFor(c, "late task.end recorded", func(m protocol.ClientMessage) bool {
		return m.Type == "event" && m.Event.TaskID == task.ID && m.Event.Type == "task.end"
	})

	var tasks []protocol.Task
	h.API("GET", "/api/tasks?agent="+a.Agent.ID, nil, &tasks)
	if tasks[0].State != "cancelled" {
		t.Fatalf("state: %s", tasks[0].State)
	}
}

func TestReconnectKeepsAgentOnline(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("D")
	c := h.ConnectClient()
	h.StartFakeAgent(a.Token)
	h.WaitFor(c, "first idle", decktest.AgentState(a.Agent.ID, "idle"))

	h.StartFakeAgent(a.Token)
	h.WaitFor(c, "second idle", decktest.AgentState(a.Agent.ID, "idle"))
	time.Sleep(200 * time.Millisecond)

	var agents []protocol.Agent
	h.API("GET", "/api/agents", nil, &agents)
	if agents[0].State != "idle" {
		t.Fatalf("after reconnect: %s", agents[0].State)
	}
	if s := h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "Task after reconnect"}, nil); s != http.StatusCreated {
		t.Fatalf("start after reconnect: %d", s)
	}
}

func TestMessageReopensFinishedTask(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("E")
	c := h.ConnectClient()
	h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))

	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "First question"}, &task)
	h.WaitFor(c, "waiting", decktest.TaskState(task.ID, "waiting"))
	h.API("POST", "/api/tasks/"+task.ID+"/approvals/a1", map[string]string{"choice": "승인"}, nil)
	h.WaitFor(c, "done", decktest.TaskState(task.ID, "done"))

	if s := h.API("POST", "/api/tasks/"+task.ID+"/messages", map[string]string{"text": "이어서"}, nil); s != http.StatusNoContent {
		t.Fatalf("message on done task: %d", s)
	}
	h.WaitFor(c, "reopened", decktest.TaskState(task.ID, "running"))
	h.WaitFor(c, "agent reply", func(m protocol.ClientMessage) bool {
		return m.Type == "event" && m.Event.TaskID == task.ID && bytes.Contains(m.Event.Payload, []byte("들었습니다: 이어서"))
	})
}

func TestBrowserOrigins(t *testing.T) {
	h := decktest.New(t)
	preflight := func(origin string) string {
		req, _ := http.NewRequest(http.MethodOptions, h.URL+"/api/agents", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.Header.Get("Access-Control-Allow-Origin")
	}
	if got := preflight(decktest.AllowedOrigin); got != decktest.AllowedOrigin {
		t.Fatalf("allowed preflight: %q", got)
	}
	if got := preflight("http://evil.example"); got != "" {
		t.Fatalf("foreign preflight: %q", got)
	}

	dial := func(origin string) (*http.Response, error) {
		_, res, err := websocket.Dial(context.Background(),
			"ws"+strings.TrimPrefix(h.URL, "http")+"/client?token="+decktest.OwnerToken,
			&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {origin}}})
		return res, err
	}
	if _, err := dial(decktest.AllowedOrigin); err != nil {
		t.Fatalf("allowed ws: %v", err)
	}
	if res, err := dial("http://evil.example"); err == nil || res.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign ws should be 403: %v", err)
	}
}

func TestRoutineRunsFeedRoomsOnce(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))

	var room protocol.Room
	if s := h.API("POST", "/api/rooms", map[string]string{"name": "Contest", "agentId": a.Agent.ID, "routineId": "job1"}, &room); s != http.StatusCreated {
		t.Fatalf("create room: %d", s)
	}
	h.WaitFor(c, "room broadcast", func(m protocol.ClientMessage) bool { return m.Type == "room" && m.Room.ID == room.ID })

	run := protocol.AgentMessage{Type: "routine.run", Routine: "job1", RoutineName: "Contest-alert", Run: "2026-11-09_09-06-51", Text: "**This week's contest**", At: 1762646811000}
	agent.Send(context.Background(), run)
	agent.Send(context.Background(), run)
	agent.Send(context.Background(), protocol.AgentMessage{Type: "routine.run", Routine: "other", Run: "x", Text: "Other routine"})
	h.WaitFor(c, "other routine task", func(m protocol.ClientMessage) bool { return m.Type == "task" && m.Task.RoutineID == "other" })

	var posts []struct {
		Task    protocol.Task `json:"task"`
		Preview string        `json:"preview"`
	}
	h.API("GET", "/api/rooms/"+room.ID+"/posts", nil, &posts)
	if len(posts) != 1 || posts[0].Preview != "**This week's contest**" || posts[0].Task.State != "done" {
		t.Fatalf("posts: %+v", posts)
	}
	if posts[0].Task.CreatedAt != 1762646811000 || !strings.HasPrefix(posts[0].Task.Title, "Contest-alert · ") {
		t.Fatalf("time/title: %+v", posts[0].Task)
	}

	taskID := posts[0].Task.ID
	h.API("POST", "/api/tasks/"+taskID+"/messages", map[string]string{"text": "Tell me more about the first one"}, nil)
	if got := <-agent.Contexts; got != "**This week's contest**" {
		t.Fatalf("first reply context: %q", got)
	}
	h.WaitFor(c, "reply done", decktest.TaskState(taskID, "done"))
	h.API("POST", "/api/tasks/"+taskID+"/messages", map[string]string{"text": "again"}, nil)
	if got := <-agent.Contexts; got != "" {
		t.Fatalf("second reply should have no context: %q", got)
	}
}

func TestRoomMessageGoesToRoomAgent(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	c := h.ConnectClient()
	h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))

	var room protocol.Room
	h.API("POST", "/api/rooms", map[string]string{"name": "Chat", "agentId": a.Agent.ID}, &room)
	var task protocol.Task
	if s := h.API("POST", "/api/rooms/"+room.ID+"/messages", map[string]string{"text": "Hello"}, &task); s != http.StatusCreated {
		t.Fatalf("room message: %d", s)
	}
	if task.RoomID != room.ID || task.AgentID != a.Agent.ID {
		t.Fatalf("task: %+v", task)
	}
	var posts []map[string]any
	h.API("GET", "/api/rooms/"+room.ID+"/posts", nil, &posts)
	if len(posts) != 1 {
		t.Fatalf("posts: %d", len(posts))
	}
	if s := h.API("POST", "/api/rooms", map[string]string{"name": "x", "agentId": "nope"}, nil); s != http.StatusNotFound {
		t.Fatalf("unknown agent: %d", s)
	}
}

func TestReportIsStoredAndBroadcast(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))

	agent.Send(context.Background(), map[string]any{"type": "report", "report": "a string is not allowed"})
	agent.Send(context.Background(), map[string]any{"type": "report", "report": map[string]any{"routines": []any{map[string]any{"id": "job1"}}}})
	h.WaitFor(c, "report", func(m protocol.ClientMessage) bool {
		return m.Type == "report" && m.AgentID == a.Agent.ID && bytes.Contains(m.Report, []byte("job1"))
	})
	var got struct {
		Report map[string]any `json:"report"`
		At     int64          `json:"at"`
	}
	h.API("GET", "/api/agents/"+a.Agent.ID+"/report", nil, &got)
	if got.At == 0 || got.Report["routines"] == nil {
		t.Fatalf("report: %+v", got)
	}
}

func TestReplyQuotesAndReactionsToggle(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))

	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "First question"}, &task)
	h.WaitFor(c, "waiting", decktest.TaskState(task.ID, "waiting"))
	var events []protocol.Event
	h.API("GET", "/api/tasks/"+task.ID+"/events", nil, &events)
	first := events[0].ID // task.start "First question"

	if s := h.API("POST", "/api/tasks/"+task.ID+"/messages", map[string]string{"text": "this one", "quoteId": strconv.FormatInt(first, 10)}, nil); s != http.StatusNoContent {
		t.Fatalf("reply: %d", s)
	}
	if got := <-agent.Quotes; got != "First question" {
		t.Fatalf("agent quote: %q", got)
	}
	h.API("GET", "/api/tasks/"+task.ID+"/events", nil, &events)
	last := events[len(events)-1]
	for _, e := range events {
		if e.Type == "user_message" {
			last = e
		}
	}
	if !bytes.Contains(last.Payload, []byte(`"quote":{"id":`)) || !bytes.Contains(last.Payload, []byte("First question")) {
		t.Fatalf("stored quote: %s", last.Payload)
	}

	if s := h.API("POST", "/api/tasks/"+task.ID+"/messages", map[string]string{"text": "First question edited", "editId": strconv.FormatInt(first, 10)}, nil); s != http.StatusNoContent {
		t.Fatalf("edit: %d", s)
	}
	if got := <-agent.Quotes; got != "First question" {
		t.Fatalf("edit should quote original: %q", got)
	}
	h.API("GET", "/api/tasks/"+task.ID+"/events", nil, &events)
	var edited []byte
	for _, e := range events {
		if e.Type == "user_message" && bytes.Contains(e.Payload, []byte(`"edit":{"id":`)) {
			edited = e.Payload
		}
	}
	if !bytes.Contains(edited, []byte("First question edited")) {
		t.Fatalf("stored edit: %s", edited)
	}
	agentMsg := int64(0)
	for _, e := range events {
		if e.Type == "message" {
			agentMsg = e.ID
		}
	}
	if agentMsg != 0 {
		if s := h.API("POST", "/api/tasks/"+task.ID+"/messages", map[string]string{"text": "x", "editId": strconv.FormatInt(agentMsg, 10)}, nil); s != http.StatusBadRequest {
			t.Fatalf("editing agent message should be 400: %d", s)
		}
	}

	var other protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "Other task"}, &other)
	if s := h.API("POST", "/api/tasks/"+other.ID+"/messages", map[string]string{"text": "x", "quoteId": strconv.FormatInt(first, 10)}, nil); s != http.StatusBadRequest {
		t.Fatalf("quote from other task should be 400: %d", s)
	}
	if s := h.API("POST", "/api/tasks/"+task.ID+"/messages", map[string]string{"text": "x", "quoteId": "999999"}, nil); s != http.StatusBadRequest {
		t.Fatalf("unknown quote: %d", s)
	}

	react := func(target int64) (int, bool) {
		var out struct{ On bool }
		s := h.API("POST", "/api/tasks/"+task.ID+"/reactions", map[string]string{"eventId": strconv.FormatInt(target, 10), "emoji": "👍"}, &out)
		return s, out.On
	}
	if s, on := react(first); s != http.StatusOK || !on {
		t.Fatalf("first react: %d %v", s, on)
	}
	if s, on := react(first); s != http.StatusOK || on {
		t.Fatalf("second react should turn off: %d %v", s, on)
	}
	if s, _ := react(999999); s != http.StatusNotFound {
		t.Fatalf("unknown target: %d", s)
	}
}

func TestRoutineToggleReachesAgent(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))

	path := "/api/agents/" + a.Agent.ID + "/routines/job1/toggle"
	if s := h.API("POST", path, map[string]string{"on": "false"}, nil); s != http.StatusAccepted {
		t.Fatalf("toggle: %d", s)
	}
	if got := <-agent.Toggles; got != "job1=off" {
		t.Fatalf("agent got %q", got)
	}
	if s := h.API("DELETE", "/api/agents/"+a.Agent.ID+"/routines/job1", nil, nil); s != http.StatusAccepted {
		t.Fatalf("remove: %d", s)
	}
	if got := <-agent.Toggles; got != "job1=removed" {
		t.Fatalf("agent got %q", got)
	}
	routines := "/api/agents/" + a.Agent.ID + "/routines"
	if s := h.API("POST", routines, map[string]string{"name": "Morning news", "schedule": "0 9 * * 1-5", "prompt": "summarize news"}, nil); s != http.StatusAccepted {
		t.Fatalf("create: %d", s)
	}
	if got := <-agent.Toggles; got != "Morning news=created@0 9 * * 1-5" {
		t.Fatalf("agent got %q", got)
	}
	if s := h.API("POST", routines, map[string]string{"name": "x", "schedule": "daily", "prompt": "y"}, nil); s != http.StatusBadRequest {
		t.Fatalf("bad schedule: %d", s)
	}
	if s := h.API("POST", path, map[string]string{"on": "maybe"}, nil); s != http.StatusBadRequest {
		t.Fatalf("bad on: %d", s)
	}
	if s := h.API("POST", "/api/agents/nope/routines/job1/toggle", map[string]string{"on": "true"}, nil); s != http.StatusNotFound {
		t.Fatalf("unknown agent: %d", s)
	}
}

func TestAgentAvatar(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	if !strings.HasPrefix(a.Agent.Avatar, "preset:") {
		t.Fatalf("new agent avatar: %q", a.Agent.Avatar)
	}
	c := h.ConnectClient()
	path := "/api/agents/" + a.Agent.ID + "/avatar"
	if s := h.API("PUT", path, map[string]string{"avatar": "preset:owl"}, nil); s != http.StatusOK {
		t.Fatalf("preset: %d", s)
	}
	h.WaitFor(c, "broadcast", func(m protocol.ClientMessage) bool { return m.Type == "agent" && m.Agent.Avatar == "preset:owl" })
	if s := h.API("PUT", path, map[string]string{"avatar": "preset:maltipooG"}, nil); s != http.StatusOK {
		t.Fatalf("motion preset: %d", s)
	}
	h.WaitFor(c, "motion", func(m protocol.ClientMessage) bool { return m.Type == "agent" && m.Agent.Avatar == "preset:maltipooG" })
	photo := "data:image/jpeg;base64," + strings.Repeat("A", 1000)
	if s := h.API("PUT", path, map[string]string{"avatar": photo}, nil); s != http.StatusOK {
		t.Fatalf("photo: %d", s)
	}
	for _, bad := range []string{"preset:dragon", "preset:pinkear", "preset:maltipoog", "data:text/html;base64,AAAA", "https://evil.example/x.png", "data:image/png;base64," + strings.Repeat("A", 200_001)} {
		if s := h.API("PUT", path, map[string]string{"avatar": bad}, nil); s != http.StatusBadRequest {
			t.Fatalf("%.40s…: %d", bad, s)
		}
	}
}

func TestUnlinkAgent(t *testing.T) {
	h := decktest.New(t)
	c := h.ConnectClient()
	a := h.CreateAgent("To delete")
	h.WaitFor(c, "created", func(m protocol.ClientMessage) bool { return m.Type == "agent" && m.Agent.ID == a.Agent.ID })
	keep := h.CreateAgent("To keep")
	h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	if s := h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "work"}, nil); s != http.StatusCreated {
		t.Fatalf("start: %d", s)
	}
	if s := h.API("DELETE", "/api/agents/"+a.Agent.ID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("delete: %d", s)
	}
	h.WaitFor(c, "removed", func(m protocol.ClientMessage) bool { return m.Type == "agent.removed" && m.AgentID == a.Agent.ID })

	var agents []protocol.Agent
	h.API("GET", "/api/agents", nil, &agents)
	if len(agents) != 1 || agents[0].ID != keep.Agent.ID {
		t.Fatalf("agents after delete: %+v", agents)
	}
	var tasks []protocol.Task
	h.API("GET", "/api/tasks", nil, &tasks)
	if len(tasks) != 0 {
		t.Fatalf("tasks after delete: %d", len(tasks))
	}
	if s := h.API("DELETE", "/api/agents/"+a.Agent.ID, nil, nil); s != http.StatusNotFound {
		t.Fatalf("second delete: %d", s)
	}
	if s := h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "again"}, nil); s != http.StatusNotFound {
		t.Fatalf("task to unlinked agent: %d", s)
	}
	if s := h.APIAs("someone-else", "DELETE", "/api/agents/"+keep.Agent.ID, nil, nil); s == http.StatusNoContent {
		t.Fatal("other owner deleted my agent")
	}
}

func TestHideTask(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("A")
	c := h.ConnectClient()
	h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "work"}, &task)
	if s := h.API("POST", "/api/tasks/"+task.ID+"/cancel", nil, nil); s/100 != 2 {
		t.Fatalf("cancel: %d", s)
	}
	h.WaitFor(c, "cancelled", decktest.TaskState(task.ID, "cancelled"))
	if s := h.API("DELETE", "/api/tasks/"+task.ID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("hide: %d", s)
	}
	h.WaitFor(c, "removed", func(m protocol.ClientMessage) bool { return m.Type == "task.removed" && m.Task.ID == task.ID })
	var tasks []protocol.Task
	h.API("GET", "/api/tasks", nil, &tasks)
	if len(tasks) != 0 {
		t.Fatalf("hidden task still listed: %d", len(tasks))
	}

	var live protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "In progress"}, &live)
	if s := h.API("DELETE", "/api/tasks/"+live.ID, nil, nil); s != http.StatusConflict {
		t.Fatalf("hide running: %d", s)
	}
}

func TestAgentReactsAndManagesOwnRooms(t *testing.T) {
	h := decktest.New(t)
	a, b := h.CreateAgent("A"), h.CreateAgent("B")
	c := h.ConnectClient()
	agentA := h.StartFakeAgent(a.Token)
	agentB := h.StartFakeAgent(b.Token)
	h.WaitAll(c, "A, B idle", decktest.AgentState(a.Agent.ID, "idle"), decktest.AgentState(b.Agent.ID, "idle"))
	ctx := context.Background()

	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "고마워"}, &task)
	agentA.Send(ctx, protocol.AgentMessage{Type: "reaction", Task: task.ID, Emoji: "❤️"})
	h.WaitFor(c, "agent reaction", func(m protocol.ClientMessage) bool {
		return m.Type == "event" && m.Event.Type == "reaction" && m.Event.Source == "agent" && bytes.Contains(m.Event.Payload, []byte(`"emoji":"❤️"`))
	})

	agentA.Send(ctx, protocol.AgentMessage{Type: "room.create", Name: "Contest news", Routine: "job1"})
	var room protocol.Room
	h.WaitFor(c, "room created", func(m protocol.ClientMessage) bool {
		if m.Type == "room" && m.Room.Name == "Contest news" && m.Room.AgentID == a.Agent.ID && m.Room.RoutineID == "job1" {
			room = *m.Room
			return true
		}
		return false
	})
	agentB.Send(ctx, protocol.AgentMessage{Type: "room.rename", ID: room.ID, Name: "hijack"})
	if reason := <-agentB.Errors; reason != "unknown room "+room.ID {
		t.Fatalf("B renaming A's room: %s", reason)
	}
	agentA.Send(ctx, protocol.AgentMessage{Type: "room.rename", ID: room.ID, Name: "Weekly contest"})
	h.WaitFor(c, "room renamed", func(m protocol.ClientMessage) bool {
		return m.Type == "room" && m.Room.ID == room.ID && m.Room.Name == "Weekly contest"
	})
}

func TestDeleteRoom(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("A")
	c := h.ConnectClient()
	var room protocol.Room
	if s := h.API("POST", "/api/rooms", map[string]string{"name": "Chat", "agentId": a.Agent.ID}, &room); s != http.StatusCreated {
		t.Fatalf("create: %d", s)
	}
	if s := h.API("DELETE", "/api/rooms/"+room.ID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("delete: %d", s)
	}
	h.WaitFor(c, "removed", func(m protocol.ClientMessage) bool { return m.Type == "room.removed" && m.Room.ID == room.ID })
	var rooms []protocol.Room
	h.API("GET", "/api/rooms", nil, &rooms)
	if len(rooms) != 0 {
		t.Fatalf("rooms left: %d", len(rooms))
	}
	if s := h.API("DELETE", "/api/rooms/"+room.ID, nil, nil); s != http.StatusNotFound {
		t.Fatalf("second delete: %d", s)
	}
}

func TestDraftIsStreamedNotStored(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("A")
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "Explain at length"}, &task)
	agent.Send(context.Background(), protocol.AgentMessage{Type: "draft", Task: task.ID, Text: "answer being written"})
	h.WaitFor(c, "draft", func(m protocol.ClientMessage) bool {
		return m.Type == "draft" && m.TaskID == task.ID && m.Text == "answer being written"
	})
	var events []protocol.Event
	h.API("GET", "/api/tasks/"+task.ID+"/events", nil, &events)
	for _, e := range events {
		if bytes.Contains(e.Payload, []byte("answer being written")) {
			t.Fatal("draft was stored")
		}
	}
}

func TestSearchAcrossConversations(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("A")
	c := h.ConnectClient()
	h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	var one, two protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "제주도 여행 계획"}, &one)
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "100% 확실한 제주 맛집"}, &two)

	var found []protocol.Event
	if s := h.API("GET", "/api/search?q=%EC%A0%9C%EC%A3%BC", nil, &found); s != http.StatusOK || len(found) != 2 || found[0].TaskID != two.ID {
		t.Fatalf("search Jeju: %d %+v", s, found)
	}
	h.API("GET", "/api/search?q=100%25", nil, &found)
	if len(found) != 1 || found[0].TaskID != two.ID {
		t.Fatalf("percent is literal: %+v", found)
	}
	h.API("GET", "/api/search?q=%25", nil, &found)
	if len(found) != 1 {
		t.Fatalf("lone %% must not match everything: %d", len(found))
	}
	h.API("POST", "/api/tasks/"+one.ID+"/cancel", nil, nil)
	h.WaitFor(c, "cancelled", decktest.TaskState(one.ID, "cancelled"))
	h.API("DELETE", "/api/tasks/"+one.ID, nil, nil)
	h.API("GET", "/api/search?q=%EC%97%AC%ED%96%89", nil, &found)
	if len(found) != 0 {
		t.Fatalf("hidden task still searchable: %+v", found)
	}
	if s := h.API("GET", "/api/search?q=", nil, nil); s != http.StatusBadRequest {
		t.Fatalf("empty q: %d", s)
	}
}

func TestTaskTitleFromAgentAndUser(t *testing.T) {
	h := decktest.New(t)
	a, b := h.CreateAgent("A"), h.CreateAgent("B")
	c := h.ConnectClient()
	agentA := h.StartFakeAgent(a.Token)
	agentB := h.StartFakeAgent(b.Token)
	h.WaitAll(c, "A, B idle", decktest.AgentState(a.Agent.ID, "idle"), decktest.AgentState(b.Agent.ID, "idle"))
	ctx := context.Background()

	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "고마워"}, &task)
	titled := func(want string) func(protocol.ClientMessage) bool {
		return func(m protocol.ClientMessage) bool {
			return m.Type == "task" && m.Task.ID == task.ID && m.Task.Title == want
		}
	}
	listed := func() string {
		var tasks []protocol.Task
		h.API("GET", "/api/tasks", nil, &tasks)
		for _, x := range tasks {
			if x.ID == task.ID {
				return x.Title
			}
		}
		return ""
	}

	agentA.Send(ctx, protocol.AgentMessage{Type: "task.title", Task: task.ID, Title: "  Thank you\nnote "})
	h.WaitFor(c, "agent title", titled("Thank you note"))
	if got := listed(); got != "Thank you note" {
		t.Fatalf("listed after agent title: %q", got)
	}

	agentB.Send(ctx, protocol.AgentMessage{Type: "task.title", Task: task.ID, Title: "hijack"})
	if reason := <-agentB.Errors; reason != "unknown task "+task.ID {
		t.Fatalf("B titling A's task: %s", reason)
	}
	agentA.Send(ctx, protocol.AgentMessage{Type: "task.title", Task: task.ID, Title: strings.Repeat("가", 81)})
	if reason := <-agentA.Errors; reason != "invalid title" {
		t.Fatalf("long agent title: %s", reason)
	}

	for _, bad := range []map[string]any{{"title": ""}, {"title": "   \n "}, {"title": strings.Repeat("가", 81)}, {}} {
		if s := h.API("PATCH", "/api/tasks/"+task.ID, bad, nil); s != http.StatusBadRequest {
			t.Fatalf("patch %v: %d", bad, s)
		}
	}
	if s := h.API("PATCH", "/api/tasks/no-such-task", map[string]string{"title": "x"}, nil); s != http.StatusNotFound {
		t.Fatalf("patch unknown: %d", s)
	}
	var renamed protocol.Task
	if s := h.API("PATCH", "/api/tasks/"+task.ID, map[string]string{"title": strings.Repeat("가", 80)}, &renamed); s != http.StatusOK || renamed.Title != strings.Repeat("가", 80) {
		t.Fatalf("patch 80: %d %q", s, renamed.Title)
	}
	if s := h.API("PATCH", "/api/tasks/"+task.ID, map[string]string{"title": "My own name"}, &renamed); s != http.StatusOK || renamed.Title != "My own name" {
		t.Fatalf("patch: %d %+v", s, renamed)
	}
	h.WaitFor(c, "user title", titled("My own name"))

	agentA.Send(ctx, protocol.AgentMessage{Type: "task.title", Task: task.ID, Title: "Agent renamed it"})
	agentA.Send(ctx, protocol.AgentMessage{Type: "reaction", Task: task.ID, Emoji: "👍"}) // catches the point after the previous message is processed
	h.WaitFor(c, "after agent retitle", func(m protocol.ClientMessage) bool { return m.Type == "event" && m.Event.Type == "reaction" })
	if got := listed(); got != "My own name" {
		t.Fatalf("agent overrode user title: %q", got)
	}
	select {
	case reason := <-agentA.Errors:
		t.Fatalf("ignored title should be silent: %s", reason)
	default:
	}
}

func TestAgentHelloSavesInfo(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("A")
	c := h.ConnectClient()
	ag := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "A idle", decktest.AgentState(a.Agent.ID, "idle"))

	ag.Send(context.Background(), protocol.AgentMessage{Type: "hello", Kind: "Hermes", Version: "0.9", Models: []protocol.AgentModel{{Role: "chat", Name: "m1"}, {Role: "delegate", Name: "m2"}}})
	h.WaitFor(c, "A info", func(m protocol.ClientMessage) bool {
		return m.Type == "agent" && m.Agent != nil && m.Agent.ID == a.Agent.ID && m.Agent.Info != nil && len(m.Agent.Info.Models) == 2
	})
	var list []protocol.Agent
	h.API("GET", "/api/agents", nil, &list)
	if len(list) != 1 || list[0].Info == nil || list[0].Info.Kind != "Hermes" || list[0].Info.Models[1].Name != "m2" {
		t.Fatalf("info not stored: %+v", list)
	}

	many := make([]protocol.AgentModel, 11)
	for i := range many {
		many[i] = protocol.AgentModel{Role: "r", Name: "n"}
	}
	ag.Send(context.Background(), protocol.AgentMessage{Type: "hello", Kind: "X", Models: many})
	select {
	case reason := <-ag.Errors:
		if reason != "invalid hello" {
			t.Fatalf("reason: %s", reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("too many models was accepted")
	}
}
