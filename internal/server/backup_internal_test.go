package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func backupFiles(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "kyeot-*.db"))
	return m
}

func TestDailyBackupAtStartAndWipedOnClose(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "kyeot.db")
	const tok = "backup-owner-token"
	s, err := New(Options{DBPath: dbPath, OwnerToken: tok})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	backups := filepath.Join(dir, "backups")
	deadline := time.Now().Add(5 * time.Second)
	for len(backupFiles(t, backups)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no daily backup was made at startup")
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.dailyBackup()
	if n := len(backupFiles(t, backups)); n != 1 {
		t.Fatalf("%d backups within 24 hours", n)
	}
	bak := dbPath + ".bak-8-20261120-010203"
	os.WriteFile(bak, []byte("old schema backup"), 0o600)

	api := httptest.NewServer(s.Handler())
	defer api.Close()
	req, _ := http.NewRequest("POST", api.URL+"/api/server/close", strings.NewReader(`{"confirm":"`+confirmWord+`"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("server close: %d", resp.StatusCode)
	}
	if n := len(backupFiles(t, backups)); n != 0 {
		t.Fatalf("%d daily backups remained after closing", n)
	}
	if _, err := os.Stat(bak); !os.IsNotExist(err) {
		t.Fatal("a schema-change backup remained after closing")
	}
	os.RemoveAll(backups)
	s.dailyBackup()
	if n := len(backupFiles(t, backups)); n != 0 {
		t.Fatal("a closed server made a backup")
	}
}
