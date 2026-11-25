package server_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/server"
)

// leftoverWaiting builds a DB where the agent disconnected from server 1 leaving a task waiting for approval.
func leftoverWaiting(t *testing.T) (db string, agent decktest.Created, task protocol.Task) {
	t.Helper()
	db = t.TempDir() + "/deck.db"
	h := decktest.NewWith(t, func(o *server.Options) { o.DBPath = db; o.AgentGrace = time.Hour })
	agent = h.CreateAgent("Bot")
	c := h.ConnectClient()
	a := rawAgent(t, h, agent.Token)
	h.WaitFor(c, "idle", decktest.AgentState(agent.Agent.ID, "idle"))
	h.API("POST", "/api/tasks", map[string]string{"agentId": agent.Agent.ID, "text": "Can I delete it?"}, &task)
	a.Send(context.Background(), protocol.AgentMessage{Type: "approval", Task: task.ID, ID: "ap1", Question: "Delete it?", Options: []string{"Yes", "No"}})
	h.WaitFor(c, "waiting", decktest.TaskState(task.ID, "waiting"))
	a.Close()
	h.WaitFor(c, "offline", decktest.AgentState(agent.Agent.ID, "offline"))
	h.Close() // one server per DB
	return db, agent, task
}

func restarted(t *testing.T, db string) *decktest.Harness {
	return decktest.NewWith(t, func(o *server.Options) { o.DBPath = db; o.AgentGrace = 200 * time.Millisecond })
}

// A task whose agent re-attached after a restart and reported it still holds the approval (blocked) is not closed, and a person can answer.
func TestRestartKeepsWaitingTaskOfBlockedAgent(t *testing.T) {
	db, agent, task := leftoverWaiting(t)
	h := restarted(t, db)
	c := h.ConnectClient()
	a := rawAgent(t, h, agent.Token)
	a.Send(context.Background(), protocol.AgentMessage{Type: "status", State: "blocked"})
	h.WaitFor(c, "blocked", decktest.AgentState(agent.Agent.ID, "blocked"))
	h.Quiet(c, "waiting task of a blocked agent must not fail", 600*time.Millisecond, decktest.TaskState(task.ID, "failed"))
	if s := h.API("POST", "/api/tasks/"+task.ID+"/approvals/ap1", map[string]string{"choice": "Yes"}, nil); s != http.StatusNoContent {
		t.Fatalf("approval after restart: %d", s)
	}
	h.WaitFor(c, "running after approval", decktest.TaskState(task.ID, "running"))
}

// A task whose agent re-attached after a restart and reported working is kept past the grace and accepts a late task.end.
func TestRestartKeepsRunningTaskOfWorkingAgent(t *testing.T) {
	db, agent, task := leftover(t)
	h := restarted(t, db)
	c := h.ConnectClient()
	a := rawAgent(t, h, agent.Token)
	a.Send(context.Background(), protocol.AgentMessage{Type: "status", State: "working"})
	h.Quiet(c, "running task of a working agent must not fail", 600*time.Millisecond, decktest.TaskState(task.ID, "failed"))
	a.Send(context.Background(), protocol.AgentMessage{Type: "task.end", Task: task.ID, Result: "done"})
	h.WaitFor(c, "late task.end", decktest.TaskState(task.ID, "done"))
}

// If the working agent goes idle without touching that task, close it then, so a task whose memory was lost doesn't linger forever.
func TestRestartFailsLeftoverWhenWorkingAgentGoesIdle(t *testing.T) {
	db, agent, task := leftover(t)
	h := restarted(t, db)
	c := h.ConnectClient()
	a := rawAgent(t, h, agent.Token)
	a.Send(context.Background(), protocol.AgentMessage{Type: "status", State: "working"})
	h.Quiet(c, "still working", 400*time.Millisecond, decktest.TaskState(task.ID, "failed"))
	a.Send(context.Background(), protocol.AgentMessage{Type: "status", State: "idle"})
	h.WaitFor(c, "failed once idle", decktest.TaskState(task.ID, "failed"))
}

// A task of a re-attached but idle agent (like the in-server adapter that lost memory on restart) is closed after the grace.
func TestRestartFailsLeftoverOfIdleAgent(t *testing.T) {
	db, agent, task := leftover(t)
	h := restarted(t, db)
	c := h.ConnectClient()
	rawAgent(t, h, agent.Token)
	h.WaitFor(c, "failed after grace", decktest.TaskState(task.ID, "failed"))
}

// Even if idle is reported before the grace, nothing is closed until the grace passes.
func TestRestartWaitsGraceEvenIfAgentIdle(t *testing.T) {
	db, agent, task := leftover(t)
	h := decktest.NewWith(t, func(o *server.Options) { o.DBPath = db; o.AgentGrace = 800 * time.Millisecond })
	c := h.ConnectClient()
	a := rawAgent(t, h, agent.Token)
	a.Send(context.Background(), protocol.AgentMessage{Type: "status", State: "idle"})
	h.Quiet(c, "closed before grace", 400*time.Millisecond, decktest.TaskState(task.ID, "failed"))
	h.WaitFor(c, "failed after grace", decktest.TaskState(task.ID, "failed"))
}

// If the agent doesn't come back, the task waiting for approval is closed too; nobody is left to take the answer (same rule as the disconnect grace).
func TestRestartFailsWaitingLeftoverWithoutAgent(t *testing.T) {
	db, _, task := leftoverWaiting(t)
	h := restarted(t, db)
	c := h.ConnectClient()
	h.WaitFor(c, "waiting leftover failed", decktest.TaskState(task.ID, "failed"))
}

// Shared-room member screens get the same status updates.
func TestRestartLeftoverReachesSharedRoomMember(t *testing.T) {
	db := t.TempDir() + "/deck.db"
	h1 := decktest.NewWith(t, func(o *server.Options) { o.DBPath = db; o.AgentGrace = time.Hour })
	agent := h1.CreateAgent("Bot")
	c1 := h1.ConnectClient()
	a := rawAgent(t, h1, agent.Token)
	h1.WaitFor(c1, "idle", decktest.AgentState(agent.Agent.ID, "idle"))
	friendTok := invite(t, h1, "Bob")
	var room protocol.Room
	h1.API("POST", "/api/rooms", map[string]any{"name": "Together", "agentId": agent.Agent.ID}, &room)
	var inv struct{ Code string }
	if s := h1.API("POST", "/api/rooms/"+room.ID+"/invite", nil, &inv); s != http.StatusOK {
		t.Fatalf("room invite: %d", s)
	}
	if s := h1.APIAs(friendTok, "POST", "/api/rooms/join", map[string]string{"code": inv.Code}, nil); s != http.StatusOK {
		t.Fatalf("join: %d", s)
	}
	if s := h1.API("POST", "/api/rooms/"+room.ID+"/messages", map[string]string{"text": "Do it"}, nil); s != http.StatusCreated {
		t.Fatalf("room ask: %d", s)
	}
	var taskID string
	h1.WaitFor(c1, "room task", func(m protocol.ClientMessage) bool {
		if m.Type == "task" && m.Task.State == "running" {
			taskID = m.Task.ID
			return true
		}
		return false
	})
	a.Close()
	h1.WaitFor(c1, "offline", decktest.AgentState(agent.Agent.ID, "offline"))
	h1.Close() // one server per DB

	h := decktest.NewWith(t, func(o *server.Options) { o.DBPath = db; o.AgentGrace = time.Second })
	friend := h.ConnectClientAs(friendTok)
	h.WaitFor(friend, "member sees failed", decktest.TaskState(taskID, "failed"))
}
