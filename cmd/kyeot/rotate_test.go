package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kyeotapp/kyeot/internal/server"
)

// rotateHome points the config folder under a temp HOME and creates one owner token.
func rotateHome(t *testing.T) (tokenFile, keyFile, token string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".xdg"))
	t.Setenv("KYEOT_OWNER_TOKEN", "")
	t.Setenv("AGENT_DECK_OWNER_TOKEN", "")
	token, created, err := ownerToken()
	if err != nil || !created {
		t.Fatalf("token: %v %v", created, err)
	}
	files, _ := tokenFiles()
	kf, _ := uploadKeyFile()
	return files[0], kf, token
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

func TestUploadKeyStartsFromLegacyToken(t *testing.T) {
	_, kf, token := rotateHome(t)
	key, created, err := uploadKey(token)
	if err != nil || !created {
		t.Fatalf("first: %v %v", created, err)
	}
	if !bytes.Equal(key, server.LegacyUploadKey(token)) {
		t.Fatal("the first key made must equal the key the token used to give, so old attachment URLs work")
	}
	if m := mode(t, kf); m != 0o600 {
		t.Fatalf("mode %o", m)
	}
	again, created, err := uploadKey("other-token-0123456789")
	if err != nil || created || !bytes.Equal(again, key) {
		t.Fatal("an existing key file is used as is, regardless of the token")
	}
}

// A run given the token by environment variable, like the test server, does not create an attachment key in the user config folder (11-18 incident: the real server's key was overwritten by the test token's key)
func TestUploadKeyEnvTokenTouchesNoFile(t *testing.T) {
	_, kf, _ := rotateHome(t)
	os.Remove(kf)
	t.Setenv("KYEOT_OWNER_TOKEN", "test-token-0123456789abcdef")
	key, created, err := uploadKey("test-token-0123456789abcdef")
	if err != nil || created || !bytes.Equal(key, server.LegacyUploadKey("test-token-0123456789abcdef")) {
		t.Fatalf("an environment-variable token gives an in-memory key: %v %v", created, err)
	}
	if _, err := os.Stat(kf); !os.IsNotExist(err) {
		t.Fatal("an environment-variable token run created an attachment key file")
	}

	// even if the real server's key file already exists, it is neither read nor changed
	os.MkdirAll(filepath.Dir(kf), 0o700)
	real := strings.Repeat("ab", 32) + "\n"
	os.WriteFile(kf, []byte(real), 0o600)
	if key, _, _ := uploadKey("test-token-0123456789abcdef"); bytes.Equal(key, bytes.Repeat([]byte{0xab}, 32)) {
		t.Fatal("an environment-variable token run read the real server's key file")
	}
	if b, _ := os.ReadFile(kf); string(b) != real {
		t.Fatal("the real server's key file changed")
	}
}

func TestRotateOwnerTokenKeepsUploadKey(t *testing.T) {
	tf, kf, old := rotateHome(t)
	var out bytes.Buffer
	if err := rotateFlow(true, false, false, "", strings.NewReader(""), &out, func(io.Writer) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	now, err := readOwnerToken()
	if err != nil || now == old || len(now) < 40 {
		t.Fatalf("the token must change: %v", err)
	}
	if m := mode(t, tf); m != 0o600 {
		t.Fatalf("token mode %o", m)
	}
	key, err := readUploadKey(kf)
	if err != nil || !bytes.Equal(key, server.LegacyUploadKey(old)) {
		t.Fatalf("before reissuing, the old token's key must be fixed: %v", err)
	}
	if s := out.String(); strings.Contains(s, now) || strings.Contains(s, old) || !strings.Contains(s, "QR") || !strings.Contains(s, "다시 켜야") {
		t.Fatalf("출력: %q", s)
	}
}

func TestRotateUploadsAsksFirst(t *testing.T) {
	_, kf, token := rotateHome(t)
	before, _, _ := uploadKey(token)
	restart := func(io.Writer) (bool, error) { return true, nil }
	var out bytes.Buffer
	if err := rotateFlow(false, true, false, "", strings.NewReader("n\n"), &out, restart); err == nil {
		t.Fatal("an error when refused")
	}
	if k, _ := readUploadKey(kf); !bytes.Equal(k, before) {
		t.Fatal("when refused, the key stays")
	}
	out.Reset()
	if err := rotateFlow(false, true, false, "", strings.NewReader("y\n"), &out, restart); err != nil {
		t.Fatal(err)
	}
	k, err := readUploadKey(kf)
	if err != nil || bytes.Equal(k, before) {
		t.Fatalf("the key must change: %v", err)
	}
	if now, _ := readOwnerToken(); now != token {
		t.Fatal("only the attachment key changes")
	}
	if !strings.Contains(out.String(), "다시 켰어요") {
		t.Fatalf("출력: %q", out.String())
	}
}

func TestRotateRefusesEnvToken(t *testing.T) {
	rotateHome(t)
	t.Setenv("KYEOT_OWNER_TOKEN", "env-owner-token-0123456789")
	if err := rotateOwnerToken(); err == nil || !strings.Contains(err.Error(), "KYEOT_OWNER_TOKEN") {
		t.Fatalf("an environment-variable token cannot be changed: %v", err)
	}
}
