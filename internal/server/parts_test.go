package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/fakeagent"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

const moneyCard = `{"version": 1, "id": "money-card", "title": "Money card",
  "params": {
    "label":  {"kind": "text", "required": true, "sample": "App revenue"},
    "value":  {"kind": "number", "required": true},
    "before": {"kind": "number", "sample": 950000},
    "daily":  {"kind": "items"},
    "ask":    {"kind": "text", "default": "Tell me this month's revenue in detail"},
    "unit":   {"kind": "text", "default": "USD"}
  },
  "blocks": [
    {"type": "stat", "label": {"$param": "label"}, "value": {"$param": "value"}, "compare": {"$param": "before"}, "unit": {"$param": "unit"}},
    {"type": "bars", "items": {"$param": "daily"}, "color": "blue"},
    {"type": "button", "label": "Details", "say": {"$param": "ask"}}
  ]}`

const moneyScreen = `{"version": 1, "id": "money", "title": "Revenue", "icon": "chart.bar", "blocks": [
  {"type": "use", "part": "money-card", "with": {"label": "App revenue", "value": {"$data": "revenue.total"}, "daily": {"$data": "revenue.daily"}}}]}`

type partsRig struct {
	h     *decktest.Harness
	agent *fakeagent.Agent
	id    string
}

func newPartsRig(t *testing.T) partsRig {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	return partsRig{h, h.StartFakeAgent(a.Token), a.Agent.ID}
}

func (r partsRig) send(t *testing.T, msg map[string]any) protocol.ServerMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.agent.Send(ctx, msg); err != nil {
		t.Fatal(err)
	}
	return recv(t, r.agent.Acks)
}

func (r partsRig) putPart(t *testing.T, raw string) protocol.ServerMessage {
	t.Helper()
	return r.send(t, map[string]any{"type": "ui.part.put", "part": json.RawMessage(raw)})
}

func (r partsRig) putScreen(t *testing.T, raw string) protocol.ServerMessage {
	t.Helper()
	return r.send(t, map[string]any{"type": "ui.put", "screen": json.RawMessage(raw)})
}

func (r partsRig) appSpec(t *testing.T, screen string) string {
	t.Helper()
	var ui uiState
	r.h.API("GET", "/api/agents/"+r.id+"/ui", nil, &ui)
	for _, s := range ui.Screens {
		if s.ID == screen {
			return string(s.Spec)
		}
	}
	t.Fatalf("screen %s missing: %+v", screen, ui.Screens)
	return ""
}

func mustOK(t *testing.T, ack protocol.ServerMessage) {
	t.Helper()
	if ack.OK == nil || !*ack.OK {
		t.Fatalf("ack %+v", ack)
	}
}

func TestPartExpandsForAppOnly(t *testing.T) {
	r := newPartsRig(t)
	ack := r.putPart(t, moneyCard)
	mustOK(t, ack)
	if ack.ID != "money-card" {
		t.Fatalf("ack id %q", ack.ID)
	}
	mustOK(t, r.putScreen(t, moneyScreen))

	spec := r.appSpec(t, "money")
	for _, want := range []string{`"type":"stat"`, `"type":"bars"`, `"type":"button"`, `{"$data":"revenue.total"}`, `{"$data":"revenue.daily"}`, `"unit":"USD"`} {
		if !strings.Contains(spec, want) {
			t.Fatalf("app spec lacks %s: %s", want, spec)
		}
	}
	if strings.Contains(spec, `"use"`) || strings.Contains(spec, "$param") {
		t.Fatalf("use reached the app: %s", spec)
	}

	// the agent gets back the source and the part list
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r.agent.Send(ctx, map[string]any{"type": "ui.list"})
	var got struct {
		Screens []struct {
			ID   string          `json:"id"`
			Spec json.RawMessage `json:"spec"`
		} `json:"screens"`
		Parts []struct {
			ID      string `json:"id"`
			Version int    `json:"version"`
		} `json:"parts"`
	}
	json.Unmarshal(recv(t, r.agent.States).UI, &got)
	if len(got.Parts) != 1 || got.Parts[0].ID != "money-card" || len(got.Screens) != 1 || !strings.Contains(string(got.Screens[0].Spec), `"type":"use"`) {
		t.Fatalf("ui.state %+v", got)
	}
}

func TestPartRejectsWithReason(t *testing.T) {
	r := newPartsRig(t)
	mustOK(t, r.putPart(t, moneyCard))
	use := func(with string) string {
		return `{"version": 1, "id": "money", "title": "Revenue", "blocks": [{"type": "use", "part": "money-card", "with": ` + with + `}]}`
	}
	cases := []struct {
		name, kind, raw, want string
	}{
		{"required missing", "screen", use(`{"label": "a"}`), "value 가 필요하다"},
		{"unknown name", "screen", use(`{"label": "a", "value": 1, "nope": 1}`), `없는 매개변수 "nope"`},
		{"kind mismatch", "screen", use(`{"label": "a", "value": "many"}`), "with.value"},
		{"missing part", "screen", strings.Replace(use(`{}`), "money-card", "ghost", 1), "없는 조립 부품 ghost"},
		{"unused $param", "part", strings.Replace(moneyCard, `"unit":   {"kind": "text", "default": "USD"}`, `"unit": {"kind": "text", "default": "USD"}, "extra": {"kind": "text"}`, 1), "extra 를 선언했는데"},
		{"use inside part", "part", strings.Replace(moneyCard, `"color": "blue"}`, `"color": "blue"}, {"type": "use", "part": "x"}`, 1), "use 를 못 쓴다"},
		{"sample kind", "part", strings.Replace(moneyCard, `"sample": 950000`, `"sample": "many"`, 1), "params.before.sample"},
	}
	for _, c := range cases {
		var ack protocol.ServerMessage
		if c.kind == "part" {
			ack = r.putPart(t, strings.Replace(c.raw, "money-card", "other-card", 1))
		} else {
			ack = r.putScreen(t, c.raw)
		}
		if ack.OK == nil || *ack.OK || !strings.Contains(ack.Reason, c.want) {
			t.Errorf("%s: %+v, want reason %q", c.name, ack, c.want)
		}
	}
}

func TestPartExpandOver60(t *testing.T) {
	r := newPartsRig(t)
	mustOK(t, r.putPart(t, moneyCard))
	var blocks []string
	for range 21 {
		blocks = append(blocks, `{"type": "use", "part": "money-card", "with": {"label": "a", "value": 1}}`)
	}
	ack := r.putScreen(t, `{"version": 1, "id": "money", "title": "Revenue", "blocks": [`+strings.Join(blocks, ",")+`]}`)
	if ack.OK == nil || *ack.OK || !strings.Contains(ack.Reason, "펼치면 blocks 가 63개") {
		t.Fatalf("%+v", ack)
	}
}

func TestPartPreview(t *testing.T) {
	r := newPartsRig(t)
	mustOK(t, r.putPart(t, moneyCard))
	var got struct {
		Blocks []map[string]any `json:"blocks"`
	}
	if s := r.h.API("GET", "/api/agents/"+r.id+"/ui/parts/money-card/preview", nil, &got); s != http.StatusOK {
		t.Fatalf("status %d", s)
	}
	stat := got.Blocks[0]
	if stat["label"] != "App revenue" || stat["value"] != float64(1234) || stat["compare"] != float64(950000) || stat["unit"] != "USD" {
		t.Fatalf("stat %v", stat)
	}
	if items, _ := got.Blocks[1]["items"].([]any); len(items) != 3 {
		t.Fatalf("bars %v", got.Blocks[1])
	}
	if s := r.h.API("GET", "/api/agents/"+r.id+"/ui/parts/ghost/preview", nil, nil); s != http.StatusNotFound {
		t.Fatalf("missing part %d", s)
	}
}

func TestPartNewVersionNotifiesApp(t *testing.T) {
	r := newPartsRig(t)
	mustOK(t, r.putPart(t, moneyCard))
	mustOK(t, r.putScreen(t, moneyScreen))
	if !strings.Contains(r.appSpec(t, "money"), `"unit":"USD"`) {
		t.Fatal("first version")
	}
	c := r.h.ConnectClient()
	mustOK(t, r.putPart(t, strings.Replace(moneyCard, `"default": "USD"`, `"default": "$"`, 1)))
	r.h.WaitFor(c, "ui", func(m protocol.ClientMessage) bool { return m.Type == "ui" && m.AgentID == r.id })
	if spec := r.appSpec(t, "money"); !strings.Contains(spec, `"unit":"$"`) {
		t.Fatalf("expands with the new part without re-uploading the screen: %s", spec)
	}
	var vs []struct {
		Version int `json:"version"`
	}
	r.h.API("GET", "/api/agents/"+r.id+"/ui/parts/money-card/versions", nil, &vs)
	if len(vs) != 2 || vs[0].Version != 2 {
		t.Fatalf("versions %+v", vs)
	}
	if s := r.h.API("POST", "/api/agents/"+r.id+"/ui/parts/money-card/revert", map[string]int{"version": 1}, nil); s != http.StatusOK {
		t.Fatalf("revert %d", s)
	}
	if spec := r.appSpec(t, "money"); !strings.Contains(spec, `"unit":"USD"`) {
		t.Fatalf("revert: %s", spec)
	}
}

func TestPartUpdateThatBreaksScreenIsRejected(t *testing.T) {
	r := newPartsRig(t)
	mustOK(t, r.putPart(t, moneyCard))
	mustOK(t, r.putScreen(t, moneyScreen))
	// making a parameter required that the screen doesn't supply stops the screen from expanding
	ack := r.putPart(t, strings.Replace(moneyCard, `"before": {"kind": "number", "sample": 950000}`, `"before": {"kind": "number", "required": true}`, 1))
	if ack.OK == nil || *ack.OK || !strings.Contains(ack.Reason, "화면 money") {
		t.Fatalf("%+v", ack)
	}
}

func TestRemoveUsedPartConflicts(t *testing.T) {
	r := newPartsRig(t)
	mustOK(t, r.putPart(t, moneyCard))
	mustOK(t, r.putScreen(t, moneyScreen))
	base := "/api/agents/" + r.id + "/ui/parts"

	var list []struct {
		ID     string   `json:"id"`
		UsedBy []string `json:"usedBy"`
	}
	r.h.API("GET", base, nil, &list)
	if len(list) != 1 || !slices.Equal(list[0].UsedBy, []string{"money"}) {
		t.Fatalf("list %+v", list)
	}
	var e struct {
		Error string `json:"error"`
	}
	if s := r.h.API("DELETE", base+"/money-card", nil, &e); s != http.StatusConflict || !strings.Contains(e.Error, "money") {
		t.Fatalf("delete used part %d %+v", s, e)
	}
	ack := r.send(t, map[string]any{"type": "ui.part.remove", "id": "money-card"})
	if ack.OK == nil || *ack.OK || !strings.Contains(ack.Reason, "money") {
		t.Fatalf("delete agent %+v", ack)
	}
	if s := r.h.API("DELETE", "/api/agents/"+r.id+"/ui/screens/money", nil, nil); s != http.StatusNoContent {
		t.Fatalf("delete screen %d", s)
	}
	r.h.API("GET", base, nil, &list)
	if len(list) != 1 || len(list[0].UsedBy) != 0 {
		t.Fatalf("list %+v", list)
	}
	if s := r.h.API("DELETE", base+"/money-card", nil, nil); s != http.StatusNoContent {
		t.Fatalf("delete unused part %d", s)
	}
	if s := r.h.API("DELETE", base+"/money-card", nil, nil); s != http.StatusNotFound {
		t.Fatalf("missing part %d", s)
	}
}

func TestPartRoutesNeedOwnedAgent(t *testing.T) {
	r := newPartsRig(t)
	mustOK(t, r.putPart(t, moneyCard))
	for _, path := range []string{"/ui/parts", "/ui/parts/money-card/preview", "/ui/parts/money-card/versions"} {
		if s := r.h.API("GET", "/api/agents/nope"+path, nil, nil); s != http.StatusNotFound {
			t.Errorf("%s %d", path, s)
		}
	}
}
