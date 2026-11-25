package store_test

import (
	"slices"
	"testing"

	"github.com/kyeotapp/kyeot/internal/store"
)

// Default assignment must stay the old 10 kinds. Existing agents' characters must not change even if the pickable list grows.
func TestDefaultAvatarUnchanged(t *testing.T) {
	want := map[string]string{
		"a": "preset:dog", "b": "preset:dog", "c": "preset:frog", "d": "preset:panda", "e": "preset:fox",
		"f": "preset:rabbit", "g": "preset:penguin", "h": "preset:robot", "agent-1": "preset:cat", "agent-2": "preset:penguin",
		"00000000-0000-0000-0000-000000000000": "preset:bear", "Agent A": "preset:robot",
	}
	for id, w := range want {
		if got := store.DefaultAvatar(id); got != w {
			t.Errorf("DefaultAvatar(%q) = %q, want %q", id, got, w)
		}
	}
}

// Moving character names must not overlap the old 10. scripts/check-contract.mjs checks they match the app's iconDrafts.ts.
func TestMotionPresetsDistinct(t *testing.T) {
	for _, k := range store.Presets {
		if slices.Contains(store.MotionPresets, k) {
			t.Errorf("overlaps an old name: %s", k)
		}
	}
}

func TestPickable(t *testing.T) {
	for _, ok := range []string{"owl", "robot", "maltipooG", "puppyG"} {
		if !store.Pickable(ok) {
			t.Errorf("cannot pick %s", ok)
		}
	}
	for _, bad := range []string{"", "dragon", "pinkear", "cat ", "maltipoog", "maltipooRoundG"} {
		if store.Pickable(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}
