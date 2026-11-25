package server_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
)

// If an agent omits icon or gives a name the app cannot draw, the server stores a title-matched icon and says so in the ack
func TestUIPutFitsIcon(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	put := func(spec string) string {
		agent.Send(ctx, map[string]any{"type": "ui.put", "screen": json.RawMessage(spec)})
		ack := recv(t, agent.Acks)
		if !okAck(ack) {
			t.Fatalf("must be stored: %+v", ack)
		}
		return ack.Reason
	}
	if r := put(`{"version":1,"id":"spend","title":"이번 달 비용","blocks":[{"type":"divider"}]}`); !strings.Contains(r, `"chart.bar"`) {
		t.Fatalf("must report that the empty icon was filled: %q", r)
	}
	if r := put(`{"version":1,"id":"lab","title":"테스트","icon":"testtube.2","blocks":[{"type":"divider"}]}`); !strings.Contains(r, `"testtube.2"`) {
		t.Fatalf("must report the changed name: %q", r)
	}
	if r := put(`{"version":1,"id":"mine","title":"비용","icon":"heart","blocks":[{"type":"divider"}]}`); strings.Contains(r, "icon") {
		t.Fatalf("a valid icon is left quiet: %q", r)
	}

	var ui uiState
	h.API("GET", "/api/agents/"+a.Agent.ID+"/ui", nil, &ui)
	got := map[string]string{}
	for _, s := range ui.Screens {
		var v struct{ Icon string }
		json.Unmarshal(s.Spec, &v)
		got[s.ID] = v.Icon
	}
	if got["spend"] != "chart.bar" || got["lab"] != "square.grid.2x2" || got["mine"] != "heart" {
		t.Fatalf("stored icons: %v", got)
	}
}
