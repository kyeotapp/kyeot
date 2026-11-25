package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kyeotapp/kyeot/internal/server"
)

// profilesEnv sets up ~/.hermes (default) and profiles/work under HOME.
// One fake gateway takes /v1 with the default key and /p/work/v1 with the work key (same shape as Hermes multiplex).
func profilesEnv(t *testing.T) (root, dataDir string, gw *httptest.Server) {
	t.Setenv("KYEOT_SENSORS", "off")
	home := t.TempDir()
	root = filepath.Join(home, ".hermes")
	work := filepath.Join(root, "profiles", "work")
	os.MkdirAll(work, 0o700)
	os.WriteFile(filepath.Join(root, ".env"), []byte("API_SERVER_KEY=default-key-0123456789\n"), 0o600)
	os.WriteFile(filepath.Join(root, "profile.yaml"), []byte("display_name: Agent A\n"), 0o600)
	os.WriteFile(filepath.Join(root, "config.yaml"), []byte("model:\n  default: model-a\n"), 0o600)
	os.WriteFile(filepath.Join(work, ".env"), []byte("# comment\nAPI_SERVER_KEY=work-key-0123456789ab\n"), 0o600)
	os.WriteFile(filepath.Join(work, "config.yaml"), []byte("model: model-b\n"), 0o600)
	os.MkdirAll(filepath.Join(root, "profiles", "ghost"), 0o700) // a folder without an identity marker is not a profile
	gw = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "default-key-0123456789"
		if strings.HasPrefix(r.URL.Path, "/p/work/") {
			if _, err := os.Stat(filepath.Join(work, "gateway.parked")); err == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			want = "work-key-0123456789ab"
		} else if strings.HasPrefix(r.URL.Path, "/p/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	t.Cleanup(gw.Close)
	for k, v := range map[string]string{
		"HOME": home, "HERMES_HOME": "", "API_SERVER_KEY": "", "HERMES_URL": gw.URL, "BRIDGE_UI_ADDR": "",
		"KYEOT_HERMES": "", "AGENT_DECK_HERMES": "", "KYEOT_HERMES_AGENT_FILE": "", "KYEOT_HERMES_TOKEN_FILE": "",
	} {
		t.Setenv(k, v)
	}
	return root, t.TempDir(), gw
}

func profilesServer(t *testing.T, db string) *server.Server {
	srv, err := server.New(server.Options{DBPath: db, OwnerToken: hermesTestOwner, UploadDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func connectProfile(t *testing.T, srv *server.Server, profile string) (int, map[string]any) {
	req := httptest.NewRequest("POST", "/api/local-hermes/connect", bytes.NewBufferString(`{"profile":"`+profile+`"}`))
	req.Header.Set("Authorization", "Bearer "+hermesTestOwner)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var out map[string]any
	jsonDecode(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func profileRows(t *testing.T, srv *server.Server) map[string]map[string]any {
	_, st := hermesCall(t, srv, "GET", "/api/local-hermes")
	rows := map[string]map[string]any{}
	list, _ := st["profiles"].([]any)
	for _, r := range list {
		m := r.(map[string]any)
		rows[m["id"].(string)] = m
	}
	return rows
}

func TestHermesProfilesListed(t *testing.T) {
	_, dataDir, _ := profilesEnv(t)
	srv := profilesServer(t, ":memory:")
	t.Cleanup(func() { srv.Close() })
	startHermes(srv, "127.0.0.1:1", dataDir)
	rows := profileRows(t, srv)
	if len(rows) != 2 || rows["default"] == nil || rows["work"] == nil {
		t.Fatalf("want default and work: %v", rows)
	}
	d, w := rows["default"], rows["work"]
	if d["name"] != "Agent A" || d["model"] != "model-a" || d["hasKey"] != true || d["ui"] != false { // BRIDGE_UI_ADDR="", so the screen address is off
		t.Fatalf("default row: %v", d)
	}
	if w["name"] != "work" || w["model"] != "model-b" || w["hasKey"] != true || w["ui"] != false || w["gateway"] != "stopped" {
		t.Fatalf("work row: %v", w)
	}
	for _, r := range rows {
		for k, v := range r {
			if s, ok := v.(string); ok && strings.Contains(s, "key-0123") {
				t.Fatalf("key leaked in %s", k)
			}
		}
	}
}

func TestHermesProfilesConnectBothUnlinkOneRestart(t *testing.T) {
	_, dataDir, _ := profilesEnv(t)
	db := filepath.Join(t.TempDir(), "deck.db")
	srv := profilesServer(t, db)
	startHermes(srv, "127.0.0.1:1", dataDir)

	_, a := connectProfile(t, srv, "")
	code, b := connectProfile(t, srv, "work")
	if code != 200 || a["agentId"] == nil || b["agentId"] == nil || a["agentId"] == b["agentId"] {
		t.Fatalf("two agents: %v %v", a, b)
	}
	for _, f := range []string{"hermes-agent", "hermes-agent.work"} {
		if _, err := os.Stat(filepath.Join(dataDir, f)); err != nil {
			t.Fatalf("id file %s: %v", f, err)
		}
	}
	if _, again := connectProfile(t, srv, "work"); again["agentId"] != b["agentId"] {
		t.Fatalf("reconnect should keep id: %v", again)
	}

	if code, _ := hermesCall(t, srv, "DELETE", "/api/agents/"+a["agentId"].(string)); code/100 != 2 {
		t.Fatalf("unlink default: %d", code)
	}
	rows := profileRows(t, srv)
	if rows["default"]["connected"] != false || rows["work"]["connected"] != true || rows["work"]["agentId"] != b["agentId"] {
		t.Fatalf("unlinking default must keep work: %v", rows)
	}
	srv.Close()

	srv2 := profilesServer(t, db)
	t.Cleanup(func() { srv2.Close() })
	startHermes(srv2, "127.0.0.1:1", dataDir)
	rows = profileRows(t, srv2)
	if rows["work"]["connected"] != true || rows["work"]["agentId"] != b["agentId"] {
		t.Fatalf("work should resume after restart: %v", rows)
	}
	if rows["default"]["connected"] != false || rows["default"]["problem"] != "unlinked" {
		t.Fatalf("unlinked default must stay off: %v", rows["default"])
	}
}

func TestHermesProfilesGateway(t *testing.T) {
	root, dataDir, _ := profilesEnv(t)
	os.WriteFile(filepath.Join(root, "gateway.pid"), []byte(`{"pid": `+strconv.Itoa(os.Getpid())+`}`), 0o600)
	os.WriteFile(filepath.Join(root, "gateway_state.json"), []byte(`{"served_profiles": ["default"]}`), 0o600)
	srv := profilesServer(t, ":memory:")
	t.Cleanup(func() { srv.Close() })
	startHermes(srv, "127.0.0.1:1", dataDir)
	if rows := profileRows(t, srv); rows["default"]["gateway"] != "running" || rows["work"]["gateway"] != "stopped" {
		t.Fatalf("not yet served: %v", rows)
	}
	os.WriteFile(filepath.Join(root, "gateway_state.json"), []byte(`{"served_profiles": ["default", "work"]}`), 0o600)
	if rows := profileRows(t, srv); rows["default"]["gateway"] != "running" || rows["work"]["gateway"] != "running" {
		t.Fatalf("running: %v", rows)
	}
	os.WriteFile(filepath.Join(root, "profiles", "work", "gateway.parked"), nil, 0o600)
	if rows := profileRows(t, srv); rows["work"]["gateway"] != "stopped" {
		t.Fatalf("parked: %v", rows["work"])
	}
	if code, out := connectProfile(t, srv, "work"); code != 503 || out["problem"] != "gateway_off" {
		t.Fatalf("parked connect: %d %v", code, out)
	}
	if code, out := connectProfile(t, srv, "nope"); code != 404 || out["problem"] != "not_found" {
		t.Fatalf("unknown profile: %d %v", code, out)
	}
}

func TestHermesProfilesLegacyHome(t *testing.T) {
	root, dataDir, _ := profilesEnv(t)
	t.Setenv("HERMES_HOME", root)
	srv := profilesServer(t, ":memory:")
	t.Cleanup(func() { srv.Close() })
	startHermes(srv, "127.0.0.1:1", dataDir)
	_, st := hermesCall(t, srv, "GET", "/api/local-hermes")
	if list, _ := st["profiles"].([]any); len(list) != 1 || st["found"] != true {
		t.Fatalf("HERMES_HOME keeps one: %v", st)
	}
	if code, out := connectProfile(t, srv, ""); code != 200 || out["agentId"] == nil {
		t.Fatalf("legacy connect: %d %v", code, out)
	}
}

func jsonDecode(b []byte, v any) { json.Unmarshal(b, v) }
