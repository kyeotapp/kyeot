package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/kyeotapp/kyeot/internal/store"
)

func versionServer(t *testing.T, opts Options) *httptest.Server {
	t.Helper()
	opts.DBPath, opts.OwnerToken = ":memory:", "test-owner-token-0123456789"
	srv, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); srv.Close() })
	return ts
}

func getVersion(t *testing.T, url string) (int, string) {
	t.Helper()
	res, err := http.Get(url + "/version")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestVersionNoAuth(t *testing.T) {
	ts := versionServer(t, Options{Version: "0.1.0"})
	code, body := getVersion(t, ts.URL)
	if code != http.StatusOK {
		t.Fatalf("%d %q", code, body)
	}
	var got struct {
		Version           string
		Protocol          int
		MinClientProtocol int
		Features          []string
		AvatarPresets     []string
		ServerID          string
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err, body)
	}
	if got.Version != "0.1.0" || got.Protocol != Protocol || got.MinClientProtocol != MinClientProtocol || len(got.Features) == 0 {
		t.Fatalf("%+v", got)
	}
	if len(got.ServerID) != 32 || !slices.Contains(got.Features, "server.id") {
		t.Errorf("serverId %q, server.id in features: %v", got.ServerID, slices.Contains(got.Features, "server.id"))
	}
	if MinClientProtocol > Protocol {
		t.Error("the app minimum version can't exceed the server version")
	}
	if !slices.Equal(got.AvatarPresets, store.MotionPresets) {
		t.Errorf("avatarPresets %v != store.MotionPresets", got.AvatarPresets)
	}
	for _, name := range got.AvatarPresets {
		if !store.Pickable(name) {
			t.Errorf("announced name %q can't be accepted as a picture", name)
		}
	}
	for _, f := range got.Features {
		if f == "" || strings.ContainsAny(f, " \t\n") {
			t.Errorf("odd feature name: %q", f)
		}
	}
}

func TestVersionDevIsUnavailable(t *testing.T) {
	for _, v := range []string{"", "dev", "0.1", "1.2.3-rc1"} {
		ts := versionServer(t, Options{Version: v})
		if code, body := getVersion(t, ts.URL); code != http.StatusServiceUnavailable || !strings.Contains(body, "개발판") {
			t.Errorf("version %q: %d %q", v, code, body)
		}
	}
}

func TestVersionAllowedNets(t *testing.T) {
	nets, _ := ParseNets("10.255.255.0/24") // public-ok
	ts := versionServer(t, Options{Version: "0.1.0", AllowNets: nets})
	if code, _ := getVersion(t, ts.URL); code != http.StatusForbidden {
		t.Errorf("outside allowed network: %d", code)
	}
}

func TestInstallScriptRouteGone(t *testing.T) {
	ts := versionServer(t, Options{Version: "0.1.0"})
	res, err := http.Get(ts.URL + "/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(b), "#!/bin/sh") {
		t.Error("/install.sh still serves the script")
	}
}
