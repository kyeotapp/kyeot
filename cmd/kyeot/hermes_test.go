package main

import "testing"

func TestLocalURL(t *testing.T) {
	for addr, want := range map[string]string{
		"0.0.0.0:7700":   "http://127.0.0.1:7700",
		":7700":          "http://127.0.0.1:7700",
		"[::]:7700":      "http://127.0.0.1:7700",
		"127.0.0.1:7797": "http://127.0.0.1:7797",
	} {
		if got := localURL(addr); got != want {
			t.Errorf("%s: %s", addr, got)
		}
	}
}
