package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/fakeagent"
	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/server"
	"github.com/kyeotapp/kyeot/internal/store"
)

func agentState(t *testing.T, url, id string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", url+"/api/agents", nil)
	req.Header.Set("Authorization", "Bearer "+decktest.OwnerToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var agents []protocol.Agent
	json.NewDecoder(res.Body).Decode(&agents)
	for _, a := range agents {
		if a.ID == id {
			return a.State
		}
	}
	t.Fatalf("agent %s not listed", id)
	return ""
}

func waitState(t *testing.T, url, id, want string) {
	t.Helper()
	for i := 0; i < 50; i++ {
		if agentState(t, url, id) == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("agent state: got %s, want %s", agentState(t, url, id), want)
}

// The old process used to save the disconnect on restart and overwrite the idle the new process wrote.
// A shutting-down server writes no state; the new server resets everything to offline, then raises attaching agents to idle.
func TestRestartKeepsAttachedAgentIdle(t *testing.T) {
	db := filepath.Join(t.TempDir(), "kyeot.db")
	open := func() (*server.Server, *httptest.Server) {
		srv, err := server.New(server.Options{DBPath: db, OwnerToken: decktest.OwnerToken, AllowedOrigins: []string{decktest.AllowedOrigin}, UploadDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		return srv, httptest.NewServer(srv.Handler())
	}
	old, oldTS := open()
	var made struct {
		Agent protocol.Agent
		Token string
	}
	req, _ := http.NewRequest("POST", oldTS.URL+"/api/agents", strings.NewReader(`{"name":"Agent A"}`))
	req.Header.Set("Authorization", "Bearer "+decktest.OwnerToken)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(res.Body).Decode(&made)
	res.Body.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := fakeagent.Dial(ctx, oldTS.URL, made.Token)
	if err != nil {
		t.Fatal(err)
	}
	go a.Run(ctx)
	waitState(t, oldTS.URL, made.Agent.ID, protocol.AgentIdle)

	// The new server starts first (the old one is still cleaning up connections), and the old server closes only after the agent attaches to the new one.
	// The old server's cleanup must not overwrite the idle the new server wrote with offline.
	neu, neuTS := open()
	defer neu.Close()
	defer neuTS.Close()
	if s := agentState(t, neuTS.URL, made.Agent.ID); s != protocol.AgentOffline {
		t.Fatalf("fresh server should start agents offline, got %s", s)
	}
	b, err := fakeagent.Dial(ctx, neuTS.URL, made.Token)
	if err != nil {
		t.Fatal(err)
	}
	go b.Run(ctx)
	waitState(t, neuTS.URL, made.Agent.ID, protocol.AgentIdle)

	old.Close()
	oldTS.Close()
	time.Sleep(300 * time.Millisecond)
	if s := agentState(t, neuTS.URL, made.Agent.ID); s != protocol.AgentIdle {
		t.Fatalf("closing old server overwrote the state: %s", s)
	}
}

// Even if another process on the same DB overwrites the state with offline, an attached agent returns to its last state on the next ping.
func TestAttachedAgentRecoversFromClobberedState(t *testing.T) {
	db := filepath.Join(t.TempDir(), "kyeot.db")
	srv, err := server.New(server.Options{DBPath: db, OwnerToken: decktest.OwnerToken, AllowedOrigins: []string{decktest.AllowedOrigin}, UploadDir: t.TempDir(), PingInterval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer srv.Close()
	defer ts.Close()
	var made struct {
		Agent protocol.Agent
		Token string
	}
	req, _ := http.NewRequest("POST", ts.URL+"/api/agents", strings.NewReader(`{"name":"Agent A"}`))
	req.Header.Set("Authorization", "Bearer "+decktest.OwnerToken)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(res.Body).Decode(&made)
	res.Body.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := fakeagent.Dial(ctx, ts.URL, made.Token)
	if err != nil {
		t.Fatal(err)
	}
	go a.Run(ctx)
	waitState(t, ts.URL, made.Agent.ID, protocol.AgentIdle)

	other, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.MarkAgentsOffline(); err != nil {
		t.Fatal(err)
	}
	other.Close()
	waitState(t, ts.URL, made.Agent.ID, protocol.AgentIdle)
}
