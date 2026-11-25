package main

import "testing"

func TestAccessLogOffByDefault(t *testing.T) {
	for v, on := range map[string]bool{"": false, "0": false, "off": false, "1": true, "ON": true, "yes": true} {
		t.Setenv("KYEOT_ACCESS_LOG", v)
		t.Setenv("AGENT_DECK_ACCESS_LOG", "")
		if got := accessLogOut() != nil; got != on {
			t.Errorf("KYEOT_ACCESS_LOG=%q: on=%v, want %v", v, got, on)
		}
	}
}
