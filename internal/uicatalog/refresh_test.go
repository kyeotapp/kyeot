package uicatalog

import (
	"strings"
	"testing"
)

func TestRefreshNeedsSayRoutineOrData(t *testing.T) {
	spec := func(block string) []byte {
		return []byte(`{"version":1,"id":"net","title":"Network","icon":"wifi","blocks":[` + block + `]}`)
	}
	ok := []string{
		`{"type":"refresh","say":"Recompute the numbers"}`,
		`{"type":"refresh","routine":"a1b2c3d4e5f6","data":"netwatch"}`,
		`{"type":"refresh","routine":"job_1","say":"Again","label":"Refresh","kind":"tonal"}`,
		`{"type":"refresh","data":"netwatch","style":"icon"}`,                      // a sensor bundle refreshes directly with data alone
		`{"type":"refresh","bundles":["netwatch","disk","system"],"style":"icon"}`, // three bundles with one button
	}
	for _, b := range ok {
		if _, err := Validate(spec(b)); err != nil {
			t.Fatalf("%s: %v", b, err)
		}
	}
	bad := map[string]string{
		`{"type":"refresh"}`:                         "say, routine, data",
		`{"type":"refresh","say":"  "}`:              "say, routine, data",
		`{"type":"refresh","data":"Net"}`:            "refresh.data",
		`{"type":"refresh","routine":"a; rm -rf /"}`: "refresh.routine",
		`{"type":"refresh","routine":{"$data":"x"}}`: "refresh.routine",
		`{"type":"refresh","say":"x","data":"Net"}`:  "refresh.data",
		`{"type":"refresh","bundles":["Net"]}`:       "refresh.bundles",
		`{"type":"refresh","bundles":[]}`:            "refresh.bundles",
	}
	for b, want := range bad {
		_, err := Validate(spec(b))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: want %q, got %v", b, want, err)
		}
	}
}
