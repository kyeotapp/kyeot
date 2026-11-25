package server_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/fakeagent"
	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/server"
)

// rawAgent is a minimal agent that only sends messages, with no automatic replies. It does not read messages the server sends.
func rawAgent(t *testing.T, h *decktest.Harness, token string) *fakeagent.Agent {
	t.Helper()
	a, err := fakeagent.Dial(context.Background(), h.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

func TestDisconnectedAgentFailsOpenTasks(t *testing.T) {
	h := decktest.NewWith(t, func(o *server.Options) { o.AgentGrace = 150 * time.Millisecond })
	a := h.CreateAgent("bot")
	c := h.ConnectClient()
	agent := rawAgent(t, h, a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "do it"}, &task)
	agent.Close()
	h.WaitFor(c, "offline", decktest.AgentState(a.Agent.ID, "offline"))
	var tasks []protocol.Task
	h.API("GET", "/api/tasks", nil, &tasks)
	if tasks[0].State != "running" {
		t.Fatalf("task should survive the grace period: %s", tasks[0].State)
	}
	h.WaitFor(c, "task failed after grace", decktest.TaskState(task.ID, "failed"))
	if s := h.API("DELETE", "/api/tasks/"+task.ID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("hide after fail: %d", s)
	}
}

func TestIdleClosesAnsweredTasksOnly(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("bot")
	c := h.ConnectClient()
	agent := rawAgent(t, h, a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	var answered, silent protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "one"}, &answered)
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "two"}, &silent)
	agent.Send(context.Background(), protocol.AgentMessage{Type: "message", Task: answered.ID, Text: "done"})
	h.WaitFor(c, "reply", func(m protocol.ClientMessage) bool {
		return m.Type == "event" && m.Event.TaskID == answered.ID && m.Event.Type == "message"
	})
	agent.Send(context.Background(), protocol.AgentMessage{Type: "status", State: "idle"})
	h.WaitFor(c, "answered task done", decktest.TaskState(answered.ID, "done"))
	var tasks []protocol.Task
	h.API("GET", "/api/tasks", nil, &tasks)
	for _, x := range tasks {
		if x.ID == silent.ID && x.State != "running" {
			t.Fatalf("unanswered task should stay running: %s", x.State)
		}
	}
}

func TestTwoApprovalsBothAnswerable(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("bot")
	c := h.ConnectClient()
	agent := rawAgent(t, h, a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "ask both"}, &task)
	for _, id := range []string{"a1", "a2"} {
		agent.Send(context.Background(), protocol.AgentMessage{Type: "approval", Task: task.ID, ID: id, Question: id + "?", Options: []string{"Yes", "No"}})
	}
	h.WaitFor(c, "second approval", func(m protocol.ClientMessage) bool {
		return m.Type == "event" && m.Event.Type == "approval" && bytes.Contains(m.Event.Payload, []byte(`"id":"a2"`))
	})
	if s := h.API("POST", "/api/tasks/"+task.ID+"/approvals/a1", map[string]string{"choice": "Yes"}, nil); s != http.StatusNoContent {
		t.Fatalf("answer a1: %d", s)
	}
	var pending []protocol.Event
	h.API("GET", "/api/approvals", nil, &pending)
	if len(pending) != 1 || !bytes.Contains(pending[0].Payload, []byte(`"id":"a2"`)) {
		t.Fatalf("a2 should still be pending: %+v", pending)
	}
	if s := h.API("POST", "/api/tasks/"+task.ID+"/approvals/a2", map[string]string{"choice": "No"}, nil); s != http.StatusNoContent {
		t.Fatalf("answer a2: %d", s)
	}
	h.WaitFor(c, "running after both", decktest.TaskState(task.ID, "running"))
}

// Reattaching within the grace period does not close the task (bridge reconnect).
func TestReconnectWithinGraceKeepsTasks(t *testing.T) {
	h := decktest.NewWith(t, func(o *server.Options) { o.AgentGrace = 500 * time.Millisecond })
	a := h.CreateAgent("bot")
	c := h.ConnectClient()
	first := rawAgent(t, h, a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "do it"}, &task)
	first.Close()
	h.WaitFor(c, "offline", decktest.AgentState(a.Agent.ID, "offline"))
	second := rawAgent(t, h, a.Token)
	h.WaitFor(c, "back", decktest.AgentState(a.Agent.ID, "idle"))
	time.Sleep(700 * time.Millisecond)
	var tasks []protocol.Task
	h.API("GET", "/api/tasks", nil, &tasks)
	if tasks[0].State != "running" {
		t.Fatalf("task should survive a reconnect: %s", tasks[0].State)
	}
	second.Send(context.Background(), protocol.AgentMessage{Type: "task.end", Task: task.ID, Result: "done"})
	h.WaitFor(c, "done", decktest.TaskState(task.ID, "done"))
}

// leftover builds a DB where an agent on server 1 disconnected leaving one task. Server 1 gets a long grace so it does not close it itself.
func leftover(t *testing.T) (db string, agent decktest.Created, task protocol.Task) {
	t.Helper()
	db = t.TempDir() + "/deck.db"
	h := decktest.NewWith(t, func(o *server.Options) { o.DBPath = db; o.AgentGrace = time.Hour })
	agent = h.CreateAgent("bot")
	c := h.ConnectClient()
	a := rawAgent(t, h, agent.Token)
	h.WaitFor(c, "idle", decktest.AgentState(agent.Agent.ID, "idle"))
	h.API("POST", "/api/tasks", map[string]string{"agentId": agent.Agent.ID, "text": "do it"}, &task)
	a.Close()
	h.WaitFor(c, "offline", decktest.AgentState(agent.Agent.ID, "offline"))
	h.Close() // one server per DB
	return db, agent, task
}

func TestRestartFailsLeftoverTasks(t *testing.T) {
	db, _, task := leftover(t)
	h := decktest.NewWith(t, func(o *server.Options) { o.DBPath = db; o.AgentGrace = 150 * time.Millisecond })
	c := h.ConnectClient()
	h.WaitFor(c, "leftover task failed after restart", decktest.TaskState(task.ID, "failed"))
}

func TestRestartKeepsLeftoverTaskThatHearsBack(t *testing.T) {
	db, agent, task := leftover(t)
	h := decktest.NewWith(t, func(o *server.Options) { o.DBPath = db; o.AgentGrace = 300 * time.Millisecond })
	c := h.ConnectClient()
	a := rawAgent(t, h, agent.Token)
	a.Send(context.Background(), protocol.AgentMessage{Type: "message", Task: task.ID, Text: "reply written while disconnected"})
	h.WaitFor(c, "late reply", func(m protocol.ClientMessage) bool {
		return m.Type == "event" && m.Event.TaskID == task.ID && m.Event.Type == "message"
	})
	h.Quiet(c, "task that heard back must not fail", time.Second, decktest.TaskState(task.ID, "failed"))
}
