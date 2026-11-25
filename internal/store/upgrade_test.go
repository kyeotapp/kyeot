package store_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/kyeotapp/kyeot/internal/store"
)

// oldDB builds a DB with fewer columns from the era without a schema number (0) and inserts one conversation, room, and attachment row.
func oldDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kyeot.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE owners (id TEXT PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE agents (id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, name TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, state TEXT NOT NULL DEFAULT 'offline', last_seen INTEGER, created_at INTEGER NOT NULL)`,
		`CREATE TABLE tasks (id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, agent_id TEXT NOT NULL, title TEXT NOT NULL, state TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE events (id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT NOT NULL, source TEXT NOT NULL, type TEXT NOT NULL, payload TEXT NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE TABLE rooms (id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, agent_id TEXT NOT NULL, name TEXT NOT NULL, routine_id TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL)`,
		`CREATE TABLE uploads (id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, name TEXT NOT NULL, mime TEXT NOT NULL, size INTEGER NOT NULL, created_at INTEGER NOT NULL)`,
		`INSERT INTO owners VALUES ('me', 'me')`,
		`INSERT INTO agents (id, owner_id, name, token_hash, created_at) VALUES ('a1', 'me', 'Agent A', 'h', 1)`,
		`INSERT INTO tasks VALUES ('t1', 'me', 'a1', 'old conversation', 'done', 1, 1)`,
		`INSERT INTO events (task_id, source, type, payload, created_at) VALUES ('t1', 'user', 'message', '{"text":"hello"}', 1)`,
		`INSERT INTO rooms (id, owner_id, agent_id, name, created_at) VALUES ('r1', 'me', 'a1', 'old room', 1)`,
		`INSERT INTO uploads VALUES ('u1', 'me', 'photo.png', 'image/png', 3, 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return path
}

func userVersion(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestOpenWritesSchemaVersion(t *testing.T) {
	for name, path := range map[string]string{"old": oldDB(t), "new": filepath.Join(t.TempDir(), "kyeot.db")} {
		s, err := store.Open(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		s.Close()
		if v := userVersion(t, path); v != store.SchemaVersion {
			t.Fatalf("%s: user_version %d, want %d", name, v, store.SchemaVersion)
		}
	}
}

func fileHash(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func setUserVersion(t *testing.T, path string, v int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v)); err != nil {
		t.Fatal(err)
	}
}

func walDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kyeot.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	return path
}

func TestOpenRefusesNewerDB(t *testing.T) {
	for _, path := range []string{oldDB(t), walDB(t)} {
		refuseNewer(t, path)
	}
}

func refuseNewer(t *testing.T, path string) {
	t.Helper()
	setUserVersion(t, path, store.SchemaVersion+1)
	before := fileHash(t, path)
	s, err := store.Open(path)
	if err == nil {
		s.Close()
		t.Fatal("newer db opened")
	}
	if !errors.Is(err, store.ErrNewerDB) {
		t.Fatalf("err = %v", err)
	}
	if after := fileHash(t, path); after != before {
		t.Fatal("newer db bytes changed")
	}
	if v := userVersion(t, path); v != store.SchemaVersion+1 {
		t.Fatalf("user_version %d", v)
	}
}

func backups(t *testing.T, path string) []string {
	t.Helper()
	m, err := filepath.Glob(path + ".bak-*")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestUpgradeKeepsRowsAndBacksUpOnce(t *testing.T) {
	path := oldDB(t)
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if task, err := s.Task("t1"); err != nil || task.Title != "old conversation" {
		t.Fatalf("task: %+v %v", task, err)
	}
	if ev, err := s.Events("t1"); err != nil || len(ev) != 1 {
		t.Fatalf("events: %v %v", ev, err)
	}
	if room, _, err := s.Room("r1"); err != nil || room.Name != "old room" {
		t.Fatalf("room: %+v %v", room, err)
	}
	if up, owner, err := s.Upload("u1"); err != nil || up.Name != "photo.png" || owner != "me" {
		t.Fatalf("upload: %+v %s %v", up, owner, err)
	}
	s.Close()
	if v := userVersion(t, path); v != store.SchemaVersion {
		t.Fatalf("user_version %d", v)
	}
	baks := backups(t, path)
	if len(baks) != 1 || !strings.HasPrefix(filepath.Base(baks[0]), "kyeot.db.bak-0-") {
		t.Fatalf("backups: %v", baks)
	}
	if fi, err := os.Stat(baks[0]); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode: %v %v", fi.Mode(), err)
	}
	if v := userVersion(t, baks[0]); v != 0 {
		t.Fatalf("backup user_version %d", v)
	}
	b, err := sql.Open("sqlite", baks[0])
	if err != nil {
		t.Fatal(err)
	}
	var cols int
	b.QueryRow(`SELECT count(*) FROM pragma_table_info('tasks')`).Scan(&cols)
	var title string
	b.QueryRow(`SELECT title FROM tasks WHERE id = 't1'`).Scan(&title)
	b.Close()
	if cols != 7 || title != "old conversation" {
		t.Fatalf("backup is not the old db: %d cols, %q", cols, title)
	}

	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if baks := backups(t, path); len(baks) != 1 {
		t.Fatalf("second open made a backup: %v", baks)
	}
}

func TestNewDBNotBackedUp(t *testing.T) {
	path := walDB(t)
	if baks := backups(t, path); len(baks) != 0 {
		t.Fatalf("fresh db backed up: %v", baks)
	}
}

func TestBackupsKeepNewestThree(t *testing.T) {
	path := oldDB(t)
	old := time.Now().Add(-time.Hour)
	for i := range 4 {
		name := fmt.Sprintf("%s.bak-0-2000010%d-000000", path, i+1)
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(name, old.Add(time.Duration(i)*time.Minute), old.Add(time.Duration(i)*time.Minute))
	}
	other := path + "-wal-keep" // other names are not touched
	os.WriteFile(other, nil, 0o600)
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	baks := backups(t, path)
	names := make([]string, len(baks))
	for i, b := range baks {
		names[i] = filepath.Base(b)
	}
	if len(baks) != 3 || !slices.Contains(names, "kyeot.db.bak-0-20000103-000000") || !slices.Contains(names, "kyeot.db.bak-0-20000104-000000") {
		t.Fatalf("kept: %v", names)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("pruned an unrelated file")
	}
}

func TestReadOnlyFolderRefusesMigration(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	path := oldDB(t)
	before := fileHash(t, path)
	dir := filepath.Dir(path)
	os.Chmod(dir, 0o500)
	defer os.Chmod(dir, 0o700)
	if s, err := store.Open(path); err == nil {
		s.Close()
		t.Fatal("migrated without backup")
	}
	if after := fileHash(t, path); after != before {
		t.Fatal("db changed")
	}
	if v := userVersion(t, path); v != 0 {
		t.Fatalf("user_version %d", v)
	}
}

// Opening a version-1 DB (no list settings table) creates the table and leaves one backup.
func TestUpgradeFromV1AddsListTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kyeot.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TABLE list_prefs`, `DROP TABLE list_groups`, `PRAGMA user_version = 1`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v := userVersion(t, path); v != store.SchemaVersion {
		t.Fatalf("user_version %d", v)
	}
	if _, err := s.CreateListGroup(store.Owner, store.ListTasks, "Group"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPinned(store.Owner, store.ListTasks, "t1", true); err != nil {
		t.Fatal(err)
	}
	if baks := backups(t, path); len(baks) != 1 {
		t.Fatalf("backups: %v", baks)
	}
}

// Opening a version-3 DB (no assembled parts table) keeps the screen rows and creates ui_parts.
func TestUpgradeFromV3KeepsScreensAddsParts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kyeot.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(store.Owner, "Agent A")
	if err != nil {
		t.Fatal(err)
	}
	agentID := agent.ID
	if _, err := s.PutScreen(agentID, "money", []byte(`{"id":"money"}`)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TABLE ui_parts`, `PRAGMA user_version = 3`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if screens, err := s.Screens(agentID); err != nil || len(screens) != 1 || screens[0].ID != "money" {
		t.Fatalf("screens: %v %v", screens, err)
	}
	if v, err := s.PutPart(agentID, "money-card", []byte(`{"id":"money-card"}`)); err != nil || v != 1 {
		t.Fatalf("put part: %d %v", v, err)
	}
	if baks := backups(t, path); len(baks) != 1 || !strings.Contains(filepath.Base(baks[0]), ".bak-3-") {
		t.Fatalf("backups: %v", baks)
	}
}
