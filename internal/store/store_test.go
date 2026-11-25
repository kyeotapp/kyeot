package store_test

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/kyeotapp/kyeot/internal/store"
)

func TestOpenMigratesOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE owners (id TEXT PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE agents (id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, name TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, state TEXT NOT NULL DEFAULT 'offline', last_seen INTEGER, created_at INTEGER NOT NULL)`,
		`CREATE TABLE tasks (id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, agent_id TEXT NOT NULL, title TEXT NOT NULL, state TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`INSERT INTO owners VALUES ('me', 'me')`,
		`INSERT INTO agents (id, owner_id, name, token_hash, created_at) VALUES ('a1', 'me', 'Agent A', 'h', 1)`,
		`INSERT INTO tasks VALUES ('t1', 'me', 'a1', 'old task', 'done', 1, 1)`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("open old db: %v", err)
	}
	defer s.Close()
	if ag, err := s.Agent("a1"); err != nil || !strings.HasPrefix(ag.Avatar, "preset:") {
		t.Fatalf("old agent avatar: %+v %v", ag, err)
	}
	task, err := s.Task("t1")
	if err != nil || task.Title != "old task" || task.RoomID != "" {
		t.Fatalf("old task: %+v %v", task, err)
	}
	if src := s.TitleSource("t1"); src != store.TitleAuto {
		t.Fatalf("old task title_source: %q", src)
	}
	if _, err := s.CreateTask("me", "a1", "routine", store.NewTask{RoutineID: "j", RunID: "r"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask("me", "a1", "routine", store.NewTask{RoutineID: "j", RunID: "r"}); err == nil {
		t.Fatal("same run stored twice")
	}
}

func TestAgentTitleNeverOverridesUser(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ag, _, _ := s.CreateAgent(store.Owner, "Agent A")
	task, _ := s.CreateTask(store.Owner, ag.ID, "first message", store.NewTask{})
	set := func(title, src string, want bool) {
		t.Helper()
		if changed, err := s.SetTaskTitle(task.ID, title, src); err != nil || changed != want {
			t.Fatalf("set %q by %s: changed=%v err=%v (want %v)", title, src, changed, err, want)
		}
	}
	set("agent title", store.TitleAgent, true)
	set("agent title", store.TitleAgent, false)
	set("my title", store.TitleUser, true)
	set("other agent title", store.TitleAgent, false)
	if got, _ := s.Task(task.ID); got.Title != "my title" || s.TitleSource(task.ID) != store.TitleUser {
		t.Fatalf("after agent retitle: %q %s", got.Title, s.TitleSource(task.ID))
	}
	set("changed again", store.TitleUser, true)
}
