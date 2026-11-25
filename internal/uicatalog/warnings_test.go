package uicatalog

import (
	"strings"
	"testing"
)

func TestWarningsDoNotRejectButExplain(t *testing.T) {
	spec := func(icon, blocks string) []byte {
		return []byte(`{"version":1,"id":"x","title":"t","icon":"` + icon + `","blocks":[` + blocks + `]}`)
	}
	clean := spec("star", `{"type":"countdown","label":"Vacation","to":"2026-12-24T18:00:00+09:00"},{"type":"calendar","items":[{"date":"2026-11-16","value":1}]}`)
	if _, err := Validate(clean); err != nil {
		t.Fatal(err)
	}
	if w := Warnings(clean); len(w) != 0 {
		t.Fatalf("clean spec warned: %v", w)
	}

	bad := spec("no.such.icon", `{"type":"countdown","label":"Vacation","to":"next week"},{"type":"calendar","items":[{"date":"11/16","value":1}]},{"type":"calendar","items":{"$data":"a.b"}}`)
	if _, err := Validate(bad); err != nil {
		t.Fatalf("warnings must not reject: %v", err)
	}
	w := strings.Join(Warnings(bad), "\n")
	for _, want := range []string{"blocks[0]: countdown.to", "blocks[1].items[0]: calendar"} {
		if !strings.Contains(w, want) {
			t.Fatalf("missing %q in:\n%s", want, w)
		}
	}
	if strings.Contains(w, "blocks[2]") {
		t.Fatalf("a $data value cannot be known from the spec alone, so it must not warn:\n%s", w)
	}
}
