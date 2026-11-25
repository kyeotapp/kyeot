package hermesbridge_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/hermesbridge"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

func TestBridgeDeclaresRoutineManage(t *testing.T) {
	h := decktest.New(t)
	c := h.ConnectClient()
	_, hermesURL := newFakeHermes(t)
	agent := startBridgeWith(t, h, hermesbridge.Config{HermesURL: hermesURL, HermesKey: hermesKey, HermesHome: t.TempDir()})
	id := agent.Agent.ID
	h.WaitFor(c, "bridge idle", decktest.AgentState(id, "idle"))
	waitUntil(t, "routines.manage declared in hello", func() bool {
		var agents []protocol.Agent
		h.API("GET", "/api/agents", nil, &agents)
		i := slices.IndexFunc(agents, func(a protocol.Agent) bool { return a.ID == id })
		return i >= 0 && slices.Contains(agents[i].Features, protocol.FeatureManageRoutines)
	})
	if s := h.API("POST", "/api/agents/"+id+"/routines/job1/toggle", map[string]string{"on": "false"}, nil); s != http.StatusAccepted {
		t.Fatalf("toggle: %d", s)
	}
}
