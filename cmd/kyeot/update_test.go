package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Settings in the service file survive a restart from a shell that lacks them; the shell still wins.
func TestRestartKeepsServiceEnv(t *testing.T) {
	odd := `a "b" \c %d <e>&f`
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			h, _ := serverFakeHost(t, goos)
			h.env = map[string]string{"KYEOT_ADDR": "127.0.0.1:17792", "KYEOT_HERMES": "off", "KYEOT_NETS": "loopback", "KYEOT_PUBLIC_URL": odd}
			var out bytes.Buffer
			if err := runService(h, []string{"install"}, &out); err != nil {
				t.Fatal(err, out.String())
			}
			s, _ := serviceFor(h)
			b, _ := os.ReadFile(s.file)
			saved := savedEnv(goos, string(b))
			if saved["KYEOT_PUBLIC_URL"] != odd || saved["KYEOT_HERMES"] != "off" || saved["PATH"] != h.path {
				t.Fatalf("round trip: %#v", saved)
			}

			h.env = map[string]string{"KYEOT_ADDR": "127.0.0.1:17800"} // a new shell with only one setting
			if err := runService(h, []string{"restart"}, &out); err != nil {
				t.Fatal(err, out.String())
			}
			b, _ = os.ReadFile(s.file)
			got := savedEnv(goos, string(b))
			want := map[string]string{"PATH": h.path, "KYEOT_ADDR": "127.0.0.1:17800", "KYEOT_HERMES": "off", "KYEOT_NETS": "loopback", "KYEOT_PUBLIC_URL": odd}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("after restart:\n got %#v\nwant %#v", got, want)
			}
		})
	}
}

// Only variables the spec passes on are carried over; PATH always comes from the current shell.
func TestSavedEnvFiltered(t *testing.T) {
	h, _ := serverFakeHost(t, "linux")
	s, _ := serviceFor(h)
	os.MkdirAll(filepath.Dir(s.file), 0o755)
	os.WriteFile(s.file, []byte("[Service]\nEnvironment=\"PATH=/evil\"\nEnvironment=\"KYEOT_OWNER_TOKEN=secret\"\nEnvironment=\"KYEOT_NETS=all\"\n"), 0o644)
	h.env = map[string]string{}
	got := withSavedEnv(h).env
	if fmt.Sprint(got) != fmt.Sprint(map[string]string{"KYEOT_NETS": "all"}) {
		t.Errorf("got %#v", got)
	}
}

func TestUnquoteSystemd(t *testing.T) {
	for in, want := range map[string]string{`"A=1"`: "A=1", `"A=50%%"`: "A=50%", `"A=\"q\" \\x"`: `A="q" \x`, `A=bare`: "A=bare"} {
		if got := unquoteSystemd(in); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

// The pinned keys must be the ones install.sh trusts.
func TestUpdateKeysMatchInstallScript(t *testing.T) {
	b, err := os.ReadFile("../../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)RELEASE_PUBKEYS='([^']*)'`).FindSubmatch(b)
	if m == nil {
		t.Fatal("RELEASE_PUBKEYS not found")
	}
	if got := strings.Split(strings.TrimSpace(string(m[1])), "\n"); fmt.Sprint(got) != fmt.Sprint(releasePubkeys) {
		t.Errorf("install.sh %v\nupdate.go %v", got, releasePubkeys)
	}
}

type testSigner struct{ key, pub string }

func newSigner(t *testing.T, name string) testSigner {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	k := filepath.Join(t.TempDir(), name)
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "test", "-f", k).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v %s", err, out)
	}
	pub, _ := os.ReadFile(k + ".pub")
	return testSigner{k, strings.TrimSpace(string(pub))}
}

func (s testSigner) sign(t *testing.T, ns string, body []byte, hash string) []byte {
	t.Helper()
	args := []string{"-q", "-Y", "sign", "-f", s.key, "-n", ns}
	if hash != "" {
		args = append(args, "-O", "hashalg="+hash)
	}
	cmd := exec.Command("ssh-keygen", args...)
	cmd.Stdin = bytes.NewReader(body)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return out
}

func TestVerifySSHSig(t *testing.T) {
	good, other := newSigner(t, "good"), newSigner(t, "other")
	msg := []byte("abc  kyeot_linux_arm64\n")
	keys := []string{good.pub}
	for _, hash := range []string{"", "sha256"} {
		if err := verifySSHSig(msg, good.sign(t, releaseNamespace, msg, hash), keys, releaseNamespace); err != nil {
			t.Errorf("good signature (%q) rejected: %v", hash, err)
		}
	}
	cases := map[string]error{
		"other key":      verifySSHSig(msg, other.sign(t, releaseNamespace, msg, ""), keys, releaseNamespace),
		"other ns":       verifySSHSig(msg, good.sign(t, "file", msg, ""), keys, releaseNamespace),
		"changed body":   verifySSHSig(append([]byte("x"), msg...), good.sign(t, releaseNamespace, msg, ""), keys, releaseNamespace),
		"not armored":    verifySSHSig(msg, []byte("junk"), keys, releaseNamespace),
		"no trusted key": verifySSHSig(msg, good.sign(t, releaseNamespace, msg, ""), nil, releaseNamespace),
	}
	for name, err := range cases {
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// fakeRelease serves VERSION, an asset that answers `version`, checksums and a signature.
type fakeRelease struct {
	ranMark string // created when the served asset is executed
	latest  string
	signer  testSigner
	badSum  bool
	noSig   bool
	wrongV  bool // the asset reports a different version
	srv     *httptest.Server
	fetched []string
}

func newFakeRelease(t *testing.T, latest string) *fakeRelease {
	r := &fakeRelease{latest: latest, signer: newSigner(t, "rel"), ranMark: filepath.Join(t.TempDir(), "ran")}
	name := "kyeot_" + runtime.GOOS + "_" + runtime.GOARCH
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.fetched = append(r.fetched, req.URL.Path)
		v := r.latest
		if r.wrongV {
			v = "9.9.9"
		}
		asset := []byte("#!/bin/sh\ntouch '" + r.ranMark + "'\necho " + v + "\n")
		sum := sha256.Sum256(asset)
		if r.badSum {
			sum[0] ^= 1
		}
		sums := []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")
		switch req.URL.Path {
		case "/rel/latest/download/VERSION":
			io.WriteString(w, r.latest+"\n")
		case "/rel/download/v" + r.latest + "/" + name:
			w.Write(asset)
		case "/rel/download/v" + r.latest + "/checksums.txt":
			w.Write(sums)
		case "/rel/download/v" + r.latest + "/checksums.txt.sig":
			if r.noSig {
				http.NotFound(w, req)
				return
			}
			w.Write(r.signer.sign(t, releaseNamespace, sums, ""))
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *fakeRelease) plan(t *testing.T, current string) (updatePlan, *int) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "kyeot")
	os.WriteFile(exe, []byte("old "+current), 0o755)
	restarts := 0
	return updatePlan{current: current, exe: exe, goos: runtime.GOOS, goarch: runtime.GOARCH, base: r.srv.URL + "/rel/download", insecure: true,
		keys: []string{r.signer.pub}, client: r.srv.Client(),
		restart: func(io.Writer) (bool, error) { restarts++; return true, nil },
		running: func() string { return "" }}, &restarts
}

func TestUpdateInstallsAndRestarts(t *testing.T) {
	r := newFakeRelease(t, "0.2.0")
	p, restarts := r.plan(t, "0.1.5")
	var out bytes.Buffer
	if err := runUpdate(p, nil, &out); err != nil {
		t.Fatal(err, out.String())
	}
	if got, _ := exec.Command(p.exe, "version").Output(); strings.TrimSpace(string(got)) != "0.2.0" {
		t.Errorf("new file: %q", got)
	}
	if prev, _ := os.ReadFile(filepath.Join(filepath.Dir(p.exe), "kyeot.prev")); string(prev) != "old 0.1.5" {
		t.Errorf("kyeot.prev: %q", prev)
	}
	if *restarts != 1 {
		t.Errorf("restarts: %d", *restarts)
	}
	mustContain(t, out.String(), "0.1.5 → 0.2.0", "kyeot.prev", rollbackDoc)
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(p.exe), ".kyeot.*")); len(left) > 0 {
		t.Errorf("leftovers: %v", left)
	}
}

func TestUpdateCheckAndUpToDate(t *testing.T) {
	r := newFakeRelease(t, "0.2.0")
	p, restarts := r.plan(t, "0.1.5")
	var out bytes.Buffer
	if err := runUpdate(p, []string{"--check"}, &out); err != nil || !strings.Contains(out.String(), "0.1.5 → 0.2.0") {
		t.Fatalf("check: %v %s", err, out.String())
	}
	if b, _ := os.ReadFile(p.exe); string(b) != "old 0.1.5" || *restarts != 0 {
		t.Error("--check must not change anything")
	}
	for _, cur := range []string{"0.2.0", "0.10.0"} { // 0.10.0 > 0.2.0 numerically
		out.Reset()
		p, _ := r.plan(t, cur)
		if err := runUpdate(p, nil, &out); err != nil || !strings.Contains(out.String(), "이미 최신이다") {
			t.Errorf("%s: %v %s", cur, err, out.String())
		}
	}
}

// Every bad release leaves the installed file untouched and nothing restarted.
func TestUpdateRefusesBadRelease(t *testing.T) {
	cases := map[string]func(r *fakeRelease, p *updatePlan){
		"bad checksum":   func(r *fakeRelease, _ *updatePlan) { r.badSum = true },
		"no signature":   func(r *fakeRelease, _ *updatePlan) { r.noSig = true },
		"untrusted key":  func(_ *fakeRelease, p *updatePlan) { p.keys = []string{newSigner(t, "x").pub} },
		"wrong version":  func(r *fakeRelease, _ *updatePlan) { r.wrongV = true },
		"http not test":  func(_ *fakeRelease, p *updatePlan) { p.insecure = false },
		"dev build":      func(_ *fakeRelease, p *updatePlan) { p.current = "dev" },
		"brew install":   func(_ *fakeRelease, p *updatePlan) { p.exe = "/opt/homebrew/Cellar/kyeot/0.1.5/bin/kyeot" },
		"unknown option": nil,
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			r := newFakeRelease(t, "0.2.0")
			p, restarts := r.plan(t, "0.1.5")
			exe := p.exe
			args := []string(nil)
			if mut == nil {
				args = []string{"--force"}
			} else {
				mut(r, &p)
			}
			var out bytes.Buffer
			if err := runUpdate(p, args, &out); err == nil {
				t.Fatalf("accepted: %s", out.String())
			}
			if b, _ := os.ReadFile(exe); string(b) != "old 0.1.5" || *restarts != 0 {
				t.Errorf("changed the install: %q restarts %d", b, *restarts)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "kyeot.prev")); err == nil {
				t.Error("kyeot.prev written for a refused update")
			}
			if _, err := os.Stat(r.ranMark); err == nil && name != "wrong version" {
				t.Error("ran a file that was not verified")
			}
		})
	}
}

// Without a registered service, a server still running is reported as old.
func TestUpdateWarnsAboutForegroundServer(t *testing.T) {
	r := newFakeRelease(t, "0.2.0")
	p, _ := r.plan(t, "0.1.5")
	p.restart = func(io.Writer) (bool, error) { return false, nil }
	p.running = func() string { return "running" }
	var out bytes.Buffer
	if err := runUpdate(p, nil, &out); err != nil {
		t.Fatal(err)
	}
	mustContain(t, out.String(), "아직 옛 판이다")
}

func TestVersionLess(t *testing.T) {
	for _, c := range [][3]string{{"0.1.5", "0.2.0", "y"}, {"0.2.0", "0.1.5", ""}, {"0.9.0", "0.10.0", "y"}, {"1.0.0", "1.0.0", ""}} {
		if got := versionLess(c[0], c[1]); got != (c[2] == "y") {
			t.Errorf("%s < %s: %v", c[0], c[1], got)
		}
	}
}

// The test signing key is honoured only together with KYEOT_INSECURE_TEST=1.
func TestTestKeyNeedsInsecure(t *testing.T) {
	t.Setenv("KYEOT_TEST_RELEASE_PUBKEY", "ssh-ed25519 AAAAtest")
	t.Setenv("KYEOT_INSECURE_TEST", "")
	if p := realUpdatePlan(); fmt.Sprint(p.keys) != fmt.Sprint(releasePubkeys) || p.insecure {
		t.Fatalf("test key used without KYEOT_INSECURE_TEST=1: %v", p.keys)
	}
	t.Setenv("KYEOT_INSECURE_TEST", "1")
	if p := realUpdatePlan(); fmt.Sprint(p.keys) != "[ssh-ed25519 AAAAtest]" {
		t.Fatalf("test key ignored with KYEOT_INSECURE_TEST=1: %v", p.keys)
	}
}

// Redirects that leave https are refused unless testing.
func TestReleaseClientRefusesPlainRedirect(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "9.9.9") }))
	defer plain.Close()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, plain.URL+"/VERSION", http.StatusFound)
	}))
	defer tls.Close()
	c := releaseClient(false)
	c.Transport = tls.Client().Transport
	if _, err := fetch(t.Context(), c, tls.URL+"/VERSION", 100); err == nil {
		t.Fatal("followed a redirect to http")
	}
	c = releaseClient(true)
	c.Transport = tls.Client().Transport
	if b, err := fetch(t.Context(), c, tls.URL+"/VERSION", 100); err != nil || string(b) != "9.9.9" {
		t.Fatalf("insecure test mode: %q %v", b, err)
	}
}
