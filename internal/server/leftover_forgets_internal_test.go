package server

import (
	"context"
	"testing"

	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/store"
)

// After a restart, leftover pending approvals are closed after grace only if the reattached agent is the in-server adapter (forgets).
func TestLeftoverWaitingClosesOnlyForForgetfulAgent(t *testing.T) {
	srv, err := New(Options{DBPath: ":memory:", OwnerToken: "test-owner-token-0123456789", UploadDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	h := srv.hub
	waiting := func(name string) (string, string) {
		a, _, err := srv.store.CreateAgent(store.Owner, name)
		if err != nil {
			t.Fatal(err)
		}
		task, err := srv.store.CreateTask(store.Owner, a.ID, "x", store.NewTask{})
		if err != nil {
			t.Fatal(err)
		}
		h.mu.Lock()
		if _, err := h.recordState(store.Owner, task.ID, "agent", "approval", map[string]any{"id": "ap1", "question": "?"}, protocol.TaskWaiting); err != nil {
			t.Fatal(err)
		}
		h.agents[a.ID] = &peer{} // reattached
		if h.live == nil {
			h.live = map[string]string{}
		}
		h.live[a.ID] = protocol.AgentIdle
		h.mu.Unlock()
		return a.ID, task.ID
	}
	_, kept := waiting("outside")
	local, closed := waiting("inside")
	h.agentForgets(local)

	h.mu.Lock()
	h.left = map[string]int64{kept: srv.store.LastEventID(kept), closed: srv.store.LastEventID(closed)}
	h.leftDue = true
	h.settleLeftovers("")
	for id := range h.agents {
		delete(h.agents, id)
	}
	h.mu.Unlock()

	for id, want := range map[string]string{kept: protocol.TaskWaiting, closed: protocol.TaskFailed} {
		got, err := srv.store.Task(id)
		if err != nil || got.State != want {
			t.Fatalf("%s: %s %v, want %s", id, got.State, err, want)
		}
	}
}

// Starting an in-server adapter marks that agent as forgets.
func TestStartLocalMarksAgentForgetful(t *testing.T) {
	srv, err := New(Options{DBPath: ":memory:", OwnerToken: "test-owner-token-0123456789", UploadDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	a, token, err := srv.store.CreateAgent(store.Owner, "Hermes")
	if err != nil {
		t.Fatal(err)
	}
	srv.local.mu.Lock()
	srv.local.conns = map[string]*localConn{}
	err = srv.startLocal(LocalHermes{ID: "default", Start: func(context.Context, string) {}}, token)
	srv.local.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	srv.hub.mu.Lock()
	defer srv.hub.mu.Unlock()
	if !srv.hub.forgets[a.ID] {
		t.Fatal("the in-server adapter was not marked as forgets")
	}
}
