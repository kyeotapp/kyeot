package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type latestSeen struct {
	mu      sync.Mutex
	headers []http.Header
	hits    atomic.Int32
}

func latestRelease(t *testing.T, body string, seen *latestSeen) *httptest.Server {
	t.Helper()
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.hits.Add(1)
		seen.mu.Lock()
		seen.headers = append(seen.headers, r.Header.Clone())
		seen.mu.Unlock()
		w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func versionLatest(t *testing.T, url string) (string, bool) {
	t.Helper()
	_, body := getVersion(t, url)
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err, body)
	}
	v, ok := got["latest"].(string)
	return v, ok
}

func waitLatest(t *testing.T, url string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := versionLatest(t, url); ok {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("latest never appeared in /version")
	return ""
}

func TestLatestInVersion(t *testing.T) {
	seen := &latestSeen{}
	rel := latestRelease(t, "0.2.0\n", seen)
	ts := versionServer(t, Options{Version: "0.1.6", LatestURL: rel.URL + "/VERSION", LatestClient: rel.Client()})
	if v := waitLatest(t, ts.URL); v != "0.2.0" {
		t.Fatalf("latest %q", v)
	}
	seen.mu.Lock()
	defer seen.mu.Unlock()
	h := seen.headers[0]
	if ua := h.Get("User-Agent"); ua != "kyeot/0.1.6" {
		t.Errorf("User-Agent %q", ua)
	}
	for k := range h { // nothing that identifies the user or the server
		switch k {
		case "User-Agent", "Accept-Encoding":
		default:
			t.Errorf("unexpected header %s: %v", k, h[k])
		}
	}
}

func TestLatestRechecks(t *testing.T) {
	seen := &latestSeen{}
	rel := latestRelease(t, "0.2.0", seen)
	versionServer(t, Options{Version: "0.1.6", LatestURL: rel.URL + "/VERSION", LatestClient: rel.Client(), LatestEvery: 20 * time.Millisecond})
	deadline := time.Now().Add(5 * time.Second)
	for seen.hits.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := seen.hits.Load(); n < 3 {
		t.Fatalf("checked %d times", n)
	}
}

func TestLatestOff(t *testing.T) {
	seen := &latestSeen{}
	rel := latestRelease(t, "0.2.0", seen)
	for name, opts := range map[string]Options{
		"no url":      {Version: "0.1.6", LatestClient: rel.Client()},
		"dev version": {Version: "dev-abc", LatestURL: rel.URL + "/VERSION", LatestClient: rel.Client()},
	} {
		t.Run(name, func(t *testing.T) {
			srv, err := New(Options{DBPath: ":memory:", OwnerToken: "test-owner-token-0123456789", Version: opts.Version, LatestURL: opts.LatestURL, LatestClient: opts.LatestClient})
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(100 * time.Millisecond)
			srv.Close()
		})
	}
	if n := seen.hits.Load(); n != 0 {
		t.Fatalf("checked %d times while off", n)
	}
}

func TestVersionWithoutLatest(t *testing.T) {
	ts := versionServer(t, Options{Version: "0.1.6"})
	if v, ok := versionLatest(t, ts.URL); ok {
		t.Fatalf("latest %q before any check", v)
	}
}

func TestLatestRejects(t *testing.T) {
	big := "0.2.0" + strings.Repeat(" ", latestMax) // valid once trimmed, so only the size limit refuses it
	cases := map[string]http.HandlerFunc{
		"not a version": func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>")) },
		"too large":     func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(big)) },
		"status 500": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("0.2.0"))
		},
		"prerelease": func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("0.2.0-rc1")) },
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			rel := httptest.NewTLSServer(h)
			defer rel.Close()
			if v, err := fetchLatest(context.Background(), latestClient(rel.Client()), rel.URL, "0.1.6"); err == nil {
				t.Fatalf("accepted %q", v)
			}
		})
	}
}

func TestLatestHTTPSOnly(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("0.2.0")) }))
	defer plain.Close()
	if v, err := fetchLatest(context.Background(), latestClient(nil), plain.URL, "0.1.6"); err == nil {
		t.Fatalf("plain http accepted %q", v)
	}
	rel := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/VERSION", http.StatusFound)
	}))
	defer rel.Close()
	if v, err := fetchLatest(context.Background(), latestClient(rel.Client()), rel.URL, "0.1.6"); err == nil {
		t.Fatalf("redirect to http accepted %q", v)
	}
}

func TestLatestKeepsLastOnFailure(t *testing.T) {
	var fail atomic.Bool
	rel := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		w.Write([]byte("0.2.0"))
	}))
	defer rel.Close()
	ts := versionServer(t, Options{Version: "0.1.6", LatestURL: rel.URL, LatestClient: rel.Client(), LatestEvery: 20 * time.Millisecond})
	waitLatest(t, ts.URL)
	fail.Store(true)
	time.Sleep(100 * time.Millisecond)
	if v, ok := versionLatest(t, ts.URL); !ok || v != "0.2.0" {
		t.Fatalf("latest after failure %q %v", v, ok)
	}
}

func TestLatestCloseStopsCheck(t *testing.T) {
	hang := make(chan struct{})
	rel := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hang:
		case <-r.Context().Done():
		}
	}))
	defer rel.Close()
	defer close(hang)
	srv, err := New(Options{DBPath: ":memory:", OwnerToken: "test-owner-token-0123456789", Version: "0.1.6", LatestURL: rel.URL, LatestClient: rel.Client()})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	done := make(chan struct{})
	go func() { srv.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited on the update check")
	}
}

func TestLatestStopsAfterServerClose(t *testing.T) {
	seen := &latestSeen{}
	rel := latestRelease(t, "0.2.0", seen)
	srv, err := New(Options{DBPath: ":memory:", OwnerToken: "test-owner-token-0123456789", Version: "0.1.6", LatestURL: rel.URL, LatestClient: rel.Client(), LatestEvery: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	deadline := time.Now().Add(5 * time.Second)
	for seen.hits.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	srv.closed.Store(true) // what POST /api/server/close sets
	n := seen.hits.Load()
	time.Sleep(200 * time.Millisecond)
	if got := seen.hits.Load(); got > n+1 { // one request may already be in flight
		t.Fatalf("closed server kept checking: %d → %d", n, got)
	}
}
