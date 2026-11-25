package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/kyeotapp/kyeot/internal/uicatalog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/fakeagent"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

type uiState struct {
	Screens []struct {
		ID      string          `json:"id"`
		Version int             `json:"version"`
		Spec    json.RawMessage `json:"spec"`
	} `json:"screens"`
	Tabs   []string              `json:"tabs"`
	Hidden []string              `json:"hidden"`
	Locked []string              `json:"locked"`
	Data   map[string]dataBundle `json:"data"`
	Order  []string              `json:"order"`
}

type dataBundle struct {
	Value     json.RawMessage        `json:"value"`
	UpdatedAt int64                  `json:"updatedAt"`
	Warnings  []protocol.DataWarning `json:"warnings"`
}

func recv(t *testing.T, ch chan protocol.ServerMessage) protocol.ServerMessage {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("the agent did not get a response")
		return protocol.ServerMessage{}
	}
}

func TestNewAgentGetsStarterTabs(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	var ui uiState
	if s := h.API("GET", "/api/agents/"+a.Agent.ID+"/ui", nil, &ui); s != http.StatusOK {
		t.Fatalf("status %d", s)
	}
	if len(ui.Screens) != 0 || ui.Tabs == nil || len(ui.Tabs) != 0 {
		t.Fatalf("a new agent starts with Home only: %+v", ui)
	}
}

func TestAgentPutsScreen(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	withStarters(t, h, a.Agent.ID)
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	spec := json.RawMessage(`{"version":1,"id":"revenue","title":"Revenue","icon":"chart.bar","blocks":[{"type":"stat","label":"This month","value":1}]}`)
	agent.Send(ctx, map[string]any{"type": "ui.put", "screen": spec})
	if ack := recv(t, agent.Acks); ack.OK == nil || !*ack.OK || ack.ID != "revenue" {
		t.Fatalf("ack %+v", ack)
	}
	h.WaitFor(c, "ui", func(m protocol.ClientMessage) bool { return m.Type == "ui" && m.AgentID == a.Agent.ID })

	var ui uiState
	h.API("GET", "/api/agents/"+a.Agent.ID+"/ui", nil, &ui)
	if len(ui.Screens) != 4 || len(ui.Tabs) != 4 || ui.Tabs[3] != "revenue" {
		t.Fatalf("must attach in the fourth slot after the template: %+v", ui.Tabs)
	}

	agent.Send(ctx, map[string]any{"type": "ui.put", "screen": json.RawMessage(`{"version":1,"id":"x","title":"t","icon":"i","blocks":[{"type":"map"}]}`)})
	if ack := recv(t, agent.Acks); ack.OK == nil || *ack.OK || ack.Reason == "" {
		t.Fatalf("an unknown part is rejected with a reason: %+v", ack)
	}

	agent.Send(ctx, map[string]any{"type": "ui.put", "screen": json.RawMessage(`{"version":1,"id":"extra","title":"t","icon":"i","blocks":[{"type":"divider"}]}`)})
	recv(t, agent.Acks)
	h.API("GET", "/api/agents/"+a.Agent.ID+"/ui", nil, &ui)
	if len(ui.Tabs) != 4 || len(ui.Screens) != 5 {
		t.Fatalf("at most 4 tab slots; beyond that only the screen is created: %v", ui.Tabs)
	}

	agent.Send(ctx, map[string]any{"type": "ui.layout", "tabs": []string{"revenue", "nope"}})
	if ack := recv(t, agent.Acks); ack.OK == nil || *ack.OK {
		t.Fatal("putting a missing screen in tabs is rejected")
	}

	agent.Send(ctx, map[string]any{"type": "ui.remove", "id": "revenue"})
	if ack := recv(t, agent.Acks); ack.OK == nil || !*ack.OK || ack.ID != "revenue" {
		t.Fatalf("delete ack %+v", ack)
	}

	agent.Send(ctx, map[string]any{"type": "ui.list"})
	st := recv(t, agent.States)
	var got uiState
	if st.Type != "ui.state" || json.Unmarshal(st.UI, &got) != nil {
		t.Fatalf("ui.state %+v", st)
	}
	ids := []string{}
	for _, s := range got.Screens {
		ids = append(ids, s.ID)
	}
	if !slices.Contains(ids, "extra") || slices.Contains(ids, "revenue") || slices.Contains(got.Tabs, "revenue") {
		t.Fatalf("the screens the agent reads match the owner's: %v %v", ids, got.Tabs)
	}
}

func TestAgentListBeforePutGetsStarters(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	agent := h.StartFakeAgent(a.Token)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	agent.Send(ctx, map[string]any{"type": "ui.list"})
	var got uiState
	json.Unmarshal(recv(t, agent.States).UI, &got)
	if len(got.Screens) != 0 || len(got.Tabs) != 0 {
		t.Fatalf("no screens at first: %+v", got)
	}
}

func TestOwnerManagesScreens(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	withStarters(t, h, a.Agent.ID)
	other := h.CreateAgent("Other")
	base := "/api/agents/" + a.Agent.ID + "/ui"
	h.API("GET", base, nil, &uiState{})
	if s := h.API("POST", base+"/screens", json.RawMessage(`{"version":1,"id":"today","title":"Today","icon":"sun.max","blocks":[{"type":"divider"}]}`), nil); s != http.StatusOK {
		t.Fatalf("template add %d", s)
	}
	if s := h.API("POST", base+"/screens", json.RawMessage(`{"version":1,"id":"bad","title":"t","icon":"i","blocks":[{"type":"map"}]}`), nil); s != http.StatusBadRequest {
		t.Fatalf("validation failure gives 400 %d", s)
	}
	if s := h.API("PUT", base+"/tabs", map[string]any{"tabs": []string{"today", "costs"}}, nil); s != http.StatusNoContent {
		t.Fatalf("tab save %d", s)
	}
	h.API("POST", base+"/screens", json.RawMessage(`{"version":1,"id":"week","title":"Weekly","icon":"calendar","blocks":[{"type":"divider"}]}`), nil)
	if s := h.API("PUT", base+"/tabs", map[string]any{"tabs": []string{"today", "costs", "rooms", "routines", "week"}}, nil); s != http.StatusBadRequest {
		t.Fatalf("5 slots rejected %d", s)
	}
	h.API("POST", base+"/screens", json.RawMessage(`{"version":1,"id":"today","title":"Today2","icon":"sun.max","blocks":[{"type":"divider"}]}`), nil)
	var versions []struct {
		Version int `json:"version"`
	}
	if s := h.API("GET", base+"/screens/today/versions", nil, &versions); s != http.StatusOK || len(versions) != 2 || versions[0].Version != 2 {
		t.Fatalf("revision list is newest first %d %+v", s, versions)
	}
	if s := h.API("POST", base+"/screens/today/revert", map[string]int{"version": 1}, nil); s != http.StatusOK {
		t.Fatalf("revert %d", s)
	}
	if s := h.API("POST", base+"/screens/today/revert", map[string]int{"version": 99}, nil); s != http.StatusNotFound {
		t.Fatalf("missing revision %d", s)
	}
	var ui uiState
	h.API("GET", base, nil, &ui)
	found := false
	for _, sc := range ui.Screens {
		if sc.ID == "today" {
			found = true
			if sc.Version != 3 || !bytes.Contains(sc.Spec, []byte(`"Today"`)) {
				t.Fatalf("reverting lifts revision 1's content (title Today) to revision 3: %+v", sc)
			}
		}
	}
	if !found {
		t.Fatal("no today screen")
	}
	if s := h.API("DELETE", base+"/screens/today", nil, nil); s != http.StatusNoContent {
		t.Fatalf("delete %d", s)
	}
	ui = uiState{}
	h.API("GET", base, nil, &ui)
	if slices.Contains(ui.Tabs, "today") {
		t.Fatal("a deleted screen also leaves the tabs")
	}
	if s := h.API("GET", "/api/agents/"+other.Agent.ID+"/ui/screens/today/versions", nil, nil); s != http.StatusOK {
		t.Fatalf("another agent gets an empty list %d", s)
	}
}

func TestUIRoutesNeedOwnedAgent(t *testing.T) {
	h := decktest.New(t)
	base := "/api/agents/nope/ui"
	spec := json.RawMessage(`{"version":1,"id":"today","title":"Today","icon":"sun.max","blocks":[{"type":"divider"}]}`)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", base, nil},
		{"POST", base + "/screens", spec},
		{"DELETE", base + "/screens/today", nil},
		{"PUT", base + "/tabs", map[string]any{"tabs": []string{}}},
		{"PUT", base + "/order", map[string]any{"order": []string{}}},
		{"GET", base + "/screens/today/versions", nil},
		{"DELETE", base + "/data/revenue", nil},
		{"POST", base + "/screens/today/revert", map[string]int{"version": 1}},
	} {
		if s := h.API(c.method, c.path, c.body, nil); s != http.StatusNotFound {
			t.Errorf("%s %s: missing agent gives 404, got %d", c.method, c.path, s)
		}
		if s := h.APIAs("wrong-token", c.method, c.path, c.body, nil); s != http.StatusUnauthorized {
			t.Errorf("%s %s: wrong token gives 401, got %d", c.method, c.path, s)
		}
	}
}

func TestCatalogIsPublic(t *testing.T) {
	h := decktest.New(t)
	if s := h.APIAs("", "GET", "/ui/catalog", nil, nil); s != http.StatusOK {
		t.Fatalf("catalog %d", s)
	}
}

func putData(t *testing.T, agent interface {
	Send(context.Context, any) error
}, acks chan protocol.ServerMessage, name string, data any) protocol.ServerMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	agent.Send(ctx, map[string]any{"type": "data.put", "name": name, "data": data})
	return recv(t, acks)
}

func okAck(m protocol.ServerMessage) bool { return m.Type == "ui.ack" && m.OK != nil && *m.OK }

func TestAgentPutsData(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))

	if ack := putData(t, agent, agent.Acks, "revenue", map[string]any{"month": map[string]int{"total": 1200000}}); !okAck(ack) || ack.ID != "revenue" {
		t.Fatalf("ack %+v", ack)
	}
	h.WaitFor(c, "ui", func(m protocol.ClientMessage) bool { return m.Type == "ui" && m.AgentID == a.Agent.ID })

	var ui uiState
	h.API("GET", "/api/agents/"+a.Agent.ID+"/ui", nil, &ui)
	if b := ui.Data["revenue"]; string(b.Value) != `{"month":{"total":1200000}}` || b.UpdatedAt == 0 || len(ui.Screens) != 0 {
		t.Fatalf("data in the owner response: %+v", ui)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	agent.Send(ctx, map[string]any{"type": "ui.list"})
	var got uiState
	json.Unmarshal(recv(t, agent.States).UI, &got)
	if string(got.Data["revenue"].Value) != `{"month":{"total":1200000}}` {
		t.Fatalf("data in ui.state: %+v", got)
	}

	for name, data := range map[string]any{
		"deck":                              map[string]int{"x": 1},
		"Revenue":                           map[string]int{"x": 1},
		"-revenue":                          map[string]int{"x": 1},
		"big":                               map[string]string{"s": strings.Repeat("x", 262144)},
		"long-name-is-thirty-three-chars-x": map[string]int{"x": 1},
	} {
		if ack := putData(t, agent, agent.Acks, name, data); ack.Type != "ui.ack" || okAck(ack) || ack.Reason == "" || ack.ID != name {
			t.Errorf("%s: rejected with a reason %+v", name, ack)
		}
	}
	if ack := putData(t, agent, agent.Acks, "edge", map[string]string{"s": strings.Repeat("x", 262144-8)}); !okAck(ack) {
		t.Fatalf("exactly 256KB passes %+v", ack)
	}

	agent.Send(ctx, map[string]any{"type": "data.put", "name": "arr", "data": []int{1}})
	select {
	case <-agent.Errors:
	case <-time.After(2 * time.Second):
		t.Fatal("non-object data is a format error")
	}

	agent.Send(ctx, map[string]any{"type": "data.remove", "name": "revenue"})
	if ack := recv(t, agent.Acks); !okAck(ack) || ack.ID != "revenue" {
		t.Fatalf("delete ack %+v", ack)
	}
	agent.Send(ctx, map[string]any{"type": "data.remove", "name": "revenue"})
	if ack := recv(t, agent.Acks); okAck(ack) || ack.Reason == "" {
		t.Fatalf("deleting a missing bundle is rejected %+v", ack)
	}
	ui = uiState{}
	h.API("GET", "/api/agents/"+a.Agent.ID+"/ui", nil, &ui)
	if _, ok := ui.Data["revenue"]; ok || ui.Data == nil {
		t.Fatalf("a deleted bundle is gone and data stays as an empty object: %+v", ui.Data)
	}
}

func TestDataBundleCount(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	agent := h.StartFakeAgent(a.Token)
	for i := range 20 {
		if ack := putData(t, agent, agent.Acks, fmt.Sprintf("b%d", i), map[string]int{"i": i}); !okAck(ack) {
			t.Fatalf("b%d: %+v", i, ack)
		}
	}
	if ack := putData(t, agent, agent.Acks, "b20", map[string]int{"i": 20}); okAck(ack) || !strings.Contains(ack.Reason, "20") {
		t.Fatalf("the 21st is rejected %+v", ack)
	}
	if ack := putData(t, agent, agent.Acks, "b0", map[string]int{"i": 99}); !okAck(ack) {
		t.Fatalf("replacing an existing bundle is unaffected by the count %+v", ack)
	}
}

func TestOwnerRemovesData(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	agent := h.StartFakeAgent(a.Token)
	putData(t, agent, agent.Acks, "revenue", map[string]int{"x": 1})
	base := "/api/agents/" + a.Agent.ID + "/ui/data/"
	if s := h.API("DELETE", base+"revenue", nil, nil); s != http.StatusNoContent {
		t.Fatalf("delete %d", s)
	}
	if s := h.API("DELETE", base+"revenue", nil, nil); s != http.StatusNotFound {
		t.Fatalf("missing bundle %d", s)
	}
	var ui uiState
	h.API("GET", "/api/agents/"+a.Agent.ID+"/ui", nil, &ui)
	if len(ui.Data) != 0 {
		t.Fatalf("data %+v", ui.Data)
	}
}

func TestFakeAgentPutsBundlesOnConnect(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	c := h.ConnectClient()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agent, err := fakeagent.Dial(ctx, h.URL, a.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	agent.Bundles = map[string]json.RawMessage{"revenue": json.RawMessage(`{"month":{"total":7}}`)}
	go agent.Run(ctx)
	h.WaitFor(c, "ui", func(m protocol.ClientMessage) bool { return m.Type == "ui" && m.AgentID == a.Agent.ID })
	var ui uiState
	h.API("GET", "/api/agents/"+a.Agent.ID+"/ui", nil, &ui)
	if string(ui.Data["revenue"].Value) != `{"month":{"total":7}}` {
		t.Fatalf("bundle uploaded on connect: %+v", ui.Data)
	}
}

func TestHiddenScreens(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	withStarters(t, h, a.Agent.ID)
	base := "/api/agents/" + a.Agent.ID + "/ui"
	get := func() uiState {
		var ui uiState
		h.API("GET", base, nil, &ui)
		return ui
	}
	if s := h.API("PUT", base+"/tabs", map[string]any{"tabs": []string{"rooms"}}, nil); s != http.StatusNoContent {
		t.Fatalf("tabs %d", s)
	}
	if s := h.API("PUT", base+"/hidden", map[string]any{"hidden": []string{"costs"}}, nil); s != http.StatusNoContent {
		t.Fatalf("hidden %d", s)
	}
	if ui := get(); !slices.Equal(ui.Hidden, []string{"costs"}) {
		t.Fatalf("costs in hidden: %+v", ui.Hidden)
	}
	if s := h.API("PUT", base+"/hidden", map[string]any{"hidden": []string{"rooms"}}, nil); s != http.StatusBadRequest {
		t.Fatalf("a screen in the tab bar must not be hideable: %d", s)
	}
	if s := h.API("PUT", base+"/hidden", map[string]any{"hidden": []string{"nope"}}, nil); s != http.StatusBadRequest {
		t.Fatalf("a missing screen must not be hideable: %d", s)
	}

	// Re-uploading with only a rename keeps hidden and off-tab positions
	var spec json.RawMessage
	for _, sc := range get().Screens {
		if sc.ID == "costs" {
			spec = sc.Spec
		}
	}
	var m map[string]any
	json.Unmarshal(spec, &m)
	m["title"] = "My costs"
	if s := h.API("POST", base+"/screens", m, nil); s != http.StatusOK && s != http.StatusCreated {
		t.Fatalf("rename %d", s)
	}
	if ui := get(); !slices.Equal(ui.Tabs, []string{"rooms"}) || !slices.Equal(ui.Hidden, []string{"costs"}) {
		t.Fatalf("the renamed screen moved: tabs %v hidden %v", ui.Tabs, ui.Hidden)
	}

	// Putting it in a tab removes it from hidden
	h.API("PUT", base+"/tabs", map[string]any{"tabs": []string{"rooms", "costs"}}, nil)
	if ui := get(); len(ui.Hidden) != 0 {
		t.Fatalf("a screen put in a tab must leave hidden: %v", ui.Hidden)
	}

	// Deleting also removes it from hidden
	h.API("PUT", base+"/hidden", map[string]any{"hidden": []string{"routines"}}, nil)
	if s := h.API("DELETE", base+"/screens/routines", nil, nil); s != http.StatusNoContent {
		t.Fatalf("remove %d", s)
	}
	if ui := get(); len(ui.Hidden) != 0 {
		t.Fatalf("a deleted screen must leave hidden: %v", ui.Hidden)
	}
}

func TestOffOrder(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	withStarters(t, h, a.Agent.ID)
	base := "/api/agents/" + a.Agent.ID + "/ui"
	get := func() uiState {
		var ui uiState
		h.API("GET", base, nil, &ui)
		return ui
	}
	put := func(body any) int { return h.API("PUT", base+"/order", body, nil) }
	h.API("PUT", base+"/tabs", map[string]any{"tabs": []string{"rooms"}}, nil)
	ui := get()
	if ui.Order == nil || len(ui.Order) != 0 {
		t.Fatalf("the initial order is an empty list: %#v", ui.Order)
	}
	var off []string
	for _, sc := range ui.Screens {
		if !slices.Contains(ui.Tabs, sc.ID) {
			off = append(off, sc.ID)
		}
	}
	if len(off) < 2 {
		t.Fatalf("at least two off-tab screens needed: %v", off)
	}
	x, y := off[0], off[1]
	if s := put(map[string]any{"order": []string{y, x}}); s != http.StatusNoContent {
		t.Fatalf("order %d", s)
	}
	if got := get().Order; !slices.Equal(got, []string{y, x}) {
		t.Fatalf("order %v", got)
	}
	for _, bad := range []any{map[string]any{"order": []string{"nope"}}, map[string]any{"order": []string{x, x}}, map[string]any{}} {
		if s := put(bad); s != http.StatusBadRequest {
			t.Fatalf("%v should be 400: %d", bad, s)
		}
	}
	if got := get().Order; !slices.Equal(got, []string{y, x}) {
		t.Fatalf("a rejected request changed the order: %v", got)
	}

	// Deleting also removes it from the order
	if s := h.API("DELETE", base+"/screens/"+y, nil, nil); s != http.StatusNoContent {
		t.Fatalf("remove %d", s)
	}
	if got := get().Order; !slices.Equal(got, []string{x}) {
		t.Fatalf("a deleted screen remained in the order: %v", got)
	}

	// An empty list returns to server order
	if s := put(map[string]any{"order": []string{}}); s != http.StatusNoContent {
		t.Fatalf("empty order %d", s)
	}
	if got := get().Order; len(got) != 0 {
		t.Fatalf("empty order %v", got)
	}
}

func TestLockedScreensCannotBeRemoved(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	withStarters(t, h, a.Agent.ID)
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	base := "/api/agents/" + a.Agent.ID + "/ui"
	var ui uiState
	h.API("GET", base, nil, &ui)
	if s := h.API("PUT", base+"/locked", map[string]any{"locked": []string{"costs"}}, nil); s != http.StatusNoContent {
		t.Fatalf("lock %d", s)
	}
	if s := h.API("DELETE", base+"/screens/costs", nil, nil); s != http.StatusConflict {
		t.Fatalf("a locked screen must not be deletable by the user: %d", s)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	agent.Send(ctx, map[string]any{"type": "ui.remove", "id": "costs"})
	if ack := recv(t, agent.Acks); ack.OK == nil || *ack.OK || ack.Reason == "" {
		t.Fatalf("a locked screen must not be deletable by the agent: %+v", ack)
	}
	h.API("GET", base, nil, &ui)
	if !slices.Equal(ui.Locked, []string{"costs"}) || !slices.ContainsFunc(ui.Screens, func(s struct {
		ID      string          `json:"id"`
		Version int             `json:"version"`
		Spec    json.RawMessage `json:"spec"`
	}) bool {
		return s.ID == "costs"
	}) {
		t.Fatalf("costs must stay locked: %+v", ui.Locked)
	}
	h.API("PUT", base+"/locked", map[string]any{"locked": []string{}}, nil)
	if s := h.API("DELETE", base+"/screens/costs", nil, nil); s != http.StatusNoContent {
		t.Fatalf("once unlocked it must be deletable: %d", s)
	}
}

// withStarters adds the room, routine, and cost templates like the old defaults and puts them in tabs in that order.
func withStarters(t *testing.T, h *decktest.Harness, agentID string) {
	t.Helper()
	base := "/api/agents/" + agentID + "/ui"
	var ui uiState
	h.API("GET", base, nil, &ui)
	for _, s := range uicatalog.Starters() {
		var spec map[string]any
		json.Unmarshal(s.Raw, &spec)
		if st := h.API("POST", base+"/screens", spec, nil); st != http.StatusOK && st != http.StatusCreated {
			t.Fatalf("put %s %d", s.ID, st)
		}
	}
	if st := h.API("PUT", base+"/tabs", map[string]any{"tabs": []string{"rooms", "routines", "costs"}}, nil); st != http.StatusNoContent {
		t.Fatalf("tabs %d", st)
	}
}

func TestPutScreenPlace(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	put := func(id, place string) protocol.ServerMessage {
		t.Helper()
		m := map[string]any{"type": "ui.put", "screen": json.RawMessage(`{"version":1,"id":"` + id + `","title":"t","icon":"star","blocks":[{"type":"divider"}]}`)}
		if place != "" {
			m["place"] = place
		}
		agent.Send(ctx, m)
		ack := recv(t, agent.Acks)
		if ack.OK == nil || !*ack.OK {
			t.Fatalf("%s %s: %+v", id, place, ack)
		}
		return ack
	}
	state := func() uiState {
		var ui uiState
		h.API("GET", "/api/agents/"+a.Agent.ID+"/ui", nil, &ui)
		return ui
	}

	put("a", "")
	put("b", "hidden")
	put("c", "off")
	ui := state()
	if !slices.Equal(ui.Tabs, []string{"a"}) || !slices.Equal(ui.Hidden, []string{"b"}) {
		t.Fatalf("omitted goes at the tab end, hidden is hidden, off is nowhere: %v %v", ui.Tabs, ui.Hidden)
	}

	put("a", "")
	if ui = state(); !slices.Contains(ui.Tabs, "a") {
		t.Fatalf("re-uploading without place keeps the position: %v", ui.Tabs)
	}
	put("a", "hidden")
	if ui = state(); slices.Contains(ui.Tabs, "a") || !slices.Contains(ui.Hidden, "a") {
		t.Fatalf("an existing screen moves when given place: %v %v", ui.Tabs, ui.Hidden)
	}
	put("b", "tab")
	if ui = state(); !slices.Contains(ui.Tabs, "b") || slices.Contains(ui.Hidden, "b") {
		t.Fatalf("tab takes it out of hidden to the tab end: %v %v", ui.Tabs, ui.Hidden)
	}
	put("a", "off")
	if ui = state(); slices.Contains(ui.Tabs, "a") || slices.Contains(ui.Hidden, "a") {
		t.Fatalf("off is in neither tabs nor hidden: %v %v", ui.Tabs, ui.Hidden)
	}

	put("d", "")
	put("e", "")
	put("f", "") // b, d, e, f fill four slots
	if ack := put("c", "tab"); ack.Reason == "" {
		t.Fatal("when tabs are full, don't reject, only report the reason")
	}
	if ui = state(); len(ui.Tabs) != 4 || slices.Contains(ui.Tabs, "c") {
		t.Fatalf("it doesn't fit in full tabs: %v", ui.Tabs)
	}
	if ack := put("g", ""); ack.Reason == "" {
		t.Fatal("a new screen uploaded without place is also reported when it is placed off-tab because tabs are full")
	}
	if ack := put("g", ""); ack.Reason != "" {
		t.Fatalf("an existing screen keeps its position, so there is nothing to report: %q", ack.Reason)
	}

	base := "/api/agents/" + a.Agent.ID + "/ui/screens"
	if s := h.API("POST", base+"?place=hidden", json.RawMessage(`{"version":1,"id":"f","title":"t","icon":"i","blocks":[{"type":"divider"}]}`), nil); s != http.StatusOK {
		t.Fatalf("owner place status %d", s)
	}
	if ui = state(); !slices.Contains(ui.Hidden, "f") {
		t.Fatalf("the owner uses place too: %v", ui.Hidden)
	}
	if s := h.API("POST", base+"?place=top", json.RawMessage(`{"version":1,"id":"g","title":"t","icon":"i","blocks":[{"type":"divider"}]}`), nil); s != http.StatusBadRequest {
		t.Fatalf("an unknown place is rejected: %d", s)
	}
}
