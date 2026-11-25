package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kyeotapp/kyeot/internal/store"
)

func newLocal(t *testing.T) *Server {
	srv, err := New(Options{DBPath: ":memory:", OwnerToken: "test-owner-token-0123456789", UploadDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

func agentCount(t *testing.T, srv *Server) int {
	agents, err := srv.store.Agents(store.Owner)
	if err != nil {
		t.Fatal(err)
	}
	return len(agents)
}

func TestLocalAgentKeepsIDRotatesTokenAndHonorsUnlink(t *testing.T) {
	srv := newLocal(t)
	path := filepath.Join(t.TempDir(), "sub", "hermes-agent")

	first, err := srv.LocalAgent("Hermes", path)
	if err != nil || first == "" {
		t.Fatalf("create: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("id file should be 0600: %v %v", fi, err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), first) {
		t.Fatal("token must not be written to disk")
	}

	second, err := srv.LocalAgent("Hermes", path)
	if err != nil || second == first {
		t.Fatalf("restart should issue a new token: %v", err)
	}
	if _, err := srv.store.AgentByToken(first); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("old token should be invalid after rotation")
	}
	if agentCount(t, srv) != 1 {
		t.Fatal("restart should reuse the same agent")
	}

	a, _ := srv.store.AgentByToken(second)
	if err := srv.hub.UnlinkAgent(store.Owner, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.LocalAgent("Hermes", path); !errors.Is(err, ErrLocalAgentUnlinked) {
		t.Fatalf("unlinked agent must stay unlinked: %v", err)
	}
	if agentCount(t, srv) != 0 {
		t.Fatal("no new agent after unlink")
	}

	os.Remove(path) // if the user deletes the file, it attaches again
	if _, err := srv.LocalAgent("Hermes", path); err != nil || agentCount(t, srv) != 1 {
		t.Fatalf("deleting the file should allow a fresh agent: %v", err)
	}
}

func TestLocalAgentMigratesLegacyTokenFile(t *testing.T) {
	srv := newLocal(t)
	a, legacy, err := srv.hub.CreateAgent(store.Owner, "Agent A")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "legacy-token")
	os.WriteFile(path, []byte(legacy+"\n"), 0o644)

	token, err := srv.LocalAgent("Hermes", path)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := srv.store.AgentByToken(token); got.ID != a.ID || agentCount(t, srv) != 1 {
		t.Fatal("legacy token should continue the same agent")
	}
	b, _ := os.ReadFile(path)
	if strings.TrimSpace(string(b)) != a.ID {
		t.Fatalf("file should now hold the id, got %q", b)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("loose permission should be tightened: %v", fi.Mode())
	}
}

func TestLocalAgentRefusesSymlink(t *testing.T) {
	srv := newLocal(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "elsewhere"), nil, 0o600)
	link := filepath.Join(dir, "hermes-agent")
	os.Symlink(filepath.Join(dir, "elsewhere"), link)
	if _, err := srv.LocalAgent("Hermes", link); err == nil {
		t.Fatal("symlink should be refused")
	}
	if agentCount(t, srv) != 0 {
		t.Fatal("no agent on refusal")
	}
}

func TestLocalAgentRefusesUnknownFileContent(t *testing.T) {
	srv := newLocal(t)
	a, legacy, _ := srv.hub.CreateAgent(store.Owner, "Agent A")
	srv.hub.UnlinkAgent(store.Owner, a.ID)
	path := filepath.Join(t.TempDir(), "legacy-token")
	for _, content := range []string{legacy, "garbage"} { // an unlinked agent's old token, garbage
		os.WriteFile(path, []byte(content+"\n"), 0o600)
		if _, err := srv.LocalAgent("Hermes", path); err == nil {
			t.Fatalf("%q: should refuse instead of creating a new agent", content)
		}
		if b, _ := os.ReadFile(path); strings.TrimSpace(string(b)) != content {
			t.Fatal("file must stay untouched on refusal")
		}
	}
	if agentCount(t, srv) != 0 {
		t.Fatal("no agent should be created")
	}
	os.WriteFile(path, []byte(strings.ToUpper(a.ID)), 0o600) // an uppercase id is treated as the same (unlinked) agent
	if _, err := srv.LocalAgent("Hermes", path); !errors.Is(err, ErrLocalAgentUnlinked) {
		t.Fatalf("uppercase id: %v", err)
	}
}
