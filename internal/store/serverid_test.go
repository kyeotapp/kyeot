package store

import (
	"database/sql"
	"path/filepath"
	"regexp"
	"testing"
)

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestServerIDStaysAcrossRestartHandOverAndWipe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id := s.ServerID()
	if !hex32.MatchString(id) {
		t.Fatalf("server id %q", id)
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.ServerID(); got != id {
		t.Fatalf("restart: %q != %q", got, id)
	}
	heir, _, _ := s.CreateOwner("Alice")
	if _, err := s.HandOver(heir, "host-token-0123456789"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteHost(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WipeAll(); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.Closed() {
		t.Fatal("wipe did not close")
	}
	if got := s.ServerID(); got != id {
		t.Fatalf("after handover and wipe: %q != %q", got, id)
	}
}

// An old DB without the key creates it on first start. It differs per server.
func TestServerIDCreatedForOldDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first := s.ServerID()
	s.Close()

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM server_state WHERE key = 'server_id'`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id := s.ServerID()
	if !hex32.MatchString(id) || id == first {
		t.Fatalf("old db: %q (before %q)", id, first)
	}
	if v, _ := readState(s.db, stateServerID); v != id {
		t.Fatalf("not stored: %q != %q", v, id)
	}
}
