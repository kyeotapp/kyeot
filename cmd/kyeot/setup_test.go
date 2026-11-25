package main

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// isolate makes a temp environment with HOME and kyeot environment variables cleared.
func isolate(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	for _, name := range []string{"OWNER_TOKEN", "DB", "NETS", "ADDR"} {
		t.Setenv("KYEOT_"+name, "")
		t.Setenv("AGENT_DECK_"+name, "")
	}
	return home
}

func perm(t *testing.T, path string) fs.FileMode {
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestOwnerTokenCreatedOncePrivate(t *testing.T) {
	isolate(t)
	if _, err := readOwnerToken(); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("without a token it must be ErrNotExist: %v", err)
	}
	first, created, err := ownerToken()
	if err != nil || !created || len(first) < 32 {
		t.Fatalf("first run: created=%v len=%d err=%v", created, len(first), err)
	}
	dir, _ := configDir()
	file := filepath.Join(dir, "owner-token")
	if p := perm(t, file); p != 0o600 {
		t.Errorf("file mode %v, must be 600", p)
	}
	if p := perm(t, dir); p != 0o700 {
		t.Errorf("folder mode %v, must be 700", p)
	}
	second, created, err := ownerToken()
	if err != nil || created || second != first {
		t.Fatalf("the second run must read the same token: created=%v same=%v err=%v", created, second == first, err)
	}
	if got, err := readOwnerToken(); err != nil || got != first {
		t.Fatal("kyeot pair must read the same file")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("a temp file remains: %v", entries)
	}
}

func TestOwnerTokenEnvWins(t *testing.T) {
	isolate(t)
	t.Setenv("KYEOT_OWNER_TOKEN", "fake-env-token-0123456789")
	got, created, err := ownerToken()
	if err != nil || created || got != "fake-env-token-0123456789" {
		t.Fatalf("the environment variable must win: created=%v err=%v", created, err)
	}
	dir, _ := configDir()
	if _, err := os.Stat(filepath.Join(dir, "owner-token")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("with the environment variable, no file must be created")
	}
	t.Setenv("KYEOT_OWNER_TOKEN", "short")
	if _, _, err := ownerToken(); err == nil {
		t.Error("under 16 characters must be rejected")
	}
}

func TestOwnerTokenReadsLegacyFile(t *testing.T) {
	home := isolate(t)
	dir, _ := configDir()
	if dir == filepath.Join(home, ".config", "kyeot") {
		t.Skip("the config folder is the same as the old location")
	}
	old := filepath.Join(home, ".config", "kyeot", "owner-token")
	os.MkdirAll(filepath.Dir(old), 0o700)
	os.WriteFile(old, []byte("fake-legacy-token-0123456789\n"), 0o600)
	got, created, err := ownerToken()
	if err != nil || created || got != "fake-legacy-token-0123456789" {
		t.Fatalf("the old-location token must be kept: created=%v err=%v", created, err)
	}
}

func TestDefaultNetsBlocksPublic(t *testing.T) {
	isolate(t)
	nets, err := allowNets()
	if err != nil {
		t.Fatal(err)
	}
	in := func(s string) bool {
		for _, n := range nets {
			if n.Contains(net.ParseIP(s)) {
				return true
			}
		}
		return false
	}
	for ip, want := range map[string]bool{"127.0.0.1": true, "100.101.1.2": true, "192.168.0.7": true, "8.8.8.8": false, "2001:db8::1": false} { // public-ok
		if in(ip) != want {
			t.Errorf("%s: got %v want %v", ip, !want, want)
		}
	}
	t.Setenv("KYEOT_NETS", "all")
	if nets, _ = allowNets(); !in("8.8.8.8") {
		t.Error("with KYEOT_NETS=all everything must be accepted")
	}
}

func TestDBDefaultsToConfigDir(t *testing.T) {
	isolate(t)
	t.Chdir(t.TempDir())
	got, err := dbFile()
	dir, _ := configDir()
	if err != nil || got != filepath.Join(dir, "kyeot.db") {
		t.Fatalf("got %q err=%v", got, err)
	}
	if p := perm(t, dir); p != 0o700 {
		t.Errorf("folder mode %v", p)
	}
	want := filepath.Join(t.TempDir(), "x", "my.db")
	t.Setenv("KYEOT_DB", want)
	if got, err := dbFile(); err != nil || got != want {
		t.Fatalf("KYEOT_DB must win: %q %v", got, err)
	}
}

func TestPairServerHost(t *testing.T) {
	if got, _ := pairServer("192.168.0.9:7700", ""); got != "http://192.168.0.9:7700" { // public-ok
		t.Errorf("listening on a specific address gives that address: %s", got)
	}
	if got, _ := pairServer("0.0.0.0:7700", "100.64.0.1"); got != "http://100.64.0.1:7700" { // public-ok
		t.Errorf("-host wins: %s", got)
	}
}

// A data/kyeot.db in the working folder must never be used: it opened a running server's DB once.
func TestDBIgnoresWorkdirFile(t *testing.T) {
	isolate(t)
	t.Chdir(t.TempDir())
	os.MkdirAll("data", 0o700)
	os.WriteFile(legacyDB, []byte("x"), 0o600)
	dir, _ := configDir()
	if got, err := dbFile(); err != nil || got != filepath.Join(dir, "kyeot.db") {
		t.Fatalf("must use the config folder, not the working folder: %q %v", got, err)
	}
	t.Setenv("KYEOT_DB", "elsewhere.db")
	if got, _ := dbFile(); got != "elsewhere.db" {
		t.Fatalf("KYEOT_DB wins: %q", got)
	}
}

func TestOwnerTokenLosesRaceKeepsExisting(t *testing.T) {
	isolate(t)
	dir, _ := configDir()
	file := filepath.Join(dir, "owner-token")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(file, []byte("fake-winner-token-0123456789\n"), 0o600)
	if err := createPrivate(file, "fake-loser-token-0123456789\n"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("if it exists, it must be ErrExist: %v", err)
	}
	if b, _ := os.ReadFile(file); string(b) != "fake-winner-token-0123456789\n" {
		t.Fatalf("overwrote the token made first: %q", b)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("a temp file remains: %v", entries)
	}
	got, created, err := ownerToken()
	if err != nil || created || got != "fake-winner-token-0123456789" {
		t.Fatalf("the existing token must be used: created=%v err=%v", created, err)
	}
}

func TestServerNetsKeepsLoopbackForHermes(t *testing.T) {
	isolate(t)
	has := func(nets []*net.IPNet) bool {
		return slices.ContainsFunc(nets, func(n *net.IPNet) bool { return n.Contains(net.ParseIP("127.0.0.1")) })
	}
	t.Setenv("KYEOT_NETS", "lan")
	nets, err := serverNets()
	if err != nil || !has(nets) {
		t.Fatalf("loopback must be added so the in-server adapter does not get 403: %v", err)
	}
	if nets, _ := serverNets(); slices.ContainsFunc(nets, func(n *net.IPNet) bool { return n.Contains(net.ParseIP("8.8.8.8")) }) {
		t.Error("nothing is widened except loopback")
	}
	t.Setenv("KYEOT_HERMES", "off")
	if nets, _ := serverNets(); has(nets) {
		t.Error("with the adapter off, KYEOT_NETS stays as is")
	}
}
