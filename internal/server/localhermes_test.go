package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kyeotapp/kyeot/internal/store"
)

type fakeLocal struct {
	found, hasKey bool
	problem       string
	probes        atomic.Int32
	started       chan string
	path          string
}

func setFakeLocal(t *testing.T, srv *Server) *fakeLocal {
	f := &fakeLocal{found: true, hasKey: true, started: make(chan string, 8), path: filepath.Join(t.TempDir(), "hermes-agent")}
	srv.SetLocalHermes(LocalHermes{
		Name:   "Hermes",
		IDPath: f.path,
		Find:   func() (bool, bool) { return f.found, f.hasKey },
		Probe: func(ctx context.Context) string {
			f.probes.Add(1)
			time.Sleep(20 * time.Millisecond) // so concurrent calls overlap
			return f.problem
		},
		Start: func(ctx context.Context, token string) { f.started <- token },
	})
	return f
}

func localCall(t *testing.T, srv *Server, method, path, token string) (int, map[string]any) {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

const testOwner = "test-owner-token-0123456789"

func TestLocalHermesFirstRunWaitsForConsent(t *testing.T) {
	srv := newLocal(t)
	f := setFakeLocal(t, srv)
	consented, err := srv.ResumeLocalHermes()
	if err != nil || consented {
		t.Fatalf("no file must not connect: %v %v", consented, err)
	}
	if agentCount(t, srv) != 0 || len(f.started) != 0 {
		t.Fatal("nothing should be created or started before consent")
	}
	if _, err := os.Stat(f.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("id file must not exist before consent")
	}
	code, st := localCall(t, srv, "GET", "/api/local-hermes", testOwner)
	if code != 200 || st["found"] != true || st["connected"] != false || st["problem"] != nil {
		t.Fatalf("status: %d %v", code, st)
	}
}

func TestLocalHermesResumesWhenConsented(t *testing.T) {
	srv := newLocal(t)
	f := setFakeLocal(t, srv)
	if _, err := srv.LocalAgent("Hermes", f.path); err != nil { // consented before
		t.Fatal(err)
	}
	consented, err := srv.ResumeLocalHermes()
	if err != nil || !consented {
		t.Fatalf("resume: %v %v", consented, err)
	}
	token := <-f.started
	a, err := srv.store.AgentByToken(token)
	if err != nil || agentCount(t, srv) != 1 {
		t.Fatalf("resume should reuse the agent: %v", err)
	}
	if f.probes.Load() != 0 {
		t.Fatal("resume must not wait for Hermes")
	}
	_, st := localCall(t, srv, "GET", "/api/local-hermes", testOwner)
	if st["connected"] != true || st["agentId"] != a.ID {
		t.Fatalf("status: %v", st)
	}
}

func TestLocalHermesResumeHonorsUnlink(t *testing.T) {
	srv := newLocal(t)
	f := setFakeLocal(t, srv)
	srv.LocalAgent("Hermes", f.path)
	a, _ := srv.store.Agents(store.Owner)
	srv.hub.UnlinkAgent(store.Owner, a[0].ID)
	if _, err := srv.ResumeLocalHermes(); !errors.Is(err, ErrLocalAgentUnlinked) {
		t.Fatalf("unlinked: %v", err)
	}
	if len(f.started) != 0 || agentCount(t, srv) != 0 {
		t.Fatal("unlinked agent must not be reattached at startup")
	}
	_, st := localCall(t, srv, "GET", "/api/local-hermes", testOwner)
	if st["problem"] != "unlinked" || st["connected"] != false {
		t.Fatalf("status: %v", st)
	}
}

func TestLocalHermesNoKey(t *testing.T) {
	srv := newLocal(t)
	f := setFakeLocal(t, srv)
	f.hasKey = false
	_, st := localCall(t, srv, "GET", "/api/local-hermes", testOwner)
	if st["found"] != true || st["problem"] != "no_key" {
		t.Fatalf("status: %v", st)
	}
	code, out := localCall(t, srv, "POST", "/api/local-hermes/connect", testOwner)
	if code != 400 || out["problem"] != "no_key" || out["error"] == nil {
		t.Fatalf("connect: %d %v", code, out)
	}
	if agentCount(t, srv) != 0 || f.probes.Load() != 0 {
		t.Fatal("no agent, no probe without a key")
	}
}

func TestLocalHermesConnectCreatesAndIsIdempotent(t *testing.T) {
	srv := newLocal(t)
	f := setFakeLocal(t, srv)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/client?token="+testOwner, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	code, out := localCall(t, srv, "POST", "/api/local-hermes/connect", testOwner)
	id, _ := out["agentId"].(string)
	if code != 200 || id == "" {
		t.Fatalf("connect: %d %v", code, out)
	}
	b, err := os.ReadFile(f.path)
	if err != nil || strings.TrimSpace(string(b)) != id {
		t.Fatalf("id file should record consent: %q %v", b, err)
	}
	token := <-f.started
	if a, err := srv.store.AgentByToken(token); err != nil || a.ID != id {
		t.Fatal("bridge should get the agent's token")
	}
	for { // a notice goes out so the app refetches the agent list
		_, raw, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("no agent notice: %v", err)
		}
		var m struct {
			Type  string `json:"type"`
			Agent *struct {
				ID string `json:"id"`
			} `json:"agent"`
		}
		json.Unmarshal(raw, &m)
		if m.Type == "agent" && m.Agent != nil && m.Agent.ID == id {
			break
		}
	}

	code, again := localCall(t, srv, "POST", "/api/local-hermes/connect", testOwner)
	if code != 200 || again["agentId"] != id || agentCount(t, srv) != 1 || len(f.started) != 0 {
		t.Fatalf("second connect should be a no-op: %d %v", code, again)
	}
}

func TestLocalHermesConcurrentConnectMakesOneAgent(t *testing.T) {
	srv := newLocal(t)
	f := setFakeLocal(t, srv)
	var wg sync.WaitGroup
	ids := make([]string, 8)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, out := localCall(t, srv, "POST", "/api/local-hermes/connect", testOwner)
			ids[i], _ = out["agentId"].(string)
		}()
	}
	wg.Wait()
	if agentCount(t, srv) != 1 || len(f.started) != 1 {
		t.Fatalf("agents=%d started=%d", agentCount(t, srv), len(f.started))
	}
	for _, id := range ids {
		if id == "" || id != ids[0] {
			t.Fatalf("ids differ: %v", ids)
		}
	}
}

func TestLocalHermesRequiresOwner(t *testing.T) {
	srv := newLocal(t)
	setFakeLocal(t, srv)
	for _, c := range []struct{ method, path string }{{"GET", "/api/local-hermes"}, {"POST", "/api/local-hermes/connect"}} {
		for _, tok := range []string{"", "wrong-token-0123456789"} {
			if code, _ := localCall(t, srv, c.method, c.path, tok); code != 401 {
				t.Fatalf("%s %s %q: %d", c.method, c.path, tok, code)
			}
		}
	}
	if agentCount(t, srv) != 0 {
		t.Fatal("unauthenticated connect created an agent")
	}
}

func TestLocalHermesOff(t *testing.T) {
	srv := newLocal(t) // SetLocalHermes not called = KYEOT_HERMES=off
	_, st := localCall(t, srv, "GET", "/api/local-hermes", testOwner)
	if st["found"] != false || st["connected"] != false {
		t.Fatalf("status: %v", st)
	}
	if code, out := localCall(t, srv, "POST", "/api/local-hermes/connect", testOwner); code != 404 || out["problem"] != "not_found" {
		t.Fatalf("connect: %d %v", code, out)
	}
}

func TestLocalHermesUnreachable(t *testing.T) {
	srv := newLocal(t)
	f := setFakeLocal(t, srv)
	f.problem = "unreachable"
	code, out := localCall(t, srv, "POST", "/api/local-hermes/connect", testOwner)
	if code != 502 || out["problem"] != "unreachable" {
		t.Fatalf("connect: %d %v", code, out)
	}
	if agentCount(t, srv) != 0 || len(f.started) != 0 {
		t.Fatal("no agent when Hermes is unreachable")
	}
	if _, err := os.Stat(f.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("no consent file when connect failed")
	}
}

func TestLocalHermesReconnectAfterUnlink(t *testing.T) {
	srv := newLocal(t)
	f := setFakeLocal(t, srv)
	_, out := localCall(t, srv, "POST", "/api/local-hermes/connect", testOwner)
	first := out["agentId"].(string)
	<-f.started
	srv.hub.UnlinkAgent(store.Owner, first)
	if _, st := localCall(t, srv, "GET", "/api/local-hermes", testOwner); st["connected"] != false {
		t.Fatalf("unlinked agent shown as connected: %v", st)
	}
	_, out = localCall(t, srv, "POST", "/api/local-hermes/connect", testOwner)
	if id, _ := out["agentId"].(string); id == "" || id == first || agentCount(t, srv) != 1 {
		t.Fatalf("explicit connect after unlink should make a fresh agent: %v", out)
	}
}

func TestLocalHermesOwnerOnly(t *testing.T) {
	srv := newLocal(t)
	f := setFakeLocal(t, srv)
	invitedID, invited, err := srv.store.CreateOwner("Invited person")
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := localCall(t, srv, "GET", "/api/local-hermes", invited); code != 403 {
		t.Fatalf("invited GET: %d", code)
	}
	if code, _ := localCall(t, srv, "POST", "/api/local-hermes/connect", invited); code != 403 {
		t.Fatalf("invited connect: %d", code)
	}
	mine, _ := srv.store.Agents(invitedID)
	if agentCount(t, srv) != 0 || len(mine) != 0 || len(f.started) != 0 || f.probes.Load() != 0 {
		t.Fatal("invited user must not create or start an agent")
	}
	if code, _ := localCall(t, srv, "GET", "/api/local-hermes", testOwner); code != 200 {
		t.Fatalf("owner GET: %d", code)
	}
}
