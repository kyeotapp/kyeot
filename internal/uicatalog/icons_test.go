package uicatalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFitIcon(t *testing.T) {
	cases := []struct {
		name, spec, want, note string
	}{
		{"empty icon, title costs", `{"version":1,"id":"a","title":"이번 달 비용","blocks":[{"type":"list","items":[]}]}`, "chart.bar", "icon 이 없어"},
		{"icon outside the list", `{"version":1,"id":"a","title":"테스트","icon":"testtube.2","blocks":[{"type":"text","text":"x"}]}`, "square.grid.2x2", `"testtube.2" 는 앱 대응표에 없어`},
		{"valid icon is kept", `{"version":1,"id":"a","title":"이번 달 비용","icon":"heart","blocks":[{"type":"divider"}]}`, "heart", ""},
		{".fill pair is kept", `{"version":1,"id":"a","title":"비용","icon":"star.fill","blocks":[{"type":"divider"}]}`, "star.fill", ""},
		{"no title match, first mapped part", `{"version":1,"id":"a","title":"내 화면","icon":"","blocks":[{"type":"heading","text":"x"},{"type":"donut","items":[]},{"type":"list","items":[]}]}`, "chart.pie", "icon 이 없어"},
		{"nothing at all", `{"version":1,"id":"a","title":"무엇","icon":null,"blocks":[{"type":"divider"}]}`, "square.grid.2x2", "icon 이 없어"},
		{"one-letter word matches whole word only", `{"version":1,"id":"a","title":"방송 통계","blocks":[{"type":"stat","label":"x","value":1}]}`, "number", "icon 이 없어"},
		{"English matches whole word only", `{"version":1,"id":"a","title":"Weather today","blocks":[{"type":"divider"}]}`, "sun.max", "icon 이 없어"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Validate([]byte(c.spec)); err != nil {
				t.Fatalf("validate: %v", err)
			}
			out, note := FitIcon([]byte(c.spec))
			var s Screen
			if err := json.Unmarshal(out, &s); err != nil {
				t.Fatal(err)
			}
			if s.Icon != c.want {
				t.Fatalf("icon = %q, want %q", s.Icon, c.want)
			}
			if c.note == "" && (note != "" || string(out) != c.spec) {
				t.Fatalf("a valid icon must not touch the spec: %q %s", note, out)
			}
			if !strings.Contains(note, c.note) {
				t.Fatalf("note %q lacks %q", note, c.note)
			}
			if _, err := Validate(out); err != nil {
				t.Fatalf("the fixed spec must pass too: %v", err)
			}
		})
	}
}

func TestIconRulesUseCatalogIcons(t *testing.T) {
	all := []string{rules.Fallback}
	for _, e := range rules.Words {
		all = append(all, e.Icon)
	}
	for b, icon := range rules.Blocks {
		if _, ok := cat.Components[b]; !ok {
			t.Errorf("%q in iconRules.blocks is not a part", b)
		}
		all = append(all, icon)
	}
	for _, icon := range all {
		if !KnownIcon(icon) {
			t.Errorf("%q in iconRules is not in icons", icon)
		}
	}
}

func TestStarterRoomsIcon(t *testing.T) {
	for _, s := range Starters() {
		v, err := Validate(s.Raw)
		if err != nil || !KnownIcon(v.Icon) {
			t.Fatalf("%s: %v %q", s.ID, err, v.Icon)
		}
		if s.ID == "rooms" && v.Icon != "bubble.left" {
			t.Fatalf("rooms icon = %q", v.Icon)
		}
	}
}
