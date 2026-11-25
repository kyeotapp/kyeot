package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExchangeErrors(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := "http://" + ln.Addr().String()
	ln.Close()

	reply := func(status int, body string) string {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			w.Write([]byte(body))
		}))
		t.Cleanup(ts.Close)
		return ts.URL
	}

	for name, tc := range map[string]struct{ url, want string }{
		"space in host":   {"http://my mac:7700", "주소"},
		"bad escape":      {"http://%zz", "주소"},
		"closed port":     {closed, "닿지 않는다"},
		"code refused":    {reply(401, `{"error":"the code is wrong or expired"}`), "expired"},
		"locked":          {reply(429, `{"error":"too many wrong codes"}`), "too many"},
		"not json":        {reply(502, `<html>bad gateway</html>`), "502"},
		"ok but empty":    {reply(200, `{}`), "200"},
		"not kyeot (404)": {reply(404, ``), "404"},
	} {
		tok, err := exchange(tc.url, "123456")
		if err == nil || tok != "" || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: token %q err %v", name, tok, err)
		}
	}
}

func TestRunConnectBadArgsAndSavedFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HERMES_HOME", filepath.Join(home, ".hermes"))
	t.Setenv("API_SERVER_KEY", "")
	file := filepath.Join(home, "connect.json")
	t.Setenv("KYEOT_CONNECT_FILE", file)

	for _, args := range [][]string{{"only-url"}, {"a", "b", "c"}} {
		if err := runConnect(args); err == nil || !strings.Contains(err.Error(), "쓰는 법") {
			t.Errorf("%v: %v", args, err)
		}
	}
	for _, addr := range []string{"http://my mac:7700", "http://[::1", "http://%zz"} {
		if err := runConnect([]string{addr, "123456"}); err == nil || !strings.Contains(err.Error(), "주소 형식") {
			t.Errorf("%q: %v", addr, err)
		}
	}
	if err := runConnect(nil); err == nil || !strings.Contains(err.Error(), "저장된 연결이 없다") {
		t.Fatalf("no file: %v", err)
	}
	for name, body := range map[string]string{
		"broken json": `{"url":`,
		"no token":    `{"url":"http://127.0.0.1:7700"}`,
		"no url":      `{"token":"t"}`,
		"empty":       ``,
	} {
		os.WriteFile(file, []byte(body), 0o600)
		if err := runConnect(nil); err == nil || !strings.Contains(err.Error(), "읽지 못했다") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "Library")); err == nil {
		t.Fatal("must not register a service")
	}
}

// With a saved connection, it asks whether to overwrite before spending the code. If not a terminal, it only warns and keeps the old behavior.
func TestConnectAsksBeforeOverwrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HERMES_HOME", filepath.Join(home, ".hermes"))
	t.Setenv("API_SERVER_KEY", "")
	t.Setenv("KYEOT_CONNECT_YES", "")
	file := filepath.Join(home, "connect.json")
	t.Setenv("KYEOT_CONNECT_FILE", file)
	saved := `{"url":"http://old.example:7700","token":"t"}`
	os.WriteFile(file, []byte(saved), 0o600)
	tty, in := true, ""
	stdinIsTerminal = func() bool { return tty }
	t.Cleanup(func() { stdinIsTerminal, promptIn = func() bool { return false }, os.Stdin })

	ask := func(yes bool) (string, error) {
		promptIn = strings.NewReader(in)
		var out strings.Builder
		err := confirmOverwrite(yes, &out)
		return out.String(), err
	}
	for _, a := range []string{"", "n\n", "no\n", "nope\n"} {
		in = a
		if out, err := ask(false); err == nil || !strings.Contains(out, "old.example:7700 에 연결돼 있다. 덮어쓸까? [y/N]") {
			t.Errorf("%q: out %q err %v", a, out, err)
		}
	}
	for _, a := range []string{"y\n", " YES \n", "y"} {
		in = a
		if _, err := ask(false); err != nil {
			t.Errorf("%q: %v", a, err)
		}
	}
	in = ""
	if out, err := ask(true); err != nil || out != "" {
		t.Errorf("--yes: out %q err %v", out, err)
	}
	tty = false
	if out, err := ask(false); err != nil || !strings.Contains(out, "주의: http://old.example:7700 에 붙어 있던 연결을 덮어쓴다") {
		t.Errorf("pipe: out %q err %v", out, err)
	}
	tty = true
	os.WriteFile(file, []byte(`{"url":`), 0o600)
	if out, err := ask(false); err != nil || out != "" {
		t.Errorf("broken saved file: out %q err %v", out, err)
	}

	// in runConnect: if refused, it ends before sending the code to the server and the file stays as is
	os.WriteFile(file, []byte(saved), 0o600)
	promptIn = strings.NewReader("n\n")
	if err := runConnect([]string{"http://127.0.0.1:1", "123456"}); err == nil || !strings.Contains(err.Error(), "그대로 둔다") {
		t.Fatalf("declined: %v", err)
	}
	for _, flag := range []string{"--yes", "-y"} {
		promptIn = strings.NewReader("")
		if err := runConnect([]string{flag, "http://127.0.0.1:1", "123456"}); err == nil || !strings.Contains(err.Error(), "Hermes 를 찾지 못했다") {
			t.Fatalf("%s must skip the question and reach preflight: %v", flag, err)
		}
	}
	t.Setenv("KYEOT_CONNECT_YES", "1")
	if err := runConnect([]string{"http://127.0.0.1:1", "123456"}); err == nil || !strings.Contains(err.Error(), "Hermes 를 찾지 못했다") {
		t.Fatalf("KYEOT_CONNECT_YES must skip the question and reach preflight: %v", err)
	}
	if b, _ := os.ReadFile(file); string(b) != saved {
		t.Fatalf("saved connection changed: %s", b)
	}
}
