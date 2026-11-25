package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kyeotapp/kyeot/internal/decktest"
)

func TestConnectExchangesCodeAndSavesPrivately(t *testing.T) {
	h := decktest.New(t)
	created := h.CreateAgent("Agent A")
	var issued struct{ Code string }
	h.API("POST", "/api/agents/"+created.Agent.ID+"/pair", nil, &issued)

	token, err := exchange(h.URL, issued.Code)
	if err != nil || token == "" {
		t.Fatalf("exchange: %v", err)
	}
	if _, err := exchange(h.URL, issued.Code); err == nil {
		t.Fatal("code must not work twice")
	}
	path := filepath.Join(t.TempDir(), "sub", "connect.json")
	if err := savePrivate(path, connection{h.URL, token}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("connect file should be 0600: %v %v", fi, err)
	}
}

func TestInsecureURL(t *testing.T) {
	for url, want := range map[string]bool{
		"http://192.168.0.5:7700":        true, // public-ok
		"http://my-mac.local:7700":       true,
		"http://100.100.1.2:7700":        false, // public-ok
		"http://mac.tailnet.ts.net:7700": false,
		"http://127.0.0.1:7700":          false,
		"https://example.com":            false,
	} {
		if insecure(url) != want {
			t.Errorf("%s: want %v", url, want)
		}
	}
}

func TestReachHint(t *testing.T) {
	for url, want := range map[string]string{
		"http://100.100.1.2:7700":        "Tailscale", // public-ok
		"http://mac.tailnet.ts.net:7700": "Tailscale",
		"http://192.168.0.5:7700":        "와이파이", // public-ok
		"http://10.0.0.5:7700":           "와이파이", // public-ok
		"http://my-mac.local:7700":       "와이파이",
		"http://[fd12::1]:7700":          "와이파이",
		"https://kyeot.example.com":      "",
		"http://8.8.8.8:7700":            "",
		"http://100.128.0.1:7700":        "",
	} {
		got := reachHint(url)
		if (want == "" && got != "") || (want != "" && !strings.Contains(got, want)) {
			t.Errorf("%s: %q, want %q", url, got, want)
		}
	}
}

func TestLoopbackURL(t *testing.T) {
	for raw, want := range map[string]bool{
		"http://127.0.0.1:7700": true, "http://localhost:7700": true, "http://[::1]:7700": true,
		"http://192.168.0.10:7700": false, "https://kyeot.example.com": false, "%%": false, // public-ok
	} {
		if got := loopbackURL(raw); got != want {
			t.Errorf("%s: %v", raw, got)
		}
	}
}

func TestPlainToInternet(t *testing.T) {
	for url, want := range map[string]bool{
		"http://203.0.113.5:7700":         true,
		"http://kyeot.example.com":        true,
		"http://192.168.0.5:7700":         false, // public-ok
		"http://[fd7a:115c:a1e0::1]:7700": false,
		"http://100.100.1.2:7700":         false, // public-ok
		"http://my-mac.local:7700":        false,
		"http://mac.tailnet.ts.net:7700":  false,
		"http://my-mac:7700":              false,
		"http://localhost:7700":           false,
		"https://kyeot.example.com":       false,
	} {
		if plainToInternet(url) != want {
			t.Errorf("%s: want %v", url, want)
		}
	}
}

func TestConnectRefusesPlainPublic(t *testing.T) {
	t.Setenv("KYEOT_CONNECT_FILE", filepath.Join(t.TempDir(), "connect.json"))
	t.Setenv("KYEOT_ALLOW_PLAIN", "")
	err := runConnect([]string{"203.0.113.5:7700", "123456"})
	if err == nil || !strings.Contains(err.Error(), "KYEOT_ALLOW_PLAIN") {
		t.Fatalf("http to a public address must be rejected: %v", err)
	}
	t.Setenv("KYEOT_ALLOW_PLAIN", "1")
	if !plainAllowed() {
		t.Fatal("allowed with KYEOT_ALLOW_PLAIN=1")
	}
}
