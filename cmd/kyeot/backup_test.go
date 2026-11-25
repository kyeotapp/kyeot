package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/store"
)

type backupSide struct {
	plan backupPlan
	dir  string
}

func newSide(t *testing.T) backupSide {
	t.Helper()
	dir := t.TempDir()
	keys := filepath.Join(dir, "conf")
	return backupSide{dir: dir, plan: backupPlan{
		dbPath:      filepath.Join(dir, "kyeot.db"),
		dataDir:     dir,
		keys:        map[string]string{"owner-token": filepath.Join(keys, "owner-token"), "uploads-key": filepath.Join(keys, "uploads-key")},
		running:     func() string { return "" },
		lock:        func() (func(), error) { return lockData(filepath.Join(dir, "kyeot.db")) },
		interactive: func() bool { return false },
		now:         func() time.Time { return time.Date(2026, 11, 25, 1, 2, 3, 0, time.Local) },
	}}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// seed creates an agent named name in the DB.
func (s backupSide) seed(t *testing.T, name string) {
	t.Helper()
	st, err := store.Open(s.plan.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, _, err := st.CreateAgent(store.Owner, name); err != nil {
		t.Fatal(err)
	}
}

func (s backupSide) agents(t *testing.T) []string {
	t.Helper()
	st, err := store.Open(s.plan.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	list, err := st.Agents(store.Owner)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, a := range list {
		out = append(out, a.Name)
	}
	return out
}

func TestBackupRestoreRoundTrip(t *testing.T) {
	a := newSide(t)
	a.seed(t, "Agent A")
	write(t, filepath.Join(a.dir, "uploads", "me", "u1"), "attachment")
	write(t, filepath.Join(a.dir, "sensors", "netwatch", "2026-11-25.log"), "measurement")
	write(t, filepath.Join(a.dir, "updates", "files", "abc.js"), "bundle")
	write(t, filepath.Join(a.dir, "updates", "private-key.pem"), "must not be included")
	write(t, filepath.Join(a.dir, "link-previews", "x.png"), "cache")
	write(t, filepath.Join(a.dir, "connect.json"), "other role")
	write(t, filepath.Join(a.dir, "hermes-agent"), "agent-1")
	write(t, filepath.Join(a.dir, "hermes-agent.work"), "agent-2")
	write(t, a.plan.keys["owner-token"], "owner-token-aaaaaaaaaaaa")
	write(t, a.plan.keys["uploads-key"], "upload-key")

	bundle := filepath.Join(t.TempDir(), "kyeot.backup")
	var out bytes.Buffer
	if err := runBackup(a.plan, []string{bundle}, &out); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(bundle); fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup file mode %v", fi.Mode().Perm())
	}
	if err := runBackup(a.plan, []string{bundle}, &out); err == nil {
		t.Fatal("overwrote an existing file")
	}

	b := newSide(t)
	b.seed(t, "Agent B")
	write(t, filepath.Join(b.dir, "uploads", "me", "old"), "old attachment")
	write(t, filepath.Join(b.dir, "hermes-agent"), "agent-old")
	write(t, b.plan.keys["owner-token"], "owner-token-bbbbbbbbbbbb")
	if err := runRestore(b.plan, []string{bundle}, strings.NewReader(""), &out); err == nil {
		t.Fatal("replaced without --yes although it could not ask")
	}
	if got := b.agents(t); len(got) != 1 || got[0] != "Agent B" {
		t.Fatalf("changed although refused: %v", got)
	}
	if err := runRestore(b.plan, []string{"--yes", bundle}, strings.NewReader(""), &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if got := b.agents(t); len(got) != 1 || got[0] != "Agent A" {
		t.Fatalf("restored agents %v", got)
	}
	for rel, want := range map[string]string{
		"uploads/me/u1":                   "attachment",
		"sensors/netwatch/2026-11-25.log": "measurement",
		"updates/files/abc.js":            "bundle",
		"hermes-agent":                    "agent-1",
		"hermes-agent.work":               "agent-2",
	} {
		if got := read(t, filepath.Join(b.dir, rel)); got != want {
			t.Fatalf("%s = %q", rel, got)
		}
	}
	for _, rel := range []string{"uploads/me/old", "updates/private-key.pem", "link-previews/x.png", "connect.json"} {
		if _, err := os.Stat(filepath.Join(b.dir, rel)); err == nil {
			t.Fatalf("%s exists", rel)
		}
	}
	if read(t, b.plan.keys["owner-token"]) != "owner-token-aaaaaaaaaaaa" || read(t, b.plan.keys["uploads-key"]) != "upload-key" {
		t.Fatal("keys were not restored")
	}
	before := filepath.Join(b.dir, "backups", "before-restore-20261125-010203.tar.gz")
	c := newSide(t)
	if err := runRestore(c.plan, []string{"--yes", before}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if got := c.agents(t); len(got) != 1 || got[0] != "Agent B" {
		t.Fatalf("the pre-restore backup is not the old data: %v", got)
	}
	if read(t, filepath.Join(c.dir, "uploads", "me", "old")) != "old attachment" {
		t.Fatal("the pre-restore backup lacks the old attachment")
	}
}

func TestRestoreRefusesWhileServerRuns(t *testing.T) {
	a := newSide(t)
	a.seed(t, "Agent A")
	bundle := filepath.Join(t.TempDir(), "b")
	if err := runBackup(a.plan, []string{bundle}, io.Discard); err != nil {
		t.Fatal(err)
	}
	b := newSide(t)
	b.seed(t, "Agent B")
	b.plan.running = func() string { return "running" }
	if err := runRestore(b.plan, []string{"--yes", bundle}, strings.NewReader(""), io.Discard); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("replaced while the server runs: %v", err)
	}
	if got := b.agents(t); got[0] != "Agent B" {
		t.Fatal("changed")
	}
}

func TestRestoreRefusesNewerSchema(t *testing.T) {
	a := newSide(t)
	a.seed(t, "Agent A")
	db, err := sql.Open("sqlite", a.plan.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, store.SchemaVersion+1))
	db.Close()
	bundle := filepath.Join(t.TempDir(), "b")
	if err := runBackup(a.plan, []string{bundle}, io.Discard); err != nil {
		t.Fatal(err)
	}
	b := newSide(t)
	b.seed(t, "Agent B")
	if err := runRestore(b.plan, []string{"--yes", bundle}, strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("restored a newer schema")
	}
	if got := b.agents(t); got[0] != "Agent B" {
		t.Fatal("changed")
	}
}

// evilBundle appends one entry after a real backup.
func evilBundle(t *testing.T, name string, typ byte) string {
	t.Helper()
	a := newSide(t)
	a.seed(t, "Agent A")
	good := filepath.Join(t.TempDir(), "good")
	if err := runBackup(a.plan, []string{good}, io.Discard); err != nil {
		t.Fatal(err)
	}
	src, _ := os.Open(good)
	defer src.Close()
	zr, _ := gzip.NewReader(src)
	tr := tar.NewReader(zr)
	path := filepath.Join(t.TempDir(), "evil")
	f, _ := os.Create(path)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		tw.WriteHeader(h)
		io.Copy(tw, tr)
	}
	body := []byte("x")
	h := &tar.Header{Name: name, Mode: 0o600, Typeflag: typ, Linkname: "/etc/passwd"}
	if typ == tar.TypeReg {
		h.Size = int64(len(body))
	}
	tw.WriteHeader(h)
	if typ == tar.TypeReg {
		tw.Write(body)
	}
	tw.Close()
	gz.Close()
	f.Close()
	return path
}

func TestRestoreRejectsBadEntries(t *testing.T) {
	for _, c := range []struct {
		name string
		typ  byte
	}{
		{"data/../../evil", tar.TypeReg},
		{"/abs", tar.TypeReg},
		{"data/uploads/../../../x", tar.TypeReg},
		{"data/link-previews/x", tar.TypeReg},
		{"data/updates/private-key.pem", tar.TypeReg},
		{"keys/release-key", tar.TypeReg},
		{"data/uploads/link", tar.TypeSymlink},
	} {
		b := newSide(t)
		b.seed(t, "Agent B")
		evil := evilBundle(t, c.name, c.typ)
		if err := runRestore(b.plan, []string{"--yes", evil}, strings.NewReader(""), io.Discard); err == nil || !strings.Contains(err.Error(), "받을 수 없는 항목") {
			t.Fatalf("%q: %v", c.name, err)
		}
		if got := b.agents(t); got[0] != "Agent B" {
			t.Fatalf("%q: changed", c.name)
		}
	}
}

func TestServerRunningSeesListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	h := host{} // no registration
	if why := serverRunning(h, ln.Addr().String()); why == "" {
		t.Fatal("did not see the listening port")
	}
}

// Makes a bundle, changes only the manifest (kyeot-backup.json) and repacks it.
func rewriteManifest(t *testing.T, src string, edit func(m *manifest)) string {
	t.Helper()
	in, _ := os.Open(src)
	defer in.Close()
	zr, _ := gzip.NewReader(in)
	tr := tar.NewReader(zr)
	path := filepath.Join(t.TempDir(), "edited")
	f, _ := os.Create(path)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		body, _ := io.ReadAll(tr)
		if h.Name == bundleManifest {
			var m manifest
			json.Unmarshal(body, &m)
			edit(&m)
			body, _ = json.Marshal(m)
			h.Size = int64(len(body))
		}
		tw.WriteHeader(h)
		tw.Write(body)
	}
	tw.Close()
	gz.Close()
	f.Close()
	return path
}

func TestRestoreRefusesManifestKeyMismatch(t *testing.T) {
	a := newSide(t)
	a.seed(t, "Agent A")
	bundle := filepath.Join(t.TempDir(), "b")
	if err := runBackup(a.plan, []string{bundle}, io.Discard); err != nil { // no key file, so keys/ is empty
		t.Fatal(err)
	}
	lying := rewriteManifest(t, bundle, func(m *manifest) { m.Keys = []string{"owner-token"} })
	b := newSide(t)
	b.seed(t, "Agent B")
	write(t, filepath.Join(b.dir, "uploads", "x"), "current attachment")
	if err := runRestore(b.plan, []string{"--yes", lying}, strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("accepted a bundle that differs from its manifest")
	}
	if got := b.agents(t); got[0] != "Agent B" || read(t, filepath.Join(b.dir, "uploads", "x")) != "current attachment" {
		t.Fatal("deleted although refused")
	}
}

func TestRestoreRefusesWhileDataLocked(t *testing.T) {
	a := newSide(t)
	a.seed(t, "Agent A")
	bundle := filepath.Join(t.TempDir(), "b")
	if err := runBackup(a.plan, []string{bundle}, io.Discard); err != nil {
		t.Fatal(err)
	}
	b := newSide(t)
	b.seed(t, "Agent B")
	release, err := lockData(b.plan.dbPath) // a server running on another port
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := runRestore(b.plan, []string{"--yes", bundle}, strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("replaced although a server holds the lock")
	}
	if got := b.agents(t); got[0] != "Agent B" {
		t.Fatal("changed")
	}
}

func TestRestoreBacksUpEvenWithoutDB(t *testing.T) {
	a := newSide(t)
	a.seed(t, "Agent A")
	bundle := filepath.Join(t.TempDir(), "b")
	if err := runBackup(a.plan, []string{bundle}, io.Discard); err != nil {
		t.Fatal(err)
	}
	b := newSide(t)
	write(t, filepath.Join(b.dir, "uploads", "x"), "attachment left without a DB")
	write(t, b.plan.keys["owner-token"], "owner-token-bbbbbbbbbbbb")
	if err := runRestore(b.plan, []string{"--yes", bundle}, strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	m, _ := filepath.Glob(filepath.Join(b.dir, "backups", store.RestorePrefix+"*"))
	if len(m) != 1 {
		t.Fatalf("had something to delete but did not back up first: %v", m)
	}
}

func TestRestoreRefusesLinkedDataDir(t *testing.T) {
	a := newSide(t)
	a.seed(t, "Agent A")
	bundle := filepath.Join(t.TempDir(), "b")
	if err := runBackup(a.plan, []string{bundle}, io.Discard); err != nil {
		t.Fatal(err)
	}
	b := newSide(t)
	b.seed(t, "Agent B")
	outside := t.TempDir()
	write(t, filepath.Join(outside, "keep"), "file outside")
	if err := os.Symlink(outside, filepath.Join(b.dir, "uploads")); err != nil {
		t.Skip(err)
	}
	if err := runRestore(b.plan, []string{"--yes", bundle}, strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("deleted by following a linked folder")
	}
	if read(t, filepath.Join(outside, "keep")) != "file outside" {
		t.Fatal("deleted outside the data folder")
	}
}

func TestRestoreKeepsThreeBeforeBundles(t *testing.T) {
	a := newSide(t)
	a.seed(t, "Agent A")
	bundle := filepath.Join(t.TempDir(), "b")
	if err := runBackup(a.plan, []string{bundle}, io.Discard); err != nil {
		t.Fatal(err)
	}
	b := newSide(t)
	b.seed(t, "Agent B")
	at := time.Date(2026, 11, 25, 1, 0, 0, 0, time.Local)
	for i := range 5 {
		b.plan.now = func() time.Time { return at.Add(time.Duration(i) * time.Minute) }
		if err := runRestore(b.plan, []string{"--yes", bundle}, strings.NewReader(""), io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	m, _ := filepath.Glob(filepath.Join(b.dir, "backups", store.RestorePrefix+"*"))
	if len(m) != keepRestoreBundles || !strings.Contains(m[len(m)-1], "010400") {
		t.Fatalf("remaining bundles %v", m)
	}
}

func TestServerWaitsForDataLock(t *testing.T) {
	db := filepath.Join(t.TempDir(), "kyeot.db")
	release, err := lockData(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := waitLockData(db, 300*time.Millisecond); !errors.Is(err, errDataBusy) {
		t.Fatalf("took a lock that is already held: %v", err)
	}
	go func() { time.Sleep(200 * time.Millisecond); release() }()
	again, err := waitLockData(db, 3*time.Second)
	if err != nil {
		t.Fatalf("could not wait for and take the released lock: %v", err)
	}
	again()
}
