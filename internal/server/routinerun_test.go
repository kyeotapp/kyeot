package server_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

func TestRunRoutineNeedsDeclaredFeature(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	c := h.ConnectClient()
	h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))

	if s := h.API("POST", "/api/agents/"+a.Agent.ID+"/routines/job1/run", nil, nil); s != http.StatusConflict {
		t.Fatalf("agent without routines.run should get 409: %d", s)
	}
	if s := h.API("POST", "/api/agents/nope/routines/job1/run", nil, nil); s != http.StatusNotFound {
		t.Fatalf("unknown agent: %d", s)
	}
}

func TestRunRoutineReachesDeclaredAgent(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	agent.Send(context.Background(), protocol.AgentMessage{Type: "hello", Kind: "Test", Features: []string{protocol.FeatureRoutines, protocol.FeatureRunRoutine}})

	path := "/api/agents/" + a.Agent.ID + "/routines/job1/run"
	s := 0
	for range 50 { // hello handling is async, so retry until the feature is recorded
		if s = h.API("POST", path, nil, nil); s == http.StatusAccepted {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if s != http.StatusAccepted {
		t.Fatalf("declared agent should accept run: %d", s)
	}
	if got := <-agent.Toggles; got != "job1=run" {
		t.Fatalf("agent got %q", got)
	}
}

func TestRefreshDataReachesDeclaredAgent(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	path := "/api/agents/" + a.Agent.ID + "/data/netwatch/refresh"
	if s := h.API("POST", path, nil, nil); s != http.StatusConflict {
		t.Fatalf("undeclared agent should get 409: %d", s)
	}
	agent.Send(context.Background(), protocol.AgentMessage{Type: "hello", Kind: "Test", Features: []string{protocol.FeatureDataRefresh}})
	s := 0
	for range 50 {
		if s = h.API("POST", path, nil, nil); s == http.StatusAccepted {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if s != http.StatusAccepted {
		t.Fatalf("declared agent should accept refresh: %d", s)
	}
	if got := <-agent.Toggles; got != "netwatch=refresh" {
		t.Fatalf("agent got %q", got)
	}
}
