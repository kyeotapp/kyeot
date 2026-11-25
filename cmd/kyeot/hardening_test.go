package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestEmptyTokenFileSaysEmpty(t *testing.T) {
	isolate(t)
	files, _ := tokenFiles()
	os.MkdirAll(filepath.Dir(files[0]), 0o700)
	os.WriteFile(files[0], []byte("\n"), 0o600)
	_, _, err := ownerToken()
	if err == nil || !strings.Contains(err.Error(), "비어 있다") {
		t.Fatalf("an empty file must be reported as empty: %v", err)
	}
	if b, _ := os.ReadFile(files[0]); string(b) != "\n" {
		t.Fatal("an empty file is not silently replaced")
	}
}

func TestStartHermesOffSpellings(t *testing.T) {
	for _, v := range []string{"OFF", "0", "false", "No", " off "} {
		hermesEnv(t, "fake-key")
		t.Setenv("KYEOT_HERMES", v)
		srv := hermesTestServer(t)
		startHermes(srv, "127.0.0.1:1", t.TempDir())
		if _, st := hermesCall(t, srv, "GET", "/api/local-hermes"); st["found"] != false {
			t.Errorf("%q: %v", v, st)
		}
	}
}

func TestConnectForegroundRefusesWhenRegistered(t *testing.T) {
	home := isolate(t)
	t.Setenv("KYEOT_CONNECT_FILE", filepath.Join(home, "connect.json"))
	os.WriteFile(filepath.Join(home, "connect.json"), []byte(`{"url":"http://127.0.0.1:1","token":"x"}`), 0o600)
	t.Setenv("API_SERVER_KEY", "")
	t.Setenv("HERMES_HOME", filepath.Join(home, "nohermes"))
	s, ok := serviceFor(realHost())
	if !ok {
		t.Skip("operating system without service support")
	}
	if err := runConnect([]string{"--foreground"}); err == nil || strings.Contains(err.Error(), "이미 서비스") {
		t.Fatalf("without a registration, it is not blocked: %v", err)
	}
	os.MkdirAll(filepath.Dir(s.file), 0o700)
	os.WriteFile(s.file, []byte("x"), 0o600)
	if err := runConnect([]string{"--foreground"}); err == nil || !strings.Contains(err.Error(), "이미 서비스") {
		t.Fatalf("refused when a service is running: %v", err)
	}
}

func TestPairServerPrefersCertNameOverIP(t *testing.T) {
	cert, _ := writeCert(t, "deck.example.ts.net")
	t.Setenv("KYEOT_TLS_CERT", cert)
	if got, _ := pairServer("192.168.0.9:7700", ""); got != "https://deck.example.ts.net:7700" { // public-ok
		t.Fatalf("with TLS, the certificate name: %s", got)
	}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	pairServer("0.0.0.0:7700", "100.64.0.1")                          // public-ok
	if !strings.Contains(buf.String(), "does not cover 100.64.0.1") { // public-ok
		t.Fatalf("an address not in the certificate warns: %q", buf.String())
	}
}

func TestCertReloaderPicksUpNewFile(t *testing.T) {
	cert, key := writeCert(t, "a.example")
	r, err := newCertReloader(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := r.get(nil)
	c2, k2 := writeCert(t, "b.example")
	for _, p := range [][2]string{{c2, cert}, {k2, key}} {
		b, _ := os.ReadFile(p[0])
		os.WriteFile(p[1], b, 0o600)
		later := time.Now().Add(time.Minute)
		os.Chtimes(p[1], later, later)
	}
	second, _ := r.get(nil)
	if second == first {
		t.Fatal("it must re-read when the file changes")
	}
	os.WriteFile(cert, []byte("broken"), 0o600)
	later := time.Now().Add(2 * time.Minute)
	os.Chtimes(cert, later, later)
	if third, err := r.get(nil); err != nil || third != second {
		t.Fatalf("with a broken file, it keeps the old one: %v", err)
	}
}

func TestCertReloaderWarnsOpenKey(t *testing.T) {
	cert, key := writeCert(t, "a.example")
	os.Chmod(key, 0o644)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	if _, err := newCertReloader(cert, key); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "chmod 600") {
		t.Fatalf("warning for broad key permissions: %q", buf.String())
	}
}

func TestTLSServerServesReloadedCert(t *testing.T) {
	cert, key := writeCert(t, "a.example")
	r, err := newCertReloader(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	srv := newHTTPServer("", http.NotFoundHandler())
	srv.TLSConfig = &tls.Config{GetCertificate: r.get}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.ServeTLS(ln, "", "")
	defer srv.Close()
	cn := func() string {
		conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{InsecureSkipVerify: true})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		return conn.ConnectionState().PeerCertificates[0].Subject.CommonName
	}
	if got := cn(); got != "a.example" {
		t.Fatalf("cn %s", got)
	}
	c2, k2 := writeCert(t, "b.example")
	for _, p := range [][2]string{{c2, cert}, {k2, key}} {
		b, _ := os.ReadFile(p[0])
		os.WriteFile(p[1], b, 0o600)
		later := time.Now().Add(time.Minute)
		os.Chtimes(p[1], later, later)
	}
	if got := cn(); got != "b.example" {
		t.Fatalf("the new certificate must be used without a restart: %s", got)
	}
}

func TestOriginsTrimmed(t *testing.T) {
	if got := splitList(" https://a.example , ,http://b.example "); strings.Join(got, "|") != "https://a.example|http://b.example" {
		t.Fatal(got)
	}
	if splitList("") != nil {
		t.Fatal("nil when empty")
	}
}

// The websocket must stay alive even when the server deadline is short
func TestDeadlinesSpareWebsocket(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			typ, b, err := c.Read(context.Background())
			if err != nil {
				return
			}
			c.Write(context.Background(), typ, b)
		}
	})
	srv := newHTTPServer("", h)
	srv.ReadTimeout, srv.WriteTimeout = 200*time.Millisecond, 200*time.Millisecond
	ts := httptest.NewUnstartedServer(nil)
	ts.Config = srv
	ts.Start()
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	for i := 0; i < 3; i++ {
		time.Sleep(300 * time.Millisecond)
		if err := c.Write(ctx, websocket.MessageText, []byte("hi")); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		if _, b, err := c.Read(ctx); err != nil || string(b) != "hi" {
			t.Fatalf("the websocket must survive past the deadline %d: %v", i, err)
		}
	}
}

func TestDeadlinesExtendUploads(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		io.WriteString(w, strings.Repeat("x", int(n)))
	})
	srv := newHTTPServer("", h)
	srv.ReadTimeout, srv.WriteTimeout = 200*time.Millisecond, 200*time.Millisecond
	ts := httptest.NewUnstartedServer(nil)
	ts.Config = srv
	ts.Start()
	defer ts.Close()
	slow := func(path string) error {
		pr, pw := io.Pipe()
		go func() {
			for i := 0; i < 5; i++ {
				pw.Write([]byte("abc"))
				time.Sleep(100 * time.Millisecond)
			}
			pw.Close()
		}()
		res, err := http.Post(ts.URL+path, "application/octet-stream", pr)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		if err == nil && (res.StatusCode != 200 || len(b) != 15) {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	if err := slow("/api/uploads"); err != nil {
		t.Fatalf("a slow upload is not cut off: %v", err)
	}
	if err := slow("/api/tasks"); err == nil {
		t.Fatal("other paths must hit ReadTimeout (checks that the test really exercises the deadline)")
	}
}

func TestAPNsPartialConfigLogs(t *testing.T) {
	for _, k := range []string{"KEY", "KEY_ID", "TEAM_ID"} {
		t.Setenv("KYEOT_APNS_"+k, "")
		t.Setenv("AGENT_DECK_APNS_"+k, "")
	}
	t.Setenv("KYEOT_APNS_KEY_ID", "ABC")
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	if apnsSender() != nil || !strings.Contains(buf.String(), "must all be set") {
		t.Fatalf("if only some are set, it logs why it is off: %q", buf.String())
	}
}
