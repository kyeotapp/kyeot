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
