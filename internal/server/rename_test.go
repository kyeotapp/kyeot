package server

import (
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/kyeotapp/kyeot/internal/store"
)

func TestRenameOthersTaskIsNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deck.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO owners (id, name) VALUES ('other', 'other')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	ag, _, err := s.CreateAgent("other", "Other's bot")
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask("other", ag.ID, "Other's chat", store.NewTask{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = newHub(s).RenameTask(store.Owner, task.ID, "Hijack")
	var ae *ActionError
	if !errors.As(err, &ae) || ae.Status != http.StatusNotFound {
		t.Fatalf("rename other owner's task: %v", err)
	}
	if got, _ := s.Task(task.ID); got.Title != "Other's chat" {
		t.Fatalf("title changed: %q", got.Title)
	}
}
