package uicatalog

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// testdata/template_contracts.json is extracted by scripts/gen-template-docs.mjs from the contract samples in the app's templates.ts
type contract struct {
	ID, Bundle, Shape string
	Spec              json.RawMessage
	Value             any
}

func loadContracts(t *testing.T) []contract {
	t.Helper()
	raw, err := os.ReadFile("testdata/template_contracts.json")
	if err != nil {
		t.Fatal(err)
	}
	var cs []contract
	if err := json.Unmarshal(raw, &cs); err != nil {
		t.Fatal(err)
	}
	if len(cs) < 7 {
		t.Fatalf("only %d contract samples", len(cs))
	}
	return cs
}

func warn(spec json.RawMessage, bundle string, v any) []string {
	raw, _ := json.Marshal(v)
	var out []string
	for _, w := range DataWarnings([]json.RawMessage{spec}, bundle, raw) {
		out = append(out, w.String())
	}
	return out
}

// renames returns copies with one object key renamed to zz each. Every key is one the spec or catalog knows.
func renames(v any) []any {
	var out []any
	switch x := v.(type) {
	case map[string]any:
		for k := range x {
			c := clone(x)
			c["zz"] = c[k]
			delete(c, k)
			out = append(out, c)
			for _, sub := range renames(x[k]) {
				c := clone(x)
				c[k] = sub
				out = append(out, c)
			}
		}
	case []any:
		for i := range x {
			for _, sub := range renames(x[i]) {
				c := append([]any(nil), x...)
				c[i] = sub
				out = append(out, c)
			}
		}
	}
	return out
}

func clone(m map[string]any) map[string]any {
	c := make(map[string]any, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func TestTemplateContracts(t *testing.T) {
	for _, c := range loadContracts(t) {
		if _, err := Validate(c.Spec); err != nil {
			t.Errorf("%s spec: %v", c.ID, err)
		}
		if w := warn(c.Spec, c.Bundle, c.Value); len(w) != 0 {
			t.Errorf("%s sample bundle got a warning: %v", c.ID, w)
		}
		bad := renames(c.Value)
		if len(bad) == 0 {
			t.Fatalf("%s: no key to rename", c.ID)
		}
		for _, b := range bad {
			if len(warn(c.Spec, c.Bundle, b)) == 0 {
				s, _ := json.Marshal(b)
				t.Errorf("%s: a bundle with one key renamed passed: %s", c.ID, s)
			}
		}
		if w := warn(c.Spec, "other", map[string]any{"x": 1}); len(w) != 0 {
			t.Errorf("%s: warned about a bundle the spec does not use: %v", c.ID, w)
		}
	}
}

func TestDataWarningsSayWhere(t *testing.T) {
	cs := map[string]contract{}
	for _, c := range loadContracts(t) {
		cs[c.ID] = c
	}
	for name, tc := range map[string]struct {
		id    string
		value string
		want  string
	}{
		"list field key":   {"habits", `{"streak":1,"goals":[{"value":1,"goal":2},{"value":1,"goal":2},{"value":1,"goal":2}],"today":[{"name":"water"}]}`, "habits 탭: `habits.today.0`: list 칸은 없는 속성 \"name\""},
		"list first field": {"reading", `{"links":[{"subtitle":"3 min"}]}`, "`reading.links.0` 에 title 키가 없다"},
		"missing path":     {"revenue", `{"month":{"total":1,"before":2},"daily":[],"bySource":[]}`, "`revenue.month.prev` 가 묶음에 없다. `revenue.month` 의 키는 before, total"},
		"number slot":      {"assets", `{"total":"fifty million","prev":1,"byKind":[],"months":[],"series":[]}`, "`assets.total` 자리는 숫자인데 온 값은 글이다"},
		"list slot":        {"todos", `{"open":1,"dueToday":1,"items":{"title":"x"}}`, "`todos.items` 자리는 list 목록(배열)인데 온 값은 객체다"},
		"index field":      {"project", `{"milestones":[{"value":1,"goal":2}],"week":[]}`, "`project.milestones.1.value` 가 묶음에 없다. `project.milestones` 는 1개짜리 배열이다"},
		"$data in a field": {"workout", `{"week":{"count":[3]},"daily":[],"recent":[]}`, "`workout.week.count` 자리는 숫자나 글인데 온 값은 배열이다"},
	} {
		var v any
		json.Unmarshal([]byte(tc.value), &v)
		w := strings.Join(warn(cs[tc.id].Spec, tc.id, v), "\n")
		if !strings.Contains(w, tc.want) {
			t.Errorf("%s: %q missing:\n%s", name, tc.want, w)
		}
	}
}

// Following the skill's revenue example as is must fill the sample revenue tab. Last month once came up empty because before≠prev.
func TestSkillRevenueExampleFitsTemplate(t *testing.T) {
	raw, err := os.ReadFile("../../integrations/hermes/deck-ui/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	sec := string(raw)
	sec = sec[strings.Index(sec, "## Example: revenue tab"):]
	blocks := regexp.MustCompile("(?s)```json\n(.*?)```").FindAllStringSubmatch(sec, 2)
	var put struct {
		Name string
		Data any
	}
	if len(blocks) < 2 || json.Unmarshal([]byte(blocks[1][1]), &put) != nil || put.Name != "revenue" {
		t.Fatalf("could not find the spec and bundle of the revenue example")
	}
	if _, err := Validate([]byte(blocks[0][1])); err != nil {
		t.Fatalf("skill revenue spec: %v", err)
	}
	if w := warn(json.RawMessage(blocks[0][1]), "revenue", put.Data); len(w) != 0 {
		t.Errorf("the skill revenue example disagrees with its own spec: %v", w)
	}
	for _, c := range loadContracts(t) {
		if c.ID == "revenue" {
			if w := warn(c.Spec, "revenue", put.Data); len(w) != 0 {
				t.Errorf("the skill revenue example bundle disagrees with the sample revenue tab: %v", w)
			}
		}
	}
}

// Whether the tables in the two docs state the same shapes as the reference file. If not, run node scripts/gen-template-docs.mjs
func TestTemplateDocsInSync(t *testing.T) {
	for _, path := range []string{"../../docs/agent-ui.md", "../../integrations/hermes/deck-ui/SKILL.md"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range loadContracts(t) {
			if !strings.Contains(string(raw), fmt.Sprintf("`%s` | %s |", c.Bundle, c.Shape)) && !strings.Contains(string(raw), fmt.Sprintf("`%s`: %s |", c.Bundle, c.Shape)) {
				t.Errorf("%s lacks bundle %s shape %s", path, c.Bundle, c.Shape)
			}
		}
	}
}
