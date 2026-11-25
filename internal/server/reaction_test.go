package server_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

// A reaction with no user message to react to, or a too-long emoji, is dropped, but the agent is told why.
func TestAgentReactionDropTellsAgent(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))

	openTask(agent, "t-react")
	agent.Send(context.Background(), protocol.AgentMessage{Type: "reaction", Task: "t-react", Emoji: "👍"})
	select {
	case got := <-agent.Errors:
		if !strings.Contains(got, "no user message") {
			t.Fatalf("error: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dropped reaction must tell the agent")
	}
}
