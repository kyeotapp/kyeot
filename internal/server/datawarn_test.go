package server_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
)

// When an agent with a template tab uploads a bundle with wrong keys, it is stored and the ack reason says which path
func TestDataPutWarnsWhenShapeMissesScreen(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))

	spec := json.RawMessage(`{"version":1,"id":"habits","title":"Habits","icon":"checkmark.seal","blocks":[{"type":"stat","label":"Streak","value":{"$data":"habits.streak"}},{"type":"list","items":{"$data":"habits.today"}}]}`)
	h.API("POST", "/api/agents/"+a.Agent.ID+"/ui/screens", spec, nil)

	ack := putData(t, agent, agent.Acks, "habits", map[string]any{"streak": 3, "today": []map[string]string{{"name": "Water"}}})
	if !okAck(ack) || !strings.Contains(ack.Reason, "`habits.today.0`") || !strings.Contains(ack.Reason, `"name"`) {
		t.Fatalf("a wrong shape must be stored and the path reported: %+v", ack)
	}
	var ui uiState
	h.API("GET", "/api/agents/"+a.Agent.ID+"/ui", nil, &ui)
	if !strings.Contains(string(ui.Data["habits"].Value), `"name"`) {
		t.Fatalf("must warn but still store: %s", ui.Data["habits"].Value)
	}
	if w := ui.Data["habits"].Warnings; len(w) != 1 || w[0].Path != "habits.today" || !strings.Contains(w[0].Reason, "`habits.today.0`") || strings.Contains(w[0].Reason, "탭:") {
		t.Fatalf("the bundle the app receives must carry the warning as path and reason: %+v", w)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	agent.Send(ctx, map[string]any{"type": "ui.list"})
	var st uiState
	json.Unmarshal(recv(t, agent.States).UI, &st)
	if len(st.Data["habits"].Warnings) != 1 {
		t.Fatalf("same warning in ui.state too: %+v", st.Data["habits"])
	}

	if ack := putData(t, agent, agent.Acks, "habits", map[string]any{"streak": 3, "today": []map[string]string{{"title": "Water"}}}); !okAck(ack) || ack.Reason != "" {
		t.Fatalf("a correct shape must be quiet: %+v", ack)
	}
	if ack := putData(t, agent, agent.Acks, "other", map[string]any{"x": "y"}); !okAck(ack) || ack.Reason != "" {
		t.Fatalf("a bundle the design does not use must be quiet: %+v", ack)
	}
	ui = uiState{}
	h.API("GET", "/api/agents/"+a.Agent.ID+"/ui", nil, &ui)
	if ui.Data["habits"].Warnings != nil || ui.Data["other"].Warnings != nil {
		t.Fatalf("re-uploading correctly clears the old warning, and an unused bundle has none: %+v", ui.Data)
	}
	var raw map[string]map[string]map[string]json.RawMessage
	h.API("GET", "/api/agents/"+a.Agent.ID+"/ui", nil, &raw)
	if _, ok := raw["data"]["habits"]["warnings"]; ok {
		t.Fatalf("with no warning the key is not sent: %s", raw["data"]["habits"])
	}
}
