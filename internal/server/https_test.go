package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/server"
)

func tlsServer(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	srv, err := server.New(server.Options{DBPath: ":memory:", OwnerToken: decktest.OwnerToken, AllowedOrigins: []string{"https://app.example.com"}, UploadDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewTLSServer(srv.Handler())
	t.Cleanup(func() { srv.Close(); ts.Close() })
	return ts, ts.Client()
}

func uploadTLS(t *testing.T, c *http.Client, base string, hdr map[string]string) protocol.Attachment {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"/api/uploads?name=a.txt", strings.NewReader("body text"))
	req.Header.Set("Authorization", "Bearer "+decktest.OwnerToken)
	req.Header.Set("Content-Type", "text/plain")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var a protocol.Attachment
	json.NewDecoder(res.Body).Decode(&a)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("upload: %d", res.StatusCode)
	}
	return a
}

// A signed address is path only. It attaches as is to the address the phone uses (https or http).
func TestHTTPSSignedURLIsRelative(t *testing.T) {
	ts, c := tlsServer(t)
	a := uploadTLS(t, c, ts.URL, nil)
	if !strings.HasPrefix(a.URL, "/api/uploads/") {
		t.Fatalf("url not relative: %q", a.URL)
	}
	res, err := c.Get(ts.URL + a.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || string(b) != "body text" {
		t.Fatalf("signed get over https: %d %q", res.StatusCode, b)
	}
}

// Even if a proxy receives https and forwards http, the server never builds an absolute http:// address.
func TestForwardedProtoKeepsURLRelative(t *testing.T) {
	h := decktest.New(t)
	a := uploadTLS(t, http.DefaultClient, h.URL, map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "kyeot.example.com"})
	if !strings.HasPrefix(a.URL, "/api/uploads/") || strings.Contains(a.URL, "http") {
		t.Fatalf("url leaks scheme: %q", a.URL)
	}
}

// Connect like the phone app over wss with a header token, and like the web with an allowed Origin and an address token.
func TestWSSClient(t *testing.T) {
	ts, c := tlsServer(t)
	wss := "wss" + strings.TrimPrefix(ts.URL, "https") + "/client"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for name, opts := range map[string]struct {
		url string
		hdr http.Header
	}{
		"app": {wss, http.Header{"Authorization": {"Bearer " + decktest.OwnerToken}}},
		"web": {wss + "?token=" + decktest.OwnerToken, http.Header{"Origin": {"https://app.example.com"}}},
	} {
		conn, _, err := websocket.Dial(ctx, opts.url, &websocket.DialOptions{HTTPClient: c, HTTPHeader: opts.hdr})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		rctx := conn.CloseRead(ctx)
		if err := conn.Ping(rctx); err != nil {
			t.Fatalf("%s ping: %v", name, err)
		}
		conn.CloseNow()
	}
	_, _, err := websocket.Dial(ctx, wss+"?token="+decktest.OwnerToken, &websocket.DialOptions{HTTPClient: c, HTTPHeader: http.Header{"Origin": {"https://evil.example.com"}}})
	if err == nil {
		t.Fatal("foreign origin accepted over wss")
	}
}

// When the web version calls from an https origin, the CORS response returns that origin.
func TestHTTPSCORS(t *testing.T) {
	ts, c := tlsServer(t)
	req, _ := http.NewRequest("OPTIONS", ts.URL+"/api/agents", bytes.NewReader(nil))
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("allow-origin: %q", got)
	}
}
