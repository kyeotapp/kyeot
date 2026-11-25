package server_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
)

// Values that will look different in the app are saved, not rejected, and reported in the ui.ack reason.
func TestPutScreenWarnsWithoutRejecting(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	agent.Send(ctx, map[string]any{"type": "ui.put", "screen": json.RawMessage(`{"version":1,"id":"net","title":"Network","icon":"no.such.icon","blocks":[{"type":"countdown","label":"x","to":"next week"}]}`)})
	ack := recv(t, agent.Acks)
	if ack.OK == nil || !*ack.OK {
		t.Fatalf("warnings must not reject: %+v", ack)
	}
	if !strings.Contains(ack.Reason, `icon "no.such.icon"`) || !strings.Contains(ack.Reason, "countdown.to") {
		t.Fatalf("reason: %q", ack.Reason)
	}
	var ui struct {
		Screens []struct{ ID string } `json:"screens"`
	}
	h.API("GET", "/api/agents/"+a.Agent.ID+"/ui", nil, &ui)
	if !slices.ContainsFunc(ui.Screens, func(s struct{ ID string }) bool { return s.ID == "net" }) {
		t.Fatalf("screen not saved: %+v", ui.Screens)
	}
}
