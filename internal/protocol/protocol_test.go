package protocol

import "testing"

func TestValidSchedule(t *testing.T) {
	for s, want := range map[string]bool{
		"0 9 * * *": true, "30 18 * * 1-5": true, "0 9 * * 1,4": true, "*/15 * * * *": true,
		"every 2h": true, "30m": true, "every 0h": false, "0 9 * *": false,
		"0 9 * * *; rm -rf /": false, "--help": false, "": false, "0 9 * * * *": false,
	} {
		if got := ValidSchedule(s); got != want {
			t.Errorf("%q: got %v want %v", s, got, want)
		}
	}
}

func TestParseUIVersionsAndRevert(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"type":"ui.versions","id":"habits"}`:        true,
		`{"type":"ui.versions"}`:                      false,
		`{"type":"ui.revert","id":"habits","to":3}`:   true,
		`{"type":"ui.revert","id":"habits"}`:          false,
		`{"type":"ui.revert","id":"habits","to":0}`:   false,
		`{"type":"ui.revert","id":"habits","to":-2}`:  false,
		`{"type":"ui.revert","id":"habits","to":"3"}`: false,
		`{"type":"ui.revert","to":3}`:                 false,
	} {
		msg, _, ok := ParseAgentMessage([]byte(raw))
		if ok != want {
			t.Errorf("%s: ok=%v, want %v", raw, ok, want)
		}
		if ok && msg.Type == "ui.revert" && msg.To != 3 {
			t.Errorf("%s: to=%d", raw, msg.To)
		}
	}
}
