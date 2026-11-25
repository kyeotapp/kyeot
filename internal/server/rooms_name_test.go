package server

import (
	"strings"
	"testing"
)

func TestRoomNameCleanup(t *testing.T) {
	long := strings.Repeat("가", 45)
	for in, want := range map[string]string{
		"  Hello   world ":  "Hello world",
		"# Hello world":     "Hello world",
		"##room":            "room",
		"＃ full-width hash": "full-width hash",
		"   ":               "",
		"#":                 "",
		long:                strings.Repeat("가", 40),
		"a\tb\nc":           "a b c",
	} {
		if got := roomName(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
