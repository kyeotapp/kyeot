package server_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

func approvalCount(t *testing.T, h *decktest.Harness, taskID string) int {
	t.Helper()
	var events []protocol.Event
	h.API("GET", "/api/tasks/"+taskID+"/events", nil, &events)
	n := 0
	for _, e := range events {
		if e.Type == "approval" {
			n++
		}
	}
	return n
}

func TestResentApprovalIsIgnoredUntilAnswered(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("bot")
	c := h.ConnectClient()
	agent := rawAgent(t, h, a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "ask me"}, &task)
	ask := protocol.AgentMessage{Type: "approval", Task: task.ID, ID: "ap-1", Question: "Delete it?", Options: []string{"Yes", "No"}}
	ctx := context.Background()
	agent.Send(ctx, ask)
	agent.Send(ctx, ask) // the same approval sent again on reconnect
	agent.Send(ctx, protocol.AgentMessage{Type: "message", Task: task.ID, Text: "endmark"})
	h.WaitFor(c, "marker", func(m protocol.ClientMessage) bool {
		return m.Type == "event" && m.Event.TaskID == task.ID && m.Event.Type == "message"
	})
	if n := approvalCount(t, h, task.ID); n != 1 {
		t.Fatalf("the same approval was stored %d times", n)
	}
	if s := h.API("POST", "/api/tasks/"+task.ID+"/approvals/ap-1", map[string]string{"choice": "Yes"}, nil); s != http.StatusNoContent {
		t.Fatalf("answer: %d", s)
	}
	// if the same id comes again after answering (the bridge restarted and numbering began again), it is a new approval
	agent.Send(ctx, ask)
	agent.Send(ctx, protocol.AgentMessage{Type: "message", Task: task.ID, Text: "endmark2"})
	h.WaitFor(c, "marker2", func(m protocol.ClientMessage) bool {
		return m.Type == "event" && m.Event.TaskID == task.ID && m.Event.Type == "message" && bytes.Contains(m.Event.Payload, []byte("endmark2"))
	})
	if n := approvalCount(t, h, task.ID); n != 2 {
		t.Fatalf("new approval after answering: %d", n)
	}
	// the same id with different content is not filtered out
	other := ask
	other.Question = "Move it?"
	agent.Send(ctx, other)
	agent.Send(ctx, protocol.AgentMessage{Type: "message", Task: task.ID, Text: "endmark3"})
	h.WaitFor(c, "marker3", func(m protocol.ClientMessage) bool {
		return m.Type == "event" && m.Event.TaskID == task.ID && m.Event.Type == "message" && bytes.Contains(m.Event.Payload, []byte("endmark3"))
	})
	if n := approvalCount(t, h, task.ID); n != 3 {
		t.Fatalf("approval with different content: %d", n)
	}
}

// A bridge restart restarts its numbering, so an answered id can return in the same task as a new approval; it must be answerable.
func TestReusedApprovalIDCanBeAnswered(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("bot")
	c := h.ConnectClient()
	agent := rawAgent(t, h, a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "ask me"}, &task)
	ctx := context.Background()
	agent.Send(ctx, protocol.AgentMessage{Type: "approval", Task: task.ID, ID: "ap-1", Question: "Delete it?", Options: []string{"Yes", "No"}})
	h.WaitFor(c, "waiting", decktest.TaskState(task.ID, "waiting"))
	if s := h.API("POST", "/api/tasks/"+task.ID+"/approvals/ap-1", map[string]string{"choice": "Yes"}, nil); s != http.StatusNoContent {
		t.Fatalf("first answer: %d", s)
	}
	h.WaitFor(c, "running", decktest.TaskState(task.ID, "running"))
	agent.Send(ctx, protocol.AgentMessage{Type: "approval", Task: task.ID, ID: "ap-1", Question: "Move it?", Options: []string{"Yes", "No"}})
	h.WaitFor(c, "waiting again", decktest.TaskState(task.ID, "waiting"))
	var pending []protocol.Event
	if h.API("GET", "/api/approvals", nil, &pending); len(pending) != 1 || !bytes.Contains(pending[0].Payload, []byte("Move it?")) {
		t.Fatalf("pending: %+v", pending)
	}
	// answering another approval must keep the task waiting on the reused id
	agent.Send(ctx, protocol.AgentMessage{Type: "approval", Task: task.ID, ID: "ap-2", Question: "Also this?", Options: []string{"Yes", "No"}})
	agent.Send(ctx, protocol.AgentMessage{Type: "message", Task: task.ID, Text: "endmark0"})
	h.WaitFor(c, "marker0", func(m protocol.ClientMessage) bool {
		return m.Type == "event" && m.Event.TaskID == task.ID && m.Event.Type == "message"
	})
	if s := h.API("POST", "/api/tasks/"+task.ID+"/approvals/ap-2", map[string]string{"choice": "Yes"}, nil); s != http.StatusNoContent {
		t.Fatalf("other answer: %d", s)
	}
	if h.API("GET", "/api/approvals", nil, &pending); len(pending) != 1 || !bytes.Contains(pending[0].Payload, []byte("Move it?")) {
		t.Fatalf("reused id hidden after another answer: %+v", pending)
	}
	if s := h.API("POST", "/api/tasks/"+task.ID+"/approvals/ap-1", map[string]string{"choice": "No"}, nil); s != http.StatusNoContent {
		t.Fatalf("second answer: %d", s)
	}
	h.WaitFor(c, "running again", decktest.TaskState(task.ID, "running"))
	// a newer request with the same id replaces the open one
	agent.Send(ctx, protocol.AgentMessage{Type: "approval", Task: task.ID, ID: "ap-1", Question: "Copy it?", Options: []string{"Yes", "No"}})
	agent.Send(ctx, protocol.AgentMessage{Type: "approval", Task: task.ID, ID: "ap-1", Question: "Rename it?", Options: []string{"Rename", "Keep"}})
	agent.Send(ctx, protocol.AgentMessage{Type: "message", Task: task.ID, Text: "endmark"})
	h.WaitFor(c, "marker", func(m protocol.ClientMessage) bool {
		return m.Type == "event" && m.Event.TaskID == task.ID && m.Event.Type == "message"
	})
	if h.API("GET", "/api/approvals", nil, &pending); len(pending) != 1 || !bytes.Contains(pending[0].Payload, []byte("Rename it?")) {
		t.Fatalf("only the newer request is open: %+v", pending)
	}
	if s := h.API("POST", "/api/tasks/"+task.ID+"/approvals/ap-1", map[string]string{"choice": "Rename"}, nil); s != http.StatusNoContent {
		t.Fatalf("answer to the newer: %d", s)
	}
	h.WaitFor(c, "running after the newer", decktest.TaskState(task.ID, "running"))
	if h.API("GET", "/api/approvals", nil, &pending); len(pending) != 0 {
		t.Fatalf("the replaced request is still open: %+v", pending)
	}
}
