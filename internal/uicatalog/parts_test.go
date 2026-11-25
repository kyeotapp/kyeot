package uicatalog

import (
	"encoding/json"
	"strings"
	"testing"
)

const moneyCard = `{"version": 1, "id": "money-card", "title": "Money card",
  "params": {
    "label":  {"kind": "text", "required": true, "sample": "App revenue"},
    "value":  {"kind": "number", "required": true, "sample": 1200000},
    "before": {"kind": "number"},
    "daily":  {"kind": "items", "sample": [{"label": "Mon", "value": 40}]},
    "ask":    {"kind": "text", "default": "Tell me this month revenue in detail"}
  },
  "blocks": [
    {"type": "stat", "label": {"$param": "label"}, "value": {"$param": "value"}, "compare": {"$param": "before"}, "unit": "KRW"},
    {"type": "bars", "items": {"$param": "daily"}, "color": "blue"},
    {"type": "button", "label": "Details", "say": {"$param": "ask"}}
  ]}`

func mustPart(t *testing.T, raw string) map[string]Part {
	t.Helper()
	p, err := ValidatePart([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]Part{p.ID: p}
}

func TestExpandReplacesUse(t *testing.T) {
	parts := mustPart(t, moneyCard)
	screen := `{"version": 1, "id": "money", "title": "Revenue", "icon": "wonsign", "blocks": [
	  {"type": "heading", "text": "This month"},
	  {"type": "use", "part": "money-card", "with": {"label": "App revenue", "value": {"$data": "revenue.total"}, "daily": {"$data": "revenue.daily"}}}]}`
	exp, s, err := Expand([]byte(screen), parts)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "money" || len(s.Blocks) != 4 {
		t.Fatalf("screen %+v", s)
	}
	got := string(exp)
	for _, want := range []string{`"type":"stat"`, `"type":"bars"`, `"type":"button"`, `{"$data":"revenue.total"}`, `"say":"Tell me this month revenue in detail"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("expanded spec lacks %s: %s", want, got)
		}
	}
	if strings.Contains(got, `"use"`) || strings.Contains(got, "$param") || strings.Contains(got, "compare") {
		t.Fatalf("use, $param, or omitted optional properties remain: %s", got)
	}
}

func TestValidatePartRejects(t *testing.T) {
	sub := func(old, new string) string {
		if !strings.Contains(moneyCard, old) {
			t.Fatalf("sample lacks %q", old)
		}
		return strings.Replace(moneyCard, old, new, 1)
	}
	cases := map[string]struct{ raw, want string }{
		"kind mismatch":        {sub(`"value":  {"kind": "number"`, `"value":  {"kind": "text"`), "kind text 인데 이 자리는 number"},
		"unused parameter":     {sub(`"ask":    {"kind": "text", "default": "Tell me this month revenue in detail"}`, `"ask": {"kind": "text", "default": "x"}, "extra": {"kind": "text"}`), "extra 를 선언했는데"},
		"undeclared parameter": {sub(`{"$param": "ask"}`, `{"$param": "asked"}`), `선언하지 않은 매개변수 "asked"`},
		"use inside a part":    {sub(`{"type": "button", "label": "Details", "say": {"$param": "ask"}}`, `{"type": "button", "label": "Details", "say": {"$param": "ask"}}, {"type": "use", "part": "x"}`), "use 를 못 쓴다"},
		"sample kind":          {sub(`"sample": 1200000`, `"sample": "many"`), "params.value.sample"},
		"wrong enum value":     {sub(`"color": "blue"`, `"color": {"$param": "c"}`), "선언하지 않은"},
		"unknown kind":         {sub(`"before": {"kind": "number"}`, `"before": {"kind": "params"}`), "params.before.kind"},
	}
	for name, c := range cases {
		_, err := ValidatePart([]byte(c.raw))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

func TestExpandRejects(t *testing.T) {
	parts := mustPart(t, moneyCard)
	use := func(with string) string {
		return `{"version": 1, "id": "money", "title": "Revenue", "blocks": [{"type": "use", "part": "money-card", "with": ` + with + `}]}`
	}
	cases := map[string]struct{ raw, want string }{
		"required missing": {use(`{"label": "a"}`), "with 에 value 가 필요하다"},
		"unknown name":     {use(`{"label": "a", "value": 1, "colour": "x"}`), `없는 매개변수 "colour"`},
		"wrong kind":       {use(`{"label": "a", "value": "many"}`), "with.value"},
		"unknown part":     {strings.Replace(use(`{}`), "money-card", "nope", 1), "없는 조립 부품 nope"},
	}
	for name, c := range cases {
		_, _, err := Expand([]byte(c.raw), parts)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

func TestExpandOverLimit(t *testing.T) {
	parts := mustPart(t, moneyCard)
	var blocks []string
	for range 21 {
		blocks = append(blocks, `{"type": "use", "part": "money-card", "with": {"label": "a", "value": 1}}`)
	}
	raw := `{"version": 1, "id": "money", "title": "Revenue", "blocks": [` + strings.Join(blocks, ",") + `]}`
	_, _, err := Expand([]byte(raw), parts)
	if err == nil || !strings.Contains(err.Error(), "펼치면 blocks 가 63개") {
		t.Fatalf("%v", err)
	}
}

func TestPreviewUsesSampleThenKindDefault(t *testing.T) {
	p := mustPart(t, moneyCard)["money-card"]
	var blocks []map[string]any
	raw, _ := json.Marshal(p.Preview())
	json.Unmarshal(raw, &blocks)
	if blocks[0]["label"] != "App revenue" || blocks[0]["compare"] != float64(1234) {
		t.Fatalf("stat %v", blocks[0])
	}
	if items := blocks[1]["items"].([]any); len(items) != 1 {
		t.Fatalf("bars %v", blocks[1])
	}
	if blocks[2]["say"] != "Tell me this month revenue in detail" {
		t.Fatalf("button %v", blocks[2])
	}
	for _, b := range p.Preview() {
		if err := validateBlock(b); err != nil {
			t.Fatal(err)
		}
	}
}

func TestKindDefaultItemsHasThree(t *testing.T) {
	p := mustPart(t, strings.Replace(moneyCard, `"daily":  {"kind": "items", "sample": [{"label": "Mon", "value": 40}]}`, `"daily": {"kind": "items"}`, 1))["money-card"]
	var items []any
	json.Unmarshal(p.SampleValue("daily"), &items)
	if len(items) != 3 {
		t.Fatalf("%v", items)
	}
}

func TestUses(t *testing.T) {
	got := Uses([]byte(`{"blocks": [{"type": "use", "part": "a"}, {"type": "stat"}, {"type": "use", "part": "a"}, {"type": "use", "part": "b"}]}`))
	if strings.Join(got, ",") != "a,b" {
		t.Fatal(got)
	}
}
