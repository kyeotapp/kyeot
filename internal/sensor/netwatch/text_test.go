package netwatch

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/kyeotapp/kyeot/internal/sensor/text/texttest"
	"github.com/kyeotapp/kyeot/internal/uicatalog"
)

func TestTextKeysMatchKorean(t *testing.T) {
	cases := map[string]Bundle{
		"real day":            build(readRecords("testdata/2026-11-16.tsv"), ""),
		"no records":          build(nil, ""),
		"DNS in progress":     build([]record{line("09:00:00", "DNS_DOWN"), line("09:02:00", "DNS_DOWN")}, ""),
		"outage over an hour": build([]record{line("09:00:00", "GW_DOWN"), line("10:30:00", "WAN_DOWN"), line("10:31:00", "OK")}, ""),
		"measurement failed":  build(nil, "ping not found"),
		"unknown state":       build([]record{line("09:00:00", "WEIRD"), line("09:03:00", "OK")}, ""),
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(b)
			texttest.Check(t, raw)
		})
	}
}

// The key table adds no warnings. The server bundle (with key table) and the agent bundle (Python original, without) warn identically.
func TestTextFieldIsNotWarned(t *testing.T) {
	spec, err := os.ReadFile("../system/testdata/computer.json")
	if err != nil {
		t.Fatal(err)
	}
	ours, _ := json.Marshal(build(readRecords("testdata/2026-11-16.tsv"), ""))
	agent, err := os.ReadFile("testdata/2026-11-16.want.json")
	if err != nil {
		t.Fatal(err)
	}
	screens := []json.RawMessage{spec}
	a, b := uicatalog.DataWarnings(screens, BundleName, ours), uicatalog.DataWarnings(screens, BundleName, agent)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("server %v\nagent %v", a, b)
	}
}
