package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func openAt(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kyeot.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func setClock(t *testing.T, at *time.Time) {
	t.Helper()
	backupClock = func() time.Time { return *at }
	t.Cleanup(func() { backupClock = time.Now })
}

func ownersIn(t *testing.T, path string) int {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM owners`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDailyBackupOncePerDayKeepsSeven(t *testing.T) {
	s, path := openAt(t)
	dir := filepath.Join(filepath.Dir(path), "backups")
	now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	setClock(t, &now)

	first, err := s.DailyBackup(dir)
	if err != nil || first == "" {
		t.Fatalf("first backup: %q %v", first, err)
	}
	if fi, _ := os.Stat(first); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("permissions %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(dir); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700 {
		t.Fatalf("folder permissions %v", fi.Mode().Perm())
	}
	if n := ownersIn(t, first); n == 0 {
		t.Fatal("no rows in backup")
	}
	now = now.Add(23 * time.Hour)
	if again, err := s.DailyBackup(dir); err != nil || again != "" {
		t.Fatalf("made another within 24 hours: %q %v", again, err)
	}
	for i := range 9 {
		now = now.Add(25 * time.Hour)
		name, err := s.DailyBackup(dir)
		if err != nil || name == "" {
			t.Fatalf("#%d: %q %v", i, name, err)
		}
	}
	if got := len(dailyBackups(dir)); got != keepDaily {
		t.Fatalf("%d backups left, want %d", got, keepDaily)
	}
	if _, err := os.Stat(first); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("the oldest backup was kept")
	}
}

func TestDailyBackupSkipsMemoryDB(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dir := filepath.Join(t.TempDir(), "backups")
	if name, err := s.DailyBackup(dir); name != "" || err != nil {
		t.Fatalf("%q %v", name, err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("created a folder")
	}
}

func TestStalePartIsRemovedAndNotCounted(t *testing.T) {
	s, path := openAt(t)
	dir := filepath.Dir(path)
	part := filepath.Join(dir, partPrefix+"left")
	if err := os.WriteFile(part, []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(part, old, old)
	if _, err := backup(s.db, path, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(part); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("did not delete the leftover temp file")
	}
	for _, e := range mustRead(t, dir) {
		if len(e) > len(partPrefix) && e[:len(partPrefix)] == partPrefix {
			t.Fatalf("temp file left: %s", e)
		}
	}
}

func mustRead(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestSnapshotWhileOpenAndRefusesExisting(t *testing.T) {
	_, path := openAt(t)
	dst := filepath.Join(t.TempDir(), "copy.db")
	ver, err := Snapshot(path, dst)
	if err != nil || ver != SchemaVersion {
		t.Fatalf("%d %v", ver, err)
	}
	if ownersIn(t, dst) == 0 {
		t.Fatal("no rows in the copy")
	}
	if _, err := Snapshot(path, dst); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("overwrote an existing file: %v", err)
	}
	if v, err := CheckFile(dst); err != nil || v != SchemaVersion {
		t.Fatalf("CheckFile %d %v", v, err)
	}
}

func TestCheckFileRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	os.WriteFile(path, []byte("SQLite format 3\x00 this is not a DB"), 0o600)
	if _, err := CheckFile(path); err == nil {
		t.Fatal("accepted a corrupt file")
	}
}

func TestOpenMakesDBPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no permission bits")
	}
	path := filepath.Join(t.TempDir(), "kyeot.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	os.Chmod(path, 0o644)
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, p := range []string{path, path + "-wal"} {
		if fi, err := os.Stat(p); err == nil && fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s permissions %v", filepath.Base(p), fi.Mode().Perm())
		}
	}
}

func TestRemoveBackups(t *testing.T) {
	s, path := openAt(t)
	dir := filepath.Join(filepath.Dir(path), "backups")
	if _, err := s.DailyBackup(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := backup(s.db, path, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveBackups(dir); err != nil {
		t.Fatal(err)
	}
	if n := len(dailyBackups(dir)) + len(backupsIn(filepath.Dir(path), "kyeot.db.bak-")); n != 0 {
		t.Fatalf("%d backups left", n)
	}
}

func TestRemoveBackupsTakesRestoreBundlesAndLeftovers(t *testing.T) {
	s, path := openAt(t)
	data := filepath.Dir(path)
	dir := filepath.Join(data, "backups")
	os.MkdirAll(filepath.Join(data, ".restore-x"), 0o700)
	os.MkdirAll(filepath.Join(data, ".backup-y"), 0o700)
	os.WriteFile(filepath.Join(data, ".restore-x", "kyeot.db"), []byte("copy"), 0o600)
	os.WriteFile(filepath.Join(data, partPrefix+"z"), []byte("half"), 0o600)
	os.MkdirAll(dir, 0o700)
	restore := filepath.Join(dir, RestorePrefix+"20261125-010203.tar.gz")
	os.WriteFile(restore, []byte("bundle"), 0o600)
	mine := filepath.Join(dir, "my-own.kyeot")
	os.WriteFile(mine, []byte("user file"), 0o600)
	if err := s.RemoveBackups(dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{restore, filepath.Join(data, ".restore-x"), filepath.Join(data, ".backup-y"), filepath.Join(data, partPrefix+"z")} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s left", filepath.Base(p))
		}
	}
	if _, err := os.Stat(mine); err != nil {
		t.Fatal("deleted files outside the naming rule too")
	}
}

func TestDailyBackupIgnoresFutureName(t *testing.T) {
	s, path := openAt(t)
	dir := filepath.Join(filepath.Dir(path), "backups")
	now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.Local)
	setClock(t, &now)
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, dailyPrefix+"20270101-000000.db"), []byte("when the clock ran ahead"), 0o600)
	if name, err := s.DailyBackup(dir); err != nil || name == "" {
		t.Fatalf("stopped by a future name: %q %v", name, err)
	}
}
