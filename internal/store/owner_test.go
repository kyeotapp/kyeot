package store

import (
	"path/filepath"
	"testing"
)

// Deleting only the computer owner keeps the others, and reopening starts with an empty owner.
func TestDeleteHostKeepsOthers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	other, _, _ := s.CreateOwner("Alice")
	if _, _, err := s.CreateAgent(Owner, "Agent A"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteHost(); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Agents(Owner); len(a) != 0 {
		t.Fatalf("host agents left: %v", a)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.OwnerID() != Owner || s.HostOwner() != Owner || s.OwnerName(other) != "Alice" {
		t.Fatalf("reopen: %q %q %q", s.OwnerID(), s.HostOwner(), s.OwnerName(other))
	}
}

// After a handover (no computer account), nothing is deleted.
func TestDeleteHostAfterHandOverIsNoop(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	heir, _, _ := s.CreateOwner("Alice")
	if _, err := s.HandOver(heir, "host-token-0123456789"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteHost(); err != nil {
		t.Fatal(err)
	}
	if s.OwnerID() != heir || s.OwnerName(heir) != "Alice" {
		t.Fatalf("heir changed: %q", s.OwnerID())
	}
}

// Deleting the owner also deletes that agent's assembled parts, versions included.
func TestDeleteHostRemovesParts(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, _, _ := s.CreateAgent(Owner, "Agent A")
	s.PutPart(a.ID, "card", []byte(`{}`))
	s.PutPart(a.ID, "card", []byte(`{}`))
	if _, err := s.DeleteHost(); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow(`SELECT count(*) FROM ui_parts`).Scan(&n)
	if n != 0 {
		t.Fatalf("ui_parts rows left: %d", n)
	}
}
