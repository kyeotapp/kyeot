package server_test

import (
	"bytes"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/server"
)

func TestProxyHeader(t *testing.T) {
	cases := []struct {
		remote, key, want string
	}{
		{"127.0.0.1:5000", "X-Forwarded-For", "X-Forwarded-For"},
		{"[::1]:5000", "Forwarded", "Forwarded"},
		{"127.0.0.1:5000", "Tailscale-Funnel-Request", "Tailscale-Funnel-Request"},
		{"127.0.0.1:5000", "tailscale-user-login", "Tailscale-User-Login"},
		{"127.0.0.1:5000", "", ""},
		{"127.0.0.1:5000", "X-Kyeot-Poll", ""},
		{"192.168.0.5:5000", "X-Forwarded-For", ""},     // public-ok
		{"100.64.0.9:5000", "Tailscale-User-Login", ""}, // public-ok
	}
	for _, c := range cases {
		h := http.Header{}
		if c.key != "" {
			h[c.key] = []string{"x"} // also insert the lowercase name as is
		}
		if got := server.ProxyHeader(c.remote, h); got != c.want {
			t.Errorf("%s %s: %q (want %q)", c.remote, c.key, got, c.want)
		}
	}
}

func captureLog(t *testing.T) *bytes.Buffer {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func get(t *testing.T, url string, header map[string]string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", url+"/api/agents", nil)
	req.Header.Set("Authorization", "Bearer "+decktest.OwnerToken)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestProxyBehindLoopbackWarnsOnce(t *testing.T) {
	mark := filepath.Join(t.TempDir(), "proxy-detected")
	logs := captureLog(t)
	h := decktest.NewWith(t, func(o *server.Options) { o.ProxyMarkFile = mark })

	if s := get(t, h.URL, nil); s != http.StatusOK {
		t.Fatalf("plain: %d", s)
	}
	if _, err := os.Stat(mark); err == nil {
		t.Fatal("a loopback request without a header is not marked")
	}
	if s := get(t, h.URL, map[string]string{"X-Forwarded-For": "203.0.113.7"}); s != http.StatusOK {
		t.Fatalf("must not block: %d", s)
	}
	if s := get(t, h.URL, map[string]string{"Tailscale-Funnel-Request": "?1"}); s != http.StatusOK {
		t.Fatalf("must not block: %d", s)
	}
	b, err := os.ReadFile(mark)
	if err != nil || !strings.HasSuffix(strings.TrimSpace(string(b)), " X-Forwarded-For") || strings.Contains(string(b), "203.0.113.7") {
		t.Fatalf("mark file: %q %v", b, err)
	}
	if n := strings.Count(logs.String(), "지원하지 않는다"); n != 1 {
		t.Fatalf("one warning only: %d\n%s", n, logs)
	}
}

func TestProxyMarkLoggedAtStart(t *testing.T) {
	mark := filepath.Join(t.TempDir(), "proxy-detected")
	os.WriteFile(mark, []byte("2026-11-18T10:00:00+09:00 X-Forwarded-For\n"), 0o600)
	logs := captureLog(t)
	decktest.NewWith(t, func(o *server.Options) { o.ProxyMarkFile = mark })
	if !strings.Contains(logs.String(), "previous run") || !strings.Contains(logs.String(), "X-Forwarded-For") {
		t.Fatalf("startup reports the earlier detection: %s", logs)
	}
	if _, err := os.Stat(mark); err == nil {
		t.Fatal("cleared after reporting (rewritten if seen again in this run)")
	}
}
