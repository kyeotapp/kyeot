package hermesbridge_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

// waitApproval starts the downloads task and returns it with the id of its pending approval.
func waitApproval(t *testing.T, h *decktest.Harness, c *decktest.Client, agentID string) (protocol.Task, string) {
	t.Helper()
	var task protocol.Task
	if s := h.API("POST", "/api/tasks", map[string]string{"agentId": agentID, "text": "Tidy up the downloads folder"}, &task); s != http.StatusCreated {
		t.Fatalf("start: %d", s)
	}
	h.WaitFor(c, "agent blocked", decktest.AgentState(agentID, "blocked"))
	return task, pendingID(t, h)
}

func pendingID(t *testing.T, h *decktest.Harness) string {
	t.Helper()
	var pending []protocol.Event
	h.API("GET", "/api/approvals", nil, &pending)
	if len(pending) != 1 {
		t.Fatalf("pending: %d", len(pending))
	}
	var ap struct{ ID string }
	json.Unmarshal(pending[0].Payload, &ap)
	return ap.ID
}

func approvalSetup(t *testing.T, tweak func(*fakeHermes)) (*decktest.Harness, *decktest.Client, *fakeHermes, decktest.Created) {
	h := decktest.New(t)
	hermes, hermesURL := newFakeHermes(t)
	if tweak != nil {
		tweak(hermes)
	}
	c := h.ConnectClient()
	agent := startBridge(t, h, hermesURL, hermesKey)
	h.WaitFor(c, "bridge idle", decktest.AgentState(agent.Agent.ID, "idle"))
	return h, c, hermes, agent
}

func TestBridgeDenyReachesHermes(t *testing.T) {
	h, c, hermes, agent := approvalSetup(t, nil)
	task, id := waitApproval(t, h, c, agent.Agent.ID)
	h.API("POST", "/api/tasks/"+task.ID+"/approvals/"+id, map[string]string{"choice": "거절"}, nil)
	h.WaitFor(c, "denied reply", eventWith(task.ID, "message", "Did not delete"))
	h.WaitFor(c, "task done", decktest.TaskState(task.ID, "done"))
	_, approvals, _ := hermes.snapshot()
	if len(approvals) != 1 || approvals[0]["choice"] != "deny" || len(approvals[0]) != 3 {
		t.Fatalf("hermes approval: %v", approvals)
	}
}

func TestBridgeApprovesOnHermesWithoutGeneration(t *testing.T) {
	h, c, hermes, agent := approvalSetup(t, func(f *fakeHermes) { f.legacy = true })
	task, id := waitApproval(t, h, c, agent.Agent.ID)
	h.API("POST", "/api/tasks/"+task.ID+"/approvals/"+id, map[string]string{"choice": "한 번 허용"}, nil)
	h.WaitFor(c, "task done", decktest.TaskState(task.ID, "done"))
	_, approvals, _ := hermes.snapshot()
	if len(approvals) != 1 || approvals[0]["choice"] != "once" || approvals[0]["request_id"] != "req-1" {
		t.Fatalf("hermes approval: %v", approvals)
	}
	if _, ok := approvals[0]["execution_generation"]; ok {
		t.Fatalf("generation sent to old Hermes: %v", approvals[0])
	}
}

// A refused answer must not leave the task running with no way out.
func TestBridgeEndsTaskWhenHermesRefusesApproval(t *testing.T) {
	h, c, hermes, agent := approvalSetup(t, func(f *fakeHermes) { f.dropGen = true })
	task, id := waitApproval(t, h, c, agent.Agent.ID)
	h.API("POST", "/api/tasks/"+task.ID+"/approvals/"+id, map[string]string{"choice": "한 번 허용"}, nil)
	h.WaitFor(c, "explained", eventWith(task.ID, "message", "작업을 멈췄어요"))
	h.WaitFor(c, "task failed", decktest.TaskState(task.ID, "failed"))
	h.WaitFor(c, "bridge idle", decktest.AgentState(agent.Agent.ID, "idle"))
	if _, _, stops := hermes.snapshot(); len(stops) != 1 || stops[0] != "run_1" {
		t.Fatalf("stops: %v", stops)
	}
}

func TestBridgeEndsTaskOnStaleGeneration(t *testing.T) {
	h, c, hermes, agent := approvalSetup(t, func(f *fakeHermes) { f.stale = true })
	task, id := waitApproval(t, h, c, agent.Agent.ID)
	h.API("POST", "/api/tasks/"+task.ID+"/approvals/"+id, map[string]string{"choice": "한 번 허용"}, nil)
	h.WaitFor(c, "explained", eventWith(task.ID, "message", "지나간 실행"))
	h.WaitFor(c, "task failed", decktest.TaskState(task.ID, "failed"))
	if _, _, stops := hermes.snapshot(); len(stops) != 1 {
		t.Fatalf("stops: %v", stops)
	}
}

func TestBridgeAsksAgainAfterHermesHiccup(t *testing.T) {
	h, c, hermes, agent := approvalSetup(t, func(f *fakeHermes) { f.fail5xx = 1 })
	task, id := waitApproval(t, h, c, agent.Agent.ID)
	h.API("POST", "/api/tasks/"+task.ID+"/approvals/"+id, map[string]string{"choice": "한 번 허용"}, nil)
	h.WaitFor(c, "asked again", eventWith(task.ID, "message", "다시 골라"))
	h.WaitFor(c, "waiting again", decktest.TaskState(task.ID, "waiting"))
	again := pendingID(t, h)
	if again == id {
		t.Fatalf("same approval id offered again: %s", again)
	}
	h.API("POST", "/api/tasks/"+task.ID+"/approvals/"+again, map[string]string{"choice": "한 번 허용"}, nil)
	h.WaitFor(c, "final reply", eventWith(task.ID, "message", "Tidied up"))
	h.WaitFor(c, "task done", decktest.TaskState(task.ID, "done"))
	if _, _, stops := hermes.snapshot(); len(stops) != 0 {
		t.Fatalf("stops: %v", stops)
	}
}

func TestBridgeCancelWhileWaitingDeniesAndStops(t *testing.T) {
	h, c, hermes, agent := approvalSetup(t, nil)
	task, _ := waitApproval(t, h, c, agent.Agent.ID)
	h.API("POST", "/api/tasks/"+task.ID+"/cancel", nil, nil)
	h.WaitFor(c, "task cancelled", decktest.TaskState(task.ID, "cancelled"))
	h.WaitFor(c, "bridge idle", decktest.AgentState(agent.Agent.ID, "idle"))
	_, approvals, stops := hermes.snapshot()
	if len(approvals) != 1 || approvals[0]["choice"] != "deny" || len(stops) != 1 {
		t.Fatalf("approvals: %v stops: %v", approvals, stops)
	}
}

func TestBridgeMessageWhileWaitingStartsNewRun(t *testing.T) {
	h, c, hermes, agent := approvalSetup(t, nil)
	task, _ := waitApproval(t, h, c, agent.Agent.ID)
	h.API("POST", "/api/tasks/"+task.ID+"/messages", map[string]string{"text": "never mind, do something else"}, nil)
	h.WaitFor(c, "new run reply", eventWith(task.ID, "message", "Continuing replying"))
	h.WaitFor(c, "task done", decktest.TaskState(task.ID, "done"))
	sessions, approvals, stops := hermes.snapshot()
	if len(sessions) != 2 || len(approvals) != 1 || approvals[0]["choice"] != "deny" || len(stops) != 1 || stops[0] != "run_1" {
		t.Fatalf("sessions: %v approvals: %v stops: %v", sessions, approvals, stops)
	}
}

// Hermes denies by itself when the approval times out; the run then finishes and so must the task.
func TestBridgeEndsTaskWhenHermesTimesOutApproval(t *testing.T) {
	h, c, hermes, agent := approvalSetup(t, nil)
	task, _ := waitApproval(t, h, c, agent.Agent.ID)
	hermes.push("run_1", "run.completed", map[string]any{"output": "Approval timed out, nothing deleted"})
	h.WaitFor(c, "reply", eventWith(task.ID, "message", "nothing deleted"))
	h.WaitFor(c, "task done", decktest.TaskState(task.ID, "done"))
	h.WaitFor(c, "bridge idle", decktest.AgentState(agent.Agent.ID, "idle"))
	time.Sleep(100 * time.Millisecond)
	var pending []protocol.Event
	h.API("GET", "/api/approvals", nil, &pending)
	if len(pending) != 0 {
		t.Fatalf("approval still pending after the task ended: %d", len(pending))
	}
}
