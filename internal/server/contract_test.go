package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket/wsjson"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/fakeagent"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

// readUntil has the minimal agent read server messages and return the one that matches the condition.
func readUntil(t *testing.T, a *fakeagent.Agent, what string, pred func(protocol.ServerMessage) bool) protocol.ServerMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		var m protocol.ServerMessage
		if err := wsjson.Read(ctx, a.Conn, &m); err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		if pred(m) {
			return m
		}
	}
}

func TestAgentHearsWhoAsked(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("bot")
	owner := h.ConnectClient()
	agent := rawAgent(t, h, a.Token)
	h.WaitFor(owner, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	var room protocol.Room
	h.API("POST", "/api/rooms", map[string]any{"name": "Contest", "agentId": a.Agent.ID}, &room)
	tok2 := invite(t, h, "Bob")
	var inv struct{ Code string }
	h.API("POST", "/api/rooms/"+room.ID+"/invite", nil, &inv)
	h.APIAs(tok2, "POST", "/api/rooms/join", map[string]string{"code": inv.Code}, nil)
	var members []struct{ OwnerID string }
	h.API("GET", "/api/rooms/"+room.ID+"/members", nil, &members)
	h.API("PATCH", "/api/rooms/"+room.ID+"/members/"+members[0].OwnerID, map[string]string{"role": "agent"}, nil)

	h.API("POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "owner asks", "ask": true}, nil)
	m := readUntil(t, agent, "owner task.start", func(m protocol.ServerMessage) bool { return m.Type == "task.start" })
	if m.AskedBy != "" || m.AskedByName != "" {
		t.Fatalf("owner ask should not carry askedBy: %+v", m)
	}
	h.APIAs(tok2, "POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "member asks", "ask": true}, nil)
	m = readUntil(t, agent, "member task.start", func(m protocol.ServerMessage) bool { return m.Type == "task.start" && m.Text == "member asks" })
	if m.AskedBy != members[0].OwnerID || m.AskedByName != "Bob" {
		t.Fatalf("member ask should carry askedBy: %+v", m)
	}
}

func TestEditArrivesAsFieldNotPrefix(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("bot")
	c := h.ConnectClient()
	agent := rawAgent(t, h, a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "first words"}, &task)
	readUntil(t, agent, "task.start", func(m protocol.ServerMessage) bool { return m.Type == "task.start" })
	agent.Send(context.Background(), protocol.AgentMessage{Type: "message", Task: task.ID, Text: "reply"})
	agent.Send(context.Background(), protocol.AgentMessage{Type: "task.end", Task: task.ID, Result: "done"})
	h.WaitFor(c, "done", decktest.TaskState(task.ID, "done"))
	var events []protocol.Event
	h.API("GET", "/api/tasks/"+task.ID+"/events", nil, &events)
	first := events[0].ID
	if s := h.API("POST", "/api/tasks/"+task.ID+"/messages", map[string]any{"text": "edited words", "editId": strconv.FormatInt(first, 10)}, nil); s != http.StatusNoContent {
		t.Fatalf("edit: %d", s)
	}
	m := readUntil(t, agent, "edited user_message", func(m protocol.ServerMessage) bool { return m.Type == "user_message" })
	if m.Text != "edited words" || m.Edit == nil || m.Edit.ID != first || m.Edit.Original != "first words" || m.Quote != "first words" {
		t.Fatalf("edit should be a field, text untouched: %+v edit=%+v", m, m.Edit)
	}
}

func TestUIAckEchoesReq(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("bot")
	c := h.ConnectClient()
	agent := rawAgent(t, h, a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	agent.Send(context.Background(), protocol.AgentMessage{Type: "data.put", Name: "revenue", Data: json.RawMessage(`{"x":1}`), Req: "r-7"})
	m := readUntil(t, agent, "ui.ack", func(m protocol.ServerMessage) bool { return m.Type == "ui.ack" })
	if m.Req != "r-7" || m.OK == nil || !*m.OK || m.ID != "revenue" {
		t.Fatalf("ack: %+v", m)
	}
}

func TestPairWithoutHeaderWhenNotBrowser(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("bot")
	code := func() string {
		var c struct{ Code string }
		h.API("POST", "/api/agents/"+a.Agent.ID+"/pair", nil, &c)
		return c.Code
	}
	post := func(code string, headers map[string]string) int {
		body, _ := json.Marshal(map[string]string{"code": code})
		req, _ := http.NewRequest("POST", h.URL+"/pair/agent", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if s := post(code(), nil); s != http.StatusOK {
		t.Fatalf("curl-style pairing without the header: %d", s)
	}
	if s := post(code(), map[string]string{"Origin": "https://evil.example"}); s != http.StatusBadRequest {
		t.Fatalf("browser without the header: %d", s)
	}
	if s := post(code(), map[string]string{"Origin": "https://evil.example", "X-Kyeot-Pair": "1"}); s != http.StatusOK {
		t.Fatalf("browser with the header: %d", s)
	}
}

func TestAgentFetchesTaskUploadByToken(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("bot")
	b := h.CreateAgent("otherbot")
	h.StartFakeAgent(a.Token)
	waitState(t, h.URL, a.Agent.ID, protocol.AgentIdle)
	var up, unsent protocol.Attachment
	h.Post("/api/uploads?name=a.png", "image/png", []byte("PNGDATA"), &up)
	h.Post("/api/uploads?name=b.png", "image/png", []byte("PNGDATA"), &unsent)
	var task protocol.Task
	if s := h.API("POST", "/api/tasks", map[string]any{"agentId": a.Agent.ID, "text": "look", "attachments": []string{up.ID}}, &task); s != http.StatusCreated {
		t.Fatalf("start: %d", s)
	}
	var later protocol.Attachment
	h.Post("/api/uploads?name=c.png", "image/png", []byte("PNGDATA"), &later)
	if s := h.API("POST", "/api/tasks/"+task.ID+"/messages", map[string]any{"text": "this too", "attachments": []string{later.ID}}, nil); s != http.StatusNoContent {
		t.Fatalf("message: %d", s)
	}
	for _, id := range []string{up.ID, later.ID} {
		if s := h.APIAs(a.Token, "GET", "/api/uploads/"+id, nil, nil); s != http.StatusOK {
			t.Fatalf("task agent fetch with its token: %d", s)
		}
	}
	if s := h.APIAs(b.Token, "GET", "/api/uploads/"+up.ID, nil, nil); s != http.StatusNotFound {
		t.Fatalf("same owner's other agent fetched another task's upload: %d", s)
	}
	if s := h.APIAs(a.Token, "GET", "/api/uploads/"+unsent.ID, nil, nil); s != http.StatusNotFound {
		t.Fatalf("agent fetched an unsent upload: %d", s)
	}
	tok2 := invite(t, h, "Bob")
	var other struct{ Token string }
	h.APIAs(tok2, "POST", "/api/agents", map[string]string{"name": "theirbot"}, &other)
	if s := h.APIAs(other.Token, "GET", "/api/uploads/"+up.ID, nil, nil); s != http.StatusNotFound {
		t.Fatalf("other owner's agent fetched the upload: %d", s)
	}
}
