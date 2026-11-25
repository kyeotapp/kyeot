package server_test

import (
	"net/http"
	"testing"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

func TestOwnerRotatesAgentTokenAndDropsOldConnection(t *testing.T) {
	h := decktest.New(t)
	c := h.ConnectClient()
	created := h.CreateAgent("test")
	id := created.Agent.ID
	state := func(want string) func(protocol.ClientMessage) bool {
		return func(m protocol.ClientMessage) bool {
			return m.Type == "agent" && m.Agent.ID == id && m.Agent.State == want
		}
	}
	h.StartFakeAgent(created.Token)
	h.WaitFor(c, "agent online", state(protocol.AgentIdle))

	var res struct{ Token string }
	if s := h.API("POST", "/api/agents/"+id+"/token", nil, &res); s != http.StatusOK || res.Token == "" || res.Token == created.Token {
		t.Fatalf("rotate: %d", s)
	}
	h.WaitFor(c, "old connection dropped", state(protocol.AgentOffline))

	if s := h.APIAs("not-the-owner-token-000000", "POST", "/api/agents/"+id+"/token", nil, nil); s != http.StatusUnauthorized {
		t.Fatalf("non-owner: %d", s)
	}
	if s := h.API("POST", "/api/agents/nope/token", nil, nil); s != http.StatusNotFound {
		t.Fatalf("unknown agent: %d", s)
	}

	h.StartFakeAgent(res.Token)
	h.WaitFor(c, "new token connects", state(protocol.AgentIdle))
}
