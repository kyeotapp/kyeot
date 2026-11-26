package hermesbridge_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/hermesbridge"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

func runBridge(cfg hermesbridge.Config) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { hermesbridge.New(cfg).Run(ctx); close(done) }()
	return func() { cancel(); <-done }
}

// The task outlives a bridge restart; the second approval in it must still reach Hermes.
func TestApprovalAfterBridgeRestartReachesHermes(t *testing.T) {
	h := decktest.New(t)
	c := h.ConnectClient()
	agent := h.CreateAgent("Agent A")
	_, url1 := newFakeHermes(t)
	stop1 := runBridge(hermesbridge.Config{DeckURL: h.URL, AgentToken: agent.Token, HermesURL: url1, HermesKey: hermesKey})
	h.WaitFor(c, "bridge idle", decktest.AgentState(agent.Agent.ID, "idle"))
	var task protocol.Task
	if s := h.API("POST", "/api/tasks", map[string]string{"agentId": agent.Agent.ID, "text": "Tidy up the downloads folder"}, &task); s != http.StatusCreated {
		t.Fatalf("start: %d", s)
	}
	h.WaitFor(c, "waiting", decktest.TaskState(task.ID, "waiting"))
	first := pendingID(t, h)
	h.API("POST", "/api/tasks/"+task.ID+"/approvals/"+first, map[string]string{"choice": "한 번 허용"}, nil)
	h.WaitFor(c, "task done", decktest.TaskState(task.ID, "done"))
	stop1()
	h.WaitFor(c, "bridge offline", decktest.AgentState(agent.Agent.ID, "offline"))

	hermes2, url2 := newFakeHermes(t) // a fresh Hermes run asks for approval again
	stop2 := runBridge(hermesbridge.Config{DeckURL: h.URL, AgentToken: agent.Token, HermesURL: url2, HermesKey: hermesKey})
	defer stop2()
	h.WaitFor(c, "bridge back", decktest.AgentState(agent.Agent.ID, "idle"))
	h.API("POST", "/api/tasks/"+task.ID+"/messages", map[string]string{"text": "Tidy up the downloads folder again"}, nil)
	h.WaitFor(c, "waiting again", decktest.TaskState(task.ID, "waiting"))
	second := pendingID(t, h)
	if second == first {
		t.Fatalf("approval id reused across restart: %s", second)
	}
	if s := h.API("POST", "/api/tasks/"+task.ID+"/approvals/"+second, map[string]string{"choice": "한 번 허용"}, nil); s != http.StatusNoContent {
		t.Fatalf("second answer: %d", s)
	}
	h.WaitFor(c, "done again", decktest.TaskState(task.ID, "done"))
	if _, approvals, _ := hermes2.snapshot(); len(approvals) != 1 || approvals[0]["choice"] != "once" {
		t.Fatalf("hermes approvals after restart: %v", approvals)
	}
}
