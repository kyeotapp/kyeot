package server_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

// Even if an agent that re-attached after a restart reports idle (demo agent, an agent that doesn't use status), the pending approval stays alive and can be answered.
func TestRestartKeepsWaitingTaskWhenAgentReturnsIdle(t *testing.T) {
	db, agent, task := leftoverWaiting(t)
	h := restarted(t, db)
	c := h.ConnectClient()
	a := rawAgent(t, h, agent.Token)
	a.Send(context.Background(), protocol.AgentMessage{Type: "status", State: "idle"})
	h.WaitFor(c, "idle", decktest.AgentState(agent.Agent.ID, "idle"))
	h.Quiet(c, "waiting task must survive the restart", 700*time.Millisecond, decktest.TaskState(task.ID, "failed"))
	var pending []protocol.Event
	h.API("GET", "/api/approvals", nil, &pending)
	if len(pending) != 1 {
		t.Fatalf("pending approval list: %+v", pending)
	}
	if s := h.API("POST", "/api/tasks/"+task.ID+"/approvals/ap1", map[string]string{"choice": "Yes"}, nil); s != http.StatusNoContent {
		t.Fatalf("approval after restart: %d", s)
	}
	h.WaitFor(c, "running after approval", decktest.TaskState(task.ID, "running"))
}

// If the re-attached agent disconnects again for good, the pending approval is closed after the disconnect grace.
func TestRestartWaitingTaskFailsWhenAgentLeavesAgain(t *testing.T) {
	db, agent, task := leftoverWaiting(t)
	h := restarted(t, db)
	c := h.ConnectClient()
	a := rawAgent(t, h, agent.Token)
	a.Send(context.Background(), protocol.AgentMessage{Type: "status", State: "idle"})
	h.Quiet(c, "kept while connected", 500*time.Millisecond, decktest.TaskState(task.ID, "failed"))
	a.Close()
	h.WaitFor(c, "failed after agent left", decktest.TaskState(task.ID, "failed"))
}
