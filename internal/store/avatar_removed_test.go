package store

import "testing"

// A stored value of an old character name that can no longer be picked is still returned as is (the app resolves it)
func TestRemovedPresetStillReadable(t *testing.T) {
	s, _ := openFile(t)
	a, _, err := s.CreateAgent(Owner, "a")
	if err != nil {
		t.Fatal(err)
	}
	const old = "preset:maltipooRoundG"
	if _, err := s.SetAvatar(a.ID, old); err != nil {
		t.Fatal(err)
	}
	got, err := s.Agent(a.ID)
	if err != nil || got.Avatar != old {
		t.Fatalf("stored value %q err=%v, want %q", got.Avatar, err, old)
	}
	if list, _ := s.Agents(Owner); len(list) != 1 || list[0].Avatar != old {
		t.Fatalf("the list changed the stored value: %+v", list)
	}
}
