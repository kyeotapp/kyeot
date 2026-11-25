package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenToInternet(t *testing.T) {
	cases := map[string]bool{
		"loopback,tailscale,lan":     false,
		"127.0.0.1/32":               false,
		"192.168.1.0/24, 10.0.0.0/8": false, // public-ok
		"fd00::/8":                   false,
		"all":                        true,
		"lan, ::/0":                  true,
		"0.0.0.0/0":                  true,
		"203.0.113.0/24":             true,
		"loopback,8.8.8.8/32":        true,
		"172.0.0.0/8":                true, // wider than 172.16/12
		"100.0.0.0/8":                true, // wider than the Tailscale range
	}
	for spec, want := range cases {
		if got := openToInternet(spec); got != want {
			t.Errorf("%q: %v", spec, got)
		}
	}
}

func TestCheckPlain(t *testing.T) {
	cases := []struct {
		spec       string
		tls, allow bool
		refuse     bool
	}{
		{"all", false, false, true},
		{"203.0.113.0/24", false, false, true},
		{"all", true, false, false},
		{"all", false, true, false},
		{defaultNets, false, false, false},
	}
	for _, c := range cases {
		err := checkPlain(c.spec, c.tls, c.allow)
		if (err != nil) != c.refuse {
			t.Errorf("%q tls=%v allow=%v: %v", c.spec, c.tls, c.allow, err)
		}
		if err != nil && (!strings.Contains(err.Error(), "KYEOT_TLS_CERT") || !strings.Contains(err.Error(), "KYEOT_ALLOW_PLAIN=1")) {
			t.Errorf("the fix instructions are missing: %v", err)
		}
	}
}

// At the executable level: all+plaintext exits non-zero; ALLOW_PLAIN or the default keeps running.
func TestPlainInternetRefusedAtStart(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := filepath.Join(t.TempDir(), "kyeot")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	run := func(extra ...string) (exited bool, code int, stderr string) {
		home := t.TempDir()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin)
		cmd.Dir = home
		cmd.Env = append([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "KYEOT_ADDR=127.0.0.1:0", "KYEOT_HERMES=off", "KYEOT_SENSORS=off", "KYEOT_LANG=ko"}, extra...)
		var errb strings.Builder
		cmd.Stderr = &errb
		err := cmd.Run()
		if ctx.Err() != nil {
			return false, 0, errb.String()
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		return true, code, errb.String()
	}
	if exited, code, out := run("KYEOT_NETS=all"); !exited || code == 0 || !strings.Contains(out, "시작 거부") {
		t.Fatalf("all+plaintext is rejected: exited=%v code=%d %q", exited, code, out)
	}
	if exited, _, out := run("KYEOT_NETS=all", "KYEOT_ALLOW_PLAIN=1"); exited || !strings.Contains(out, "allowed by KYEOT_ALLOW_PLAIN") {
		t.Fatalf("with ALLOW_PLAIN it warns and starts: exited=%v %q", exited, out)
	}
	if exited, _, out := run(); exited {
		t.Fatalf("the default starts: %q", out)
	}
	if exited, code, out := run("KYEOT_PUBLIC_URL=http://kyeot.example.com"); !exited || code == 0 {
		t.Fatalf("a public http address is rejected: exited=%v code=%d %q", exited, code, out)
	}
}

func TestPairServerFullURL(t *testing.T) {
	t.Setenv("KYEOT_TLS_CERT", "")
	t.Setenv("KYEOT_PUBLIC_URL", "")
	t.Setenv("KYEOT_ALLOW_PLAIN", "")
	ok := map[string]string{
		"https://kyeot.example.com":  "https://kyeot.example.com",
		"https://kyeot.example.com/": "https://kyeot.example.com",
		"https://x.example.com:8443": "https://x.example.com:8443",
		"HTTPS://X.example.com":      "https://X.example.com",
		"http://192.168.0.9:8080":    "http://192.168.0.9:8080", // public-ok
		"http://mac.tail1.ts.net":    "http://mac.tail1.ts.net",
		"100.64.0.1":                 "http://100.64.0.1:7700", // public-ok
		"mac-mini":                   "http://mac-mini:7700",
	}
	for in, want := range ok {
		if got, err := pairServer("0.0.0.0:7700", in); err != nil || got != want {
			t.Errorf("%q → %q %v (want %q)", in, got, err, want)
		}
	}
	for _, in := range []string{"http://kyeot.example.com", "http://203.0.113.5:7700", "kyeot.example.com", "ftp://x.example.com", "https://x.example.com/deck", "https://u:p@x.example.com"} {
		if got, err := pairServer("0.0.0.0:7700", in); err == nil {
			t.Errorf("%q is rejected: %q", in, got)
		}
	}
	t.Setenv("KYEOT_ALLOW_PLAIN", "1")
	if got, err := pairServer("0.0.0.0:7700", "http://kyeot.example.com"); err != nil || got != "http://kyeot.example.com" {
		t.Errorf("allowed with ALLOW_PLAIN: %q %v", got, err)
	}
}

func TestPairServerPublicURL(t *testing.T) {
	t.Setenv("KYEOT_TLS_CERT", "")
	t.Setenv("KYEOT_ALLOW_PLAIN", "")
	t.Setenv("KYEOT_PUBLIC_URL", "https://kyeot.example.com")
	if got, err := pairServer("127.0.0.1:7700", ""); err != nil || got != "https://kyeot.example.com" {
		t.Fatalf("PUBLIC_URL is the default: %q %v", got, err)
	}
	if got, _ := pairServer("127.0.0.1:7700", "100.64.0.1"); got != "http://100.64.0.1:7700" { // public-ok
		t.Fatalf("-host wins: %q", got)
	}
	var b strings.Builder
	if err := showPair(&b, "https://kyeot.example.com", "tok", "127.0.0.1:7700"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "폰이 닿지 않는다") {
		t.Fatalf("no loopback warning when PUBLIC_URL is set")
	}
	t.Setenv("KYEOT_PUBLIC_URL", "")
	b.Reset()
	showPair(&b, "http://100.64.0.1:7700", "tok", "127.0.0.1:7700") // public-ok
	if !strings.Contains(b.String(), "폰이 닿지 않는다") {
		t.Fatalf("loopback warning when PUBLIC_URL is unset")
	}
	for _, bad := range []string{"kyeot.example.com", "http://kyeot.example.com"} {
		t.Setenv("KYEOT_PUBLIC_URL", bad)
		if _, err := pairServer("0.0.0.0:7700", ""); err == nil {
			t.Errorf("PUBLIC_URL %q is rejected", bad)
		}
	}
}
