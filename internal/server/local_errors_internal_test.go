package server

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// With a fresh DB the id file points to a missing agent, or when the file is empty, a new agent is created and the file is rewritten.
func TestLocalAgentRecoversFromStaleIDFile(t *testing.T) {
	for name, content := range map[string]string{
		"unknown id":  uuid.NewString() + "\n",
		"blank":       "  \n\t",
		"uppercase":   strings.ToUpper(uuid.NewString()),
		"crlf ending": uuid.NewString() + "\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			srv := newLocal(t)
			path := filepath.Join(t.TempDir(), "hermes-agent")
			os.WriteFile(path, []byte(content), 0o600)
			token, err := srv.LocalAgent("Hermes", path)
			if err != nil || token == "" {
				t.Fatalf("should create a new agent: %v", err)
			}
			a, err := srv.store.AgentByToken(token)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(path)
			if strings.TrimSpace(string(b)) != a.ID {
				t.Fatalf("file should now hold the new id: %q", b)
			}
			if agentCount(t, srv) != 1 {
				t.Fatal("exactly one agent")
			}
		})
	}
}

// Pressing connect again gives the same id without starting a new adapter. Even if Hermes dies meanwhile (probe fails), the existing connection stays.
func TestLocalHermesSecondConnectAfterHermesDies(t *testing.T) {
	srv := newLocal(t)
	f := setFakeLocal(t, srv)
	code, out := localCall(t, srv, "POST", "/api/local-hermes/connect", testOwner)
	if code != 200 {
		t.Fatalf("first: %d %v", code, out)
	}
	<-f.started
	f.problem = "unreachable"
	code, again := localCall(t, srv, "POST", "/api/local-hermes/connect", testOwner)
	if code != 200 || again["agentId"] != out["agentId"] {
		t.Fatalf("second: %d %v", code, again)
	}
	if len(f.started) != 0 {
		t.Fatal("must not start a second adapter")
	}
}

// A server whose owner rights were handed over does not attach this computer's Hermes to the new owner.
func TestLocalAgentRefusedAfterHandOver(t *testing.T) {
	srv := newLocal(t)
	path := filepath.Join(t.TempDir(), "hermes-agent")
	if _, err := srv.LocalAgent("Hermes", path); err != nil {
		t.Fatal(err)
	}
	heir, _, err := srv.store.CreateOwner("Alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.hub.HandOver(srv.store.OwnerID(), heir, "new-host-token-0123456789"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.LocalAgent("Hermes", path); !errors.Is(err, ErrLocalAgentUnlinked) {
		t.Fatalf("after handover: %v", err)
	}
	if a, _ := srv.store.Agents(heir); agentCount(t, srv) != 0 || len(a) != 0 {
		t.Fatal("old owner's local agent should be gone and none created for the heir")
	}
}

// Even if the new owner presses connect after the handover, it is rejected with handed_over and the id file is not deleted. The status does not show the host Hermes either.
func TestLocalHermesConnectAfterHandOver(t *testing.T) {
	srv := newLocal(t)
	f := setFakeLocal(t, srv)
	if code, out := localCall(t, srv, "POST", "/api/local-hermes/connect", testOwner); code != 200 {
		t.Fatalf("first: %d %v", code, out)
	}
	<-f.started
	heir, heirTok, err := srv.store.CreateOwner("Alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.hub.HandOver(srv.store.OwnerID(), heir, "new-host-token-0123456789"); err != nil {
		t.Fatal(err)
	}
	code, out := localCall(t, srv, "POST", "/api/local-hermes/connect", heirTok)
	if code != http.StatusConflict || out["problem"] != "handed_over" {
		t.Fatalf("connect after handover: %d %v", code, out)
	}
	if _, err := os.Stat(f.path); err != nil {
		t.Fatalf("id file should stay: %v", err)
	}
	code, out = localCall(t, srv, "GET", "/api/local-hermes", heirTok)
	if code != 200 || out["found"] != false || out["problem"] != "handed_over" || out["profiles"] != nil {
		t.Fatalf("status after handover: %d %v", code, out)
	}
}
