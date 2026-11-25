package server_test

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/fakeagent"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

// manageAgent attaches a fake agent and, if features is non-nil, declares them in hello and waits until recorded.
func manageAgent(t *testing.T, features []string) (*decktest.Harness, string, *fakeagent.Agent) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	if features != nil {
		agent.Send(context.Background(), protocol.AgentMessage{Type: "hello", Kind: "Test", Features: features})
		for i := 0; ; i++ {
			var agents []protocol.Agent
			h.API("GET", "/api/agents", nil, &agents)
			if j := slices.IndexFunc(agents, func(x protocol.Agent) bool { return x.ID == a.Agent.ID }); j >= 0 && slices.Contains(agents[j].Features, protocol.FeatureDeclared) {
				break
			}
			if i > 100 {
				t.Fatal("hello features were not recorded")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	return h, a.Agent.ID, agent
}

type routineCall struct {
	method, path string
	body         any
	got          string // what the agent receives (fakeagent.Toggles)
}

func routineCalls(id string) []routineCall {
	base := "/api/agents/" + id + "/routines"
	return []routineCall{
		{"POST", base, map[string]string{"name": "Weather", "schedule": "every 2h", "prompt": "tell me"}, "Weather=created@every 2h"},
		{"POST", base + "/job1/toggle", map[string]string{"on": "false"}, "job1=off"},
		{"DELETE", base + "/job1", nil, "job1=removed"},
	}
}

func TestRoutineManageReachesAgent(t *testing.T) {
	for name, feats := range map[string][]string{
		"declared":   {protocol.FeatureRoutines, protocol.FeatureManageRoutines},
		"undeclared": nil, // older agents pass through as before
	} {
		t.Run(name, func(t *testing.T) {
			h, id, agent := manageAgent(t, feats)
			for _, c := range routineCalls(id) {
				if s := h.API(c.method, c.path, c.body, nil); s != http.StatusAccepted {
					t.Fatalf("%s %s: %d", c.method, c.path, s)
				}
				select {
				case got := <-agent.Toggles:
					if got != c.got {
						t.Fatalf("agent got %q, want %q", got, c.got)
					}
				case <-time.After(2 * time.Second):
					t.Fatalf("%s %s did not reach the agent", c.method, c.path)
				}
			}
		})
	}
}

func TestRoutineManageRefusedWhenDeclaredWithout(t *testing.T) {
	h, id, agent := manageAgent(t, []string{protocol.FeatureRoutines, protocol.FeatureRunRoutine})
	for _, c := range routineCalls(id) {
		var body struct{ Error string }
		if s := h.API(c.method, c.path, c.body, &body); s != http.StatusConflict {
			t.Fatalf("%s %s: %d, want 409", c.method, c.path, s)
		}
		if body.Error != "이 에이전트는 앱에서 루틴을 관리할 수 없어요" {
			t.Fatalf("reason %q", body.Error)
		}
	}
	select {
	case got := <-agent.Toggles:
		t.Fatalf("rejected, yet the agent received: %q", got)
	case <-time.After(300 * time.Millisecond):
	}
}
