package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	deckui "github.com/kyeotapp/kyeot/integrations/hermes/deck-ui"
	"github.com/kyeotapp/kyeot/internal/server"
)

const hermesTestOwner = "test-owner-token-0123456789"

// hermesEnv sets up a fake Hermes and HERMES_HOME. If key is empty, no .env is written.
func hermesEnv(t *testing.T, key string) (agentFile string, hermes *httptest.Server) {
	t.Setenv("KYEOT_SENSORS", "off") // sensors use it even after TempDir cleanup
	home := t.TempDir()
	if key != "" {
		os.WriteFile(filepath.Join(home, ".env"), []byte("API_SERVER_KEY="+key+"\n"), 0o600)
	}
	hermes = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	t.Cleanup(hermes.Close)
	agentFile = filepath.Join(t.TempDir(), "hermes-agent")
	for k, v := range map[string]string{
		"HERMES_HOME": home, "API_SERVER_KEY": "", "HERMES_URL": hermes.URL, "BRIDGE_UI_ADDR": "",
		"KYEOT_HERMES": "", "AGENT_DECK_HERMES": "", "KYEOT_HERMES_AGENT_FILE": agentFile,
	} {
		t.Setenv(k, v)
	}
	return agentFile, hermes
}

func hermesTestServer(t *testing.T) *server.Server {
	srv, err := server.New(server.Options{DBPath: ":memory:", OwnerToken: hermesTestOwner, UploadDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

func hermesCall(t *testing.T, srv *server.Server, method, path string) (int, map[string]any) {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+hermesTestOwner)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestStartHermesWaitsForConsentThenConnects(t *testing.T) {
	file, _ := hermesEnv(t, "fake-key")
	srv := hermesTestServer(t)
	startHermes(srv, "127.0.0.1:1", t.TempDir())
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("first run must not record consent")
	}
	if _, st := hermesCall(t, srv, "GET", "/api/local-hermes"); st["found"] != true || st["connected"] != false {
		t.Fatalf("status: %v", st)
	}
	code, out := hermesCall(t, srv, "POST", "/api/local-hermes/connect")
	if code != 200 || out["agentId"] == nil {
		t.Fatalf("connect: %d %v", code, out)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("connect should write the id file")
	}
}

func TestStartHermesResumesWithAgentFile(t *testing.T) {
	file, _ := hermesEnv(t, "fake-key")
	srv := hermesTestServer(t)
	if _, err := srv.LocalAgent("Hermes", file); err != nil { // consented earlier
		t.Fatal(err)
	}
	startHermes(srv, "127.0.0.1:1", t.TempDir())
	if _, st := hermesCall(t, srv, "GET", "/api/local-hermes"); st["connected"] != true || st["agentId"] == nil {
		t.Fatalf("existing consent should connect at startup: %v", st)
	}
}

func TestStartHermesProblems(t *testing.T) {
	_, hermes := hermesEnv(t, "")
	srv := hermesTestServer(t)
	startHermes(srv, "127.0.0.1:1", t.TempDir())
	if _, st := hermesCall(t, srv, "GET", "/api/local-hermes"); st["found"] != true || st["problem"] != "no_key" {
		t.Fatalf("no key: %v", st)
	}

	t.Setenv("API_SERVER_KEY", "wrong-key")
	if code, out := hermesCall(t, srv, "POST", "/api/local-hermes/connect"); code != 502 || out["problem"] != "bad_key" {
		t.Fatalf("bad key: %d %v", code, out)
	}
	t.Setenv("API_SERVER_KEY", "fake-key")
	hermes.Close()
	if code, out := hermesCall(t, srv, "POST", "/api/local-hermes/connect"); code != 502 || out["problem"] != "unreachable" {
		t.Fatalf("unreachable: %d %v", code, out)
	}
}

func TestStartHermesOff(t *testing.T) {
	hermesEnv(t, "fake-key")
	t.Setenv("KYEOT_HERMES", "off")
	srv := hermesTestServer(t)
	startHermes(srv, "127.0.0.1:1", t.TempDir())
	if _, st := hermesCall(t, srv, "GET", "/api/local-hermes"); st["found"] != false {
		t.Fatalf("off: %v", st)
	}
}

// Tapping connect in the app installs the deck-ui skill on the default profile, which opens the screen address. If the screen address is off, nothing is touched.
func TestConnectInstallsDeckUISkill(t *testing.T) {
	for _, ui := range []string{"", "127.0.0.1:0"} {
		hermesEnv(t, "fake-key")
		t.Setenv("BRIDGE_UI_ADDR", ui)
		home := os.Getenv("HERMES_HOME")
		srv := hermesTestServer(t)
		startHermes(srv, "127.0.0.1:1", t.TempDir())
		if code, out := hermesCall(t, srv, "POST", "/api/local-hermes/connect"); code != 200 {
			t.Fatalf("connect: %d %v", code, out)
		}
		path := deckui.Path(home)
		var err error
		for range 100 { // the adapter runs separately
			if _, err = os.Stat(path); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if ui == "" && err == nil {
			t.Fatal("installed the skill although the screen address is off")
		}
		if ui != "" {
			if b, _ := os.ReadFile(path); !bytes.Equal(b, deckui.Skill) {
				t.Fatalf("the skill was not installed: %v", err)
			}
		}
	}
}
