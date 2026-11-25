package report

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func norm(t *testing.T, raw, kind string) map[string]any {
	t.Helper()
	b, _, err := Normalize(json.RawMessage(raw), kind, time.UnixMilli(1_700_000_000_000))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	return m
}

func TestPartialReportGetsEmptyLists(t *testing.T) {
	b, f, err := Normalize(json.RawMessage(`{"routines":[{"id":"job1","name":"daily","cron":"0 9 * * *","state":"ok"}]}`), "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"periods":[]`, `"daily":[]`, `"generatedAt":`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s in %s", want, s)
		}
	}
	if !f.Routines || f.Costs {
		t.Fatalf("features: %+v", f)
	}
	b, f, _ = Normalize(json.RawMessage(`{}`), "", time.Now())
	if !strings.Contains(string(b), `"routines":[]`) || f.Routines || f.Costs {
		t.Fatalf("empty report: %s %+v", b, f)
	}
}

func TestStateAliasesAndOn(t *testing.T) {
	cases := map[string][2]any{
		`{"state":"정상"}`:            {"ok", true},
		`{"state":"꺼짐"}`:            {"off", false},
		`{"state":"paused"}`:        {"off", false},
		`{"state":"ERROR"}`:         {"error", true},
		`{"state":"completed"}`:     {"done", false},
		`{}`:                        {"ok", true},
		`{"on":false}`:              {"off", false},
		`{"state":"ok","on":false}`: {"ok", false}, // an explicit on is respected
	}
	for raw, want := range cases {
		body := strings.TrimSuffix(strings.TrimPrefix(raw, "{"), "}")
		if body != "" {
			body = "," + body
		}
		m := norm(t, `{"routines":[{"id":"r"`+body+`}]}`, "")
		r := m["routines"].([]any)[0].(map[string]any)
		if r["state"] != want[0] || r["on"] != want[1] {
			t.Fatalf("%s → state=%v on=%v, want %v", raw, r["state"], r["on"], want)
		}
	}
}

func TestScheduleTitleAndDrop(t *testing.T) {
	m := norm(t, `{"routines":[
	  {"id":"a","name":"co-op-news","cron":"0 9 * * 1,4"},
	  {"id":"b","name":"b","every":120},
	  {"id":"c","name":"c","at":1800000000000,"once":true},
	  {"name":"only-name"},
	  {"state":"ok"}
	]}`, "hermes")
	rs := m["routines"].([]any)
	if len(rs) != 4 {
		t.Fatalf("routine without id or name should be dropped: %d", len(rs))
	}
	a, b, c, d := rs[0].(map[string]any), rs[1].(map[string]any), rs[2].(map[string]any), rs[3].(map[string]any)
	if a["schedule"] != "매주 월, 목 09:00" || a["title"] != "co op news" {
		t.Fatalf("a: %v %v", a["schedule"], a["title"])
	}
	if b["schedule"] != "2시간마다" || !strings.HasSuffix(c["schedule"].(string), ", 한 번") {
		t.Fatalf("b/c: %v %v", b["schedule"], c["schedule"])
	}
	if d["id"] != "only-name" || d["name"] != "only-name" {
		t.Fatalf("name-only routine: %+v", d)
	}
	if a["nextRun"] != nil || a["runs"] != float64(0) || a["lastStatus"] != "" {
		t.Fatalf("defaults: %+v", a)
	}
	// other agent kinds keep the name as is
	m = norm(t, `{"routines":[{"id":"a","name":"co-op-news"}]}`, "mybot")
	if m["routines"].([]any)[0].(map[string]any)["title"] != "co-op-news" {
		t.Fatal("non-hermes title should keep hyphens")
	}
}

func TestUnknownFieldsSurvive(t *testing.T) {
	m := norm(t, `{"routines":[{"id":"a","extra":{"x":1}}],"custom":"keep","periods":[{"key":"week","cost":1.5}]}`, "")
	if m["custom"] != "keep" {
		t.Fatal("top-level unknown field dropped")
	}
	r := m["routines"].([]any)[0].(map[string]any)
	if r["extra"] == nil {
		t.Fatal("routine unknown field dropped")
	}
	p := m["periods"].([]any)[0].(map[string]any)
	if p["cost"] != 1.5 || p["before"] != float64(0) || p["name"] != "" {
		t.Fatalf("period defaults: %+v", p)
	}
	if _, ok := p["bySource"].([]any); !ok {
		t.Fatal("bySource should be a list")
	}
}

func TestByRoutineKeptWithTitle(t *testing.T) {
	m := norm(t, `{"routines":[{"id":"a","name":"daily-summary","monthCost":0.1,"monthRuns":2}],"periods":[{"key":"month","byRoutine":[{"name":"AI-contest-alert","runs":6,"cost":0.128,"gone":true}]}],"daily":[{"date":"2026-11-19","byRoutine":[{"name":"daily-summary","runs":1,"cost":0.01}]}]}`, "hermes")
	r := m["routines"].([]any)[0].(map[string]any)
	if r["monthCost"] != 0.1 || r["monthRuns"] != float64(2) {
		t.Fatalf("routine month cost dropped: %+v", r)
	}
	rc := m["periods"].([]any)[0].(map[string]any)["byRoutine"].([]any)[0].(map[string]any)
	if rc["cost"] != 0.128 || rc["gone"] != true || rc["title"] != "AI contest alert" {
		t.Fatalf("byRoutine: %+v", rc)
	}
	d := m["daily"].([]any)[0].(map[string]any)["byRoutine"].([]any)[0].(map[string]any)
	if d["title"] != "daily summary" {
		t.Fatalf("daily byRoutine title: %+v", d)
	}
}

func TestNotAnObject(t *testing.T) {
	if _, _, err := Normalize(json.RawMessage(`[1]`), "", time.Now()); err == nil {
		t.Fatal("array should be rejected")
	}
}
