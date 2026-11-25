package scripts

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Runs install.sh against a fake server and a fake release. The fake kyeot only records its arguments.

type rig struct {
	t         *testing.T
	shell     string
	home      string
	tmp       string
	fakebin   string
	srv       *httptest.Server
	version   string // body of the /version response
	status    int    // status code of /version
	latest    string // latest release version
	corrupt   bool
	tamper    bool   // change the executable and checksum together, leaving the signature as the old one
	sigMode   string // "" normal, "missing" no signature, "other" signed with another key, "backup" signed with the spare key
	key       string // signing key for tests
	otherKey  string
	backupKey string
	pubkey    string
	downloads atomic.Int32
	mu        sync.Mutex // lets the handler see values the test changed
}

func assetName() string {
	return fmt.Sprintf("kyeot_%s_%s", runtime.GOOS, runtime.GOARCH)
}

// records the arguments it was called with, in order. With FAKE_INSTALL_FAIL=1, service install fails
func fakeKyeot(v string) string {
	return "#!/bin/sh\nif [ \"$1\" = version ]; then echo " + v + "; exit 0; fi\nprintf '%s\\n' \"$@\" >> \"$HOME/connect-args\"\nif [ \"$1\" = connect ]; then printf '%s' \"${KYEOT_CONNECT_YES:-}\" > \"$HOME/connect-yes\"; fi\n" +
		"if [ \"$1 $2\" = 'service install' ] && [ \"${FAKE_INSTALL_FAIL:-}\" = 1 ]; then echo 'service install is not supported on this OS' >&2; exit 1; fi\n" +
		"if [ \"$1\" = pair ]; then echo '[QR]'; fi\n" +
		"if [ \"$1 $2\" = 'service restart' ] && [ \"${FAKE_RESTART_FAIL:-}\" = 1 ]; then exit 1; fi\n"
}

// Creates an ed25519 key for tests and returns the path and public key
func testKey(t *testing.T, dir, name string) (string, string) {
	t.Helper()
	k := filepath.Join(dir, name)
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "test", "-f", k).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v %s", err, out)
	}
	pub, err := os.ReadFile(k + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	return k, strings.TrimSpace(string(pub))
}

func sign(key, body string) ([]byte, error) {
	cmd := exec.Command("ssh-keygen", "-q", "-Y", "sign", "-f", key, "-n", "kyeot-release")
	cmd.Stdin = strings.NewReader(body)
	return cmd.Output()
}

func newRig(t *testing.T, shell string) *rig {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	dir := t.TempDir()
	r := &rig{t: t, shell: shell, home: filepath.Join(dir, "home"), tmp: filepath.Join(dir, "tmp"), fakebin: filepath.Join(dir, "fakebin"),
		version: `{"version":"0.1.5"}`, status: 200, latest: "0.2.0"}
	for _, d := range []string{r.home, r.tmp, r.fakebin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r.key, r.pubkey = testKey(t, dir, "key")
	r.otherKey, _ = testKey(t, dir, "other")
	var backupPub string
	r.backupKey, backupPub = testKey(t, dir, "backup")
	r.pubkey += "\n" + backupPub // both the usual key and the spare key, like install.sh
	mux := http.NewServeMux()
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		w.WriteHeader(r.status)
		fmt.Fprintln(w, r.version)
	})
	serve := func(w http.ResponseWriter, req *http.Request, v, file string) {
		r.downloads.Add(1)
		r.mu.Lock()
		defer r.mu.Unlock()
		honest := fakeKyeot(v)
		body := honest
		if r.tamper {
			body += "# tampered\n"
		}
		sums := func(b string, corrupt bool) string {
			sum := sha256.Sum256([]byte(b))
			if corrupt {
				sum[0] ^= 1
			}
			return fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), assetName())
		}
		switch file {
		case "VERSION":
			fmt.Fprintln(w, v)
		case assetName():
			fmt.Fprint(w, body)
		case "checksums.txt":
			fmt.Fprint(w, sums(body, r.corrupt))
		case "checksums.txt.sig":
			key := r.key
			switch r.sigMode {
			case "missing":
				http.NotFound(w, req)
				return
			case "other":
				key = r.otherKey
			case "backup":
				key = r.backupKey
			}
			sig, err := sign(key, sums(honest, r.corrupt))
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			w.Write(sig)
		default:
			http.NotFound(w, req)
		}
	}
	mux.HandleFunc("GET /rel/{tag}/{file}", func(w http.ResponseWriter, req *http.Request) {
		serve(w, req, strings.TrimPrefix(req.PathValue("tag"), "v"), req.PathValue("file"))
	})
	mux.HandleFunc("GET /rel/latest/download/{file}", func(w http.ResponseWriter, req *http.Request) { // latest release when installing without a server
		r.mu.Lock()
		latest := r.latest
		r.mu.Unlock()
		serve(w, req, latest, req.PathValue("file"))
	})
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)
	return r
}

func (r *rig) bin() string { return filepath.Join(r.home, ".local", "bin", "kyeot") }

func (r *rig) run(extraEnv []string, args ...string) (int, string) {
	r.t.Helper()
	r.mu.Lock()
	r.mu.Unlock() // hand the previously changed values to the handler
	cmd := exec.Command(r.shell, append([]string{"install.sh"}, args...)...)
	cmd.Env = append([]string{
		"HOME=" + r.home,
		"TMPDIR=" + r.tmp,
		"PATH=" + r.fakebin + ":/usr/bin:/bin:/usr/sbin:/sbin",
		"KYEOT_RELEASE_BASE=" + r.srv.URL + "/rel/",
		"KYEOT_INSECURE_TEST=1",
		"KYEOT_TEST_RELEASE_PUBKEY=" + r.pubkey,
		"KYEOT_LANG=ko", // the existing expected text is Korean; English is in TestInstallEnglish
	}, extraEnv...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		r.t.Fatal(err)
	}
	return code, string(out)
}

func (r *rig) connectArgs() string {
	b, _ := os.ReadFile(filepath.Join(r.home, "connect-args"))
	return string(b)
}

func (r *rig) assertClean() {
	r.t.Helper()
	if left, _ := os.ReadDir(r.tmp); len(left) > 0 {
		r.t.Errorf("temp folder left behind: %v", left)
	}
	if junk, _ := filepath.Glob(filepath.Join(r.home, ".local", "bin", ".kyeot.*")); len(junk) > 0 {
		r.t.Errorf("leftovers remain: %v", junk)
	}
}

func (r *rig) assertNotInstalled(code int, out string) {
	r.t.Helper()
	if code == 0 {
		r.t.Errorf("must not succeed: %s", out)
	}
	if _, err := os.Stat(r.bin()); err == nil {
		r.t.Error("must not be installed")
	}
	if r.connectArgs() != "" {
		r.t.Error("must not call connect")
	}
	r.assertClean()
}

func shells(t *testing.T, f func(t *testing.T, r *rig)) {
	for _, sh := range []string{"sh", "dash"} {
		if _, err := exec.LookPath(sh); err != nil {
			continue
		}
		t.Run(sh, func(t *testing.T) { f(t, newRig(t, sh)) })
	}
}

func TestInstallOK(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		code, out := r.run(nil, r.srv.URL+"/", "123456")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, out)
		}
		st, err := os.Stat(r.bin())
		if err != nil || st.Mode().Perm() != 0o755 {
			t.Fatalf("bin: %v %v", st, err)
		}
		if got, want := r.connectArgs(), "connect\n"+r.srv.URL+"\n123456\n"; got != want {
			t.Errorf("connect args %q, want %q", got, want)
		}
		if b, _ := os.ReadFile(filepath.Join(r.home, "connect-yes")); string(b) != "1" {
			t.Errorf("must pass KYEOT_CONNECT_YES=1 to connect (so it doesn't ask about the saved connection): %q", b)
		}
		r.assertClean()
	})
}

func TestInstallForeground(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		if code, out := r.run(nil, r.srv.URL, "123456", "--foreground"); code != 0 {
			t.Fatalf("exit %d: %s", code, out)
		}
		if got, want := r.connectArgs(), "connect\n--foreground\n"+r.srv.URL+"\n123456\n"; got != want {
			t.Errorf("connect args %q, want %q", got, want)
		}
	})
}

func TestInstallBadChecksum(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		r.corrupt = true
		code, out := r.run(nil, r.srv.URL, "123456")
		r.assertNotInstalled(code, out)
		if !strings.Contains(out, "변조") {
			t.Errorf("no guidance message: %s", out)
		}
	})
}

func TestInstallSignatureRejected(t *testing.T) {
	for _, c := range []struct {
		name, want string
		set        func(r *rig)
		env        []string
	}{
		{"checksum changed", "서명이 맞지 않는다", func(r *rig) { r.tamper = true }, nil},
		{"no signature", "서명(checksums.txt.sig)이 없다", func(r *rig) { r.sigMode = "missing" }, nil},
		{"other key", "서명이 맞지 않는다", func(r *rig) { r.sigMode = "other" }, nil},
		{"embedded key", "서명이 맞지 않는다", func(*rig) {}, []string{"KYEOT_TEST_RELEASE_PUBKEY="}},
	} {
		t.Run(c.name, func(t *testing.T) {
			shells(t, func(t *testing.T, r *rig) {
				r.mu.Lock()
				c.set(r)
				r.mu.Unlock()
				code, out := r.run(c.env, r.srv.URL, "123456")
				r.assertNotInstalled(code, out)
				if !strings.Contains(out, c.want) {
					t.Errorf("no guidance message: %s", out)
				}
			})
		})
	}
}

// A release signed with the spare key (the usual key being lost) also installs
func TestInstallBackupKeySignature(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		r.mu.Lock()
		r.sigMode = "backup"
		r.mu.Unlock()
		code, out := r.run(nil, r.srv.URL, "123456")
		if code != 0 {
			t.Fatalf("spare-key signature was rejected: exit %d: %s", code, out)
		}
		if _, err := os.Stat(r.bin()); err != nil {
			t.Fatal("not installed")
		}
	})
}

// The embedded public keys are two: the usual key and the spare key
func TestInstallPinsTwoReleaseKeys(t *testing.T) {
	src, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, _ := strings.Cut(string(src), "RELEASE_PUBKEYS='")
	block, _, _ := strings.Cut(rest, "'")
	if n := strings.Count(block, "ssh-ed25519 "); n != 2 {
		t.Fatalf("%d public keys, want 2:\n%s", n, block)
	}
}

func TestInstallOldSSHKeygen(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		os.WriteFile(filepath.Join(r.fakebin, "ssh-keygen"), []byte("#!/bin/sh\necho 'ssh-keygen: illegal option -- Y' >&2\nexit 1\n"), 0o755)
		code, out := r.run(nil, r.srv.URL, "123456")
		r.assertNotInstalled(code, out)
		if !strings.Contains(out, "8.0 이상") {
			t.Errorf("no guidance message: %s", out)
		}
		if n := r.downloads.Load(); n != 0 {
			t.Errorf("must not download: %d", n)
		}
	})
}

func TestInstallUsage(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		u := r.srv.URL
		cases := [][]string{
			{u, "12345"}, {u, "1234567"}, {u, "abcdef"}, {u, "12345a"}, {u, ""}, {u, "123456\n"},
			{"http://a;b", "123456"}, {"http://$(id)", "123456"}, {"http://a'b", "123456"}, {u + "/x", "123456"},
			{u + "/install.sh", "123456"}, {"ftp://a", "123456"}, {"127.0.0.1:7700", "123456"}, {"http://a b", "123456"},
			{"http://a\nb", "123456"}, {"http://user@a", "123456"}, {"http://a`id`", "123456"}, {"http://a:1:2", "123456"},
			{"http://-a", "123456"}, {"http://a:123456", "123456"},
			{u}, {u, "123456", "--foreground", "x"}, {u, "123456", "--nope"}, {"--foreground"},
		}
		for _, args := range cases {
			code, out := r.run(nil, args...)
			if code != 2 {
				t.Errorf("%q: exit %d %s", args, code, out)
			}
		}
		if n := r.downloads.Load(); n != 0 {
			t.Errorf("must not download: %d", n)
		}
		r.assertNotInstalled(1, "")
	})
}

func TestInstallBadVersion(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		for _, v := range []string{`{"version":"0.1"}`, `{"version":"1.2.3-rc1"}`, `{"version":"../../x"}`, `{"version":""}`, `{"version":"0.1.0"}` + "\n" + `{"version":"0.2.0"}`, `oops`} {
			r.version = v
			code, out := r.run(nil, r.srv.URL, "123456")
			if code == 0 || !strings.Contains(out, "판이 올바르지") {
				t.Errorf("%q: exit %d %s", v, code, out)
			}
		}
		if n := r.downloads.Load(); n != 0 {
			t.Errorf("must not download: %d", n)
		}
		r.assertNotInstalled(1, "")
	})
}

func TestInstallMinVersion(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		for _, v := range []string{"0.0.9", "0.1.0", "0.1.4"} { // versions before signing
			r.version = `{"version":"` + v + `"}`
			code, out := r.run(nil, r.srv.URL, "123456")
			if code == 0 || !strings.Contains(out, "너무 낮다") {
				t.Errorf("%s: exit %d %s", v, code, out)
			}
		}
		if n := r.downloads.Load(); n != 0 {
			t.Errorf("must not download: %d", n)
		}
		r.assertNotInstalled(1, "")
		for _, v := range []string{"0.1.5", "0.1.10", "1.0.0"} {
			r.version = `{"version":"` + v + `"}`
			if code, out := r.run(nil, r.srv.URL, "123456"); code != 0 {
				t.Errorf("%s: exit %d %s", v, code, out)
			}
		}
	})
}

func TestInstallSameVersionSkipsDownload(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		os.MkdirAll(filepath.Dir(r.bin()), 0o755)
		os.WriteFile(r.bin(), []byte(fakeKyeot("0.1.5")), 0o755)
		code, out := r.run(nil, r.srv.URL, "123456")
		if code != 0 || !strings.Contains(out, "이미 있다") {
			t.Fatalf("exit %d: %s", code, out)
		}
		if n := r.downloads.Load(); n != 0 {
			t.Errorf("downloaded again: %d", n)
		}
		if r.connectArgs() == "" {
			t.Error("connect was not called")
		}
	})
}

func TestInstallBinIsDir(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		os.MkdirAll(r.bin(), 0o755)
		code, out := r.run(nil, r.srv.URL, "123456")
		if code == 0 || r.connectArgs() != "" || r.downloads.Load() != 0 {
			t.Errorf("exit %d: %s", code, out)
		}
	})
}

func TestInstallInsecureBaseNeedsTestFlag(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		for _, env := range [][]string{{"KYEOT_INSECURE_TEST="}, {"KYEOT_INSECURE_TEST=yes"}, {"KYEOT_RELEASE_BASE=ftp://x"}} {
			code, out := r.run(env, r.srv.URL, "123456")
			if code == 0 || !strings.Contains(out, "https") {
				t.Errorf("%v: exit %d %s", env, code, out)
			}
		}
		if n := r.downloads.Load(); n != 0 {
			t.Errorf("must not download: %d", n)
		}
		r.assertNotInstalled(1, "")
	})
}

func TestInstallRefusesRoot(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		os.WriteFile(filepath.Join(r.fakebin, "id"), []byte("#!/bin/sh\necho 0\n"), 0o755)
		code, out := r.run(nil, r.srv.URL, "123456")
		if !strings.Contains(out, "root") {
			t.Errorf("no guidance message: %s", out)
		}
		r.assertNotInstalled(code, out)
	})
}

func TestInstallDevServer(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		r.status, r.version = 503, "dev server has no version"
		code, out := r.run(nil, r.srv.URL, "123456")
		if !strings.Contains(out, "개발판") {
			t.Errorf("no guidance message: %s", out)
		}
		r.assertNotInstalled(code, out)
	})
}

func TestInstallUnreachable(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		code, out := r.run(nil, "http://127.0.0.1:1", "123456")
		if !strings.Contains(out, "닿지 않는다") {
			t.Errorf("no guidance message: %s", out)
		}
		r.assertNotInstalled(code, out)
	})
}

func TestInstallReachHint(t *testing.T) {
	src, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(src), "\nmain \"$@\"\n", "\nreach_hint \"$1\"\n", 1)
	if body == string(src) {
		t.Fatal("could not find the main call line")
	}
	f := filepath.Join(t.TempDir(), "hint.sh")
	os.WriteFile(f, []byte(body), 0o644)
	for addr, want := range map[string]string{
		"http://127.0.0.1:7700": "", "https://kyeot.example.com": "", "http://100.128.0.1": "",
		"http://192.168.0.10:7700": "와이파이", "http://10.0.0.5": "와이파이", "http://172.20.1.1:7700": "와이파이", "http://my-mac.local:7700": "와이파이", "http://[fd7a::1]:7700": "와이파이", // public-ok
		"http://my-mac.tail1.ts.net:7700": "Tailscale", "http://100.100.1.1:7700": "Tailscale", "http://100.64.0.1": "Tailscale", "http://MY-MAC.TAIL1.TS.NET": "Tailscale", // public-ok
	} {
		cmd := exec.Command("sh", f, addr)
		cmd.Env = append(os.Environ(), "KYEOT_LANG=ko")
		out, _ := cmd.CombinedOutput()
		if (want == "") != (len(out) == 0) || !strings.Contains(string(out), want) {
			t.Errorf("%s: %q", addr, out)
		}
	}
}

func TestInstallServerOnly(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		code, out := r.run(nil)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, out)
		}
		if st, err := os.Stat(r.bin()); err != nil || st.Mode()&0o111 == 0 {
			t.Fatalf("bin: %v %v", st, err)
		}
		if got, want := r.connectArgs(), "service\ninstall\npair\n"; got != want {
			t.Errorf("installing without a server registers the service, then shows the QR: %q, want %q", got, want)
		}
		if !strings.Contains(out, "0.2.0") || !strings.Contains(out, "[QR]") || !strings.Contains(out, "service uninstall") || !strings.Contains(out, "kyeot pair") || !strings.Contains(out, "에이전트 추가") {
			t.Errorf("no guidance message: %s", out)
		}
		if n := r.downloads.Load(); n != 4 {
			t.Errorf("must download only VERSION, executable, checksums, and signature: %d", n)
		}
		r.assertClean()
	})
}

// When installing a new version, only the one immediately previous version is kept as kyeot.prev. On a first install there is nothing to keep.
func TestInstallKeepsPreviousVersion(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		prev := filepath.Join(filepath.Dir(r.bin()), "kyeot.prev")
		if code, out := r.run(nil); code != 0 {
			t.Fatalf("exit %d: %s", code, out)
		}
		if _, err := os.Stat(prev); err == nil {
			t.Fatal("kyeot.prev appeared after a first install")
		}
		old := fakeKyeot("0.1.5")
		os.WriteFile(r.bin(), []byte(old), 0o755)
		code, out := r.run(nil)
		if code != 0 || !strings.Contains(out, "kyeot.prev") {
			t.Fatalf("exit %d: %s", code, out)
		}
		got, err := os.ReadFile(prev)
		if err != nil || string(got) != old {
			t.Fatalf("kyeot.prev is not the previous version: %v", err)
		}
		if st, _ := os.Stat(prev); st.Mode()&0o111 == 0 {
			t.Fatal("cannot execute kyeot.prev")
		}
		if cur, _ := os.ReadFile(r.bin()); string(cur) == old {
			t.Fatal("was not replaced by the new version")
		}
		if code, out := r.run(nil); code != 0 { // reinstalling the same version downloads nothing, so the previous version stays
			t.Fatalf("exit %d: %s", code, out)
		}
		if got, _ := os.ReadFile(prev); string(got) != old {
			t.Fatal("reinstalling the same version overwrote kyeot.prev")
		}
		r.assertClean()
	})
}

// In an English environment, the install script's output has no Korean. The fake kyeot's output is only [QR].
func TestInstallEnglish(t *testing.T) {
	hangul := func(s string) bool {
		return strings.IndexFunc(s, func(r rune) bool { return r >= 0xAC00 && r <= 0xD7A3 }) >= 0
	}
	enEnv := []string{"KYEOT_LANG=", "LC_ALL=", "LC_MESSAGES=", "LANG=en_US.UTF-8"}
	shells(t, func(t *testing.T, r *rig) {
		cases := []struct {
			name  string
			env   []string
			args  []string
			want  string
			fresh bool
		}{
			{"server", nil, nil, "“Add agent”", true},
			{"again", nil, nil, "is already installed", false},
			{"no-service", []string{"KYEOT_NO_SERVICE=1"}, nil, "“Scan QR”", false},
			{"usage", nil, []string{"x"}, "usage: sh install.sh", false},
			{"unreachable", nil, []string{"http://127.0.0.1:1", "123456"}, "Can't reach the server", false},
		}
		for _, c := range cases {
			code, out := r.run(append(append([]string{}, enEnv...), c.env...), c.args...)
			if hangul(out) {
				t.Errorf("%s: contains Korean:\n%s", c.name, out)
			}
			if !strings.Contains(out, c.want) {
				t.Errorf("%s: %q missing (exit %d):\n%s", c.name, c.want, code, out)
			}
			if c.fresh && (code != 0 || !strings.Contains(out, "Downloading kyeot 0.2.0") || !strings.Contains(out, "Installed:")) {
				t.Errorf("%s: exit %d:\n%s", c.name, code, out)
			}
		}
	})
}

func TestInstallServerOnlyBadVersionFile(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		r.mu.Lock()
		r.latest = "dev"
		r.mu.Unlock()
		code, out := r.run(nil)
		r.assertNotInstalled(code, out)
	})
}

func TestInstallServerOnlyRegisteredRestarts(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		for _, f := range []string{"Library/LaunchAgents/app.kyeot.server.plist", ".config/systemd/user/kyeot-server.service"} {
			r2 := newRig(t, r.shell)
			p := filepath.Join(r2.home, f)
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, []byte("x"), 0o644)
			code, out := r2.run(nil)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, out)
			}
			if got, want := r2.connectArgs(), "service\nrestart\n"; got != want { // no pair: the token QR is not shown again
				t.Errorf("restart when %s exists: %q, want %q", f, got, want)
			}
			if strings.Contains(out, "[QR]") {
				t.Errorf("an update must not print the token QR: %s", out)
			}
		}
	})
}

func TestInstallServerOnlyNoService(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		code, out := r.run([]string{"KYEOT_NO_SERVICE=1"})
		if code != 0 {
			t.Fatalf("exit %d: %s", code, out)
		}
		if strings.Contains(r.connectArgs(), "service") {
			t.Errorf("must not call service: %q", r.connectArgs())
		}
		if !strings.Contains(out, "서버를 시작하려면") {
			t.Errorf("no run guidance: %s", out)
		}
	})
}

func TestInstallServerOnlyServiceFails(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		code, out := r.run([]string{"FAKE_INSTALL_FAIL=1"})
		if code != 0 {
			t.Fatalf("installation succeeds even if registration fails. exit %d: %s", code, out)
		}
		if _, err := os.Stat(r.bin()); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "not supported") || !strings.Contains(out, "직접 띄운다:   "+r.bin()) {
			t.Errorf("no reason or manual start instructions: %s", out)
		}
		if strings.Contains(r.connectArgs(), "pair") {
			t.Errorf("pair was called without a server: %q", r.connectArgs())
		}
	})
}

// Updating a kyeot that runs without a service must not register one, and must say the running server is old.
func TestInstallUpdateWithoutServiceKeepsMode(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		os.MkdirAll(filepath.Dir(r.bin()), 0o755)
		os.WriteFile(r.bin(), []byte(fakeKyeot("0.1.5")), 0o755)
		code, out := r.run(nil)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, out)
		}
		if args := r.connectArgs(); strings.Contains(args, "service") || strings.Contains(args, "pair") {
			t.Errorf("must not register a service or show the QR: %q", args)
		}
		if !strings.Contains(out, "옛 판이다") {
			t.Errorf("must say the running server is old: %s", out)
		}
		code, out = r.run(nil) // same version again: nothing replaced, so no "old version" claim
		if code != 0 || strings.Contains(out, "옛 판이다") || !strings.Contains(out, "서비스로 등록돼 있지 않다") {
			t.Errorf("same-version rerun: exit %d: %s", code, out)
		}
	})
}

// KYEOT_NO_SERVICE=1 on an update only replaces the file and points at the restart.
func TestInstallUpdateNoServiceFlag(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		p := filepath.Join(r.home, ".config/systemd/user/kyeot-server.service")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
		os.MkdirAll(filepath.Dir(r.bin()), 0o755)
		os.WriteFile(r.bin(), []byte(fakeKyeot("0.1.5")), 0o755)
		code, out := r.run([]string{"KYEOT_NO_SERVICE=1"})
		if code != 0 || r.connectArgs() != "" {
			t.Fatalf("must not touch the service: exit %d args %q: %s", code, r.connectArgs(), out)
		}
		if !strings.Contains(out, "service restart") {
			t.Errorf("must say how to restart: %s", out)
		}
	})
}

// A failed restart must not be reported as a restart, and the script must fail.
func TestInstallRestartFailureReported(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		p := filepath.Join(r.home, ".config/systemd/user/kyeot-server.service")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
		code, out := r.run([]string{"FAKE_RESTART_FAIL=1"})
		if code == 0 || strings.Contains(out, "다시 켰다") || !strings.Contains(out, "다시 켜지 못했다") {
			t.Errorf("exit %d: %s", code, out)
		}
	})
}

// Updating a computer with a registered connect service restarts it in place, without rewriting its file.
func TestInstallUpdateRestartsConnectService(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		log := filepath.Join(r.home, "svc-calls")
		for _, tool := range []string{"launchctl", "systemctl"} { // never reach the real service manager
			os.WriteFile(filepath.Join(r.fakebin, tool), []byte("#!/bin/sh\necho "+tool+" \"$@\" >> "+log+"\n"), 0o755)
		}
		f := filepath.Join(r.home, ".config/systemd/user/kyeot-connect.service")
		if runtime.GOOS == "darwin" {
			f = filepath.Join(r.home, "Library/LaunchAgents/app.kyeot.connect.plist")
		}
		os.MkdirAll(filepath.Dir(f), 0o755)
		os.WriteFile(f, []byte("saved"), 0o644)
		os.MkdirAll(filepath.Dir(r.bin()), 0o755)
		os.WriteFile(r.bin(), []byte(fakeKyeot("0.1.5")), 0o755)
		code, out := r.run(nil)
		calls, _ := os.ReadFile(log)
		if code != 0 || !strings.Contains(string(calls), "restart kyeot-connect") && !strings.Contains(string(calls), "kickstart -k") {
			t.Fatalf("exit %d calls %q: %s", code, calls, out)
		}
		if b, _ := os.ReadFile(f); string(b) != "saved" {
			t.Error("the connect service file was rewritten")
		}
		os.Remove(log)
		r.run(nil) // same version: nothing replaced, nothing restarted
		if calls, _ := os.ReadFile(log); len(calls) != 0 {
			t.Errorf("restarted without an update: %q", calls)
		}
	})
}

// A failed connect restart makes the script fail.
func TestInstallConnectRestartFailureFails(t *testing.T) {
	shells(t, func(t *testing.T, r *rig) {
		for _, tool := range []string{"launchctl", "systemctl"} {
			os.WriteFile(filepath.Join(r.fakebin, tool), []byte("#!/bin/sh\nexit 1\n"), 0o755)
		}
		f := filepath.Join(r.home, ".config/systemd/user/kyeot-connect.service")
		if runtime.GOOS == "darwin" {
			f = filepath.Join(r.home, "Library/LaunchAgents/app.kyeot.connect.plist")
		}
		os.MkdirAll(filepath.Dir(f), 0o755)
		os.WriteFile(f, []byte("saved"), 0o644)
		os.MkdirAll(filepath.Dir(r.bin()), 0o755)
		os.WriteFile(r.bin(), []byte(fakeKyeot("0.1.5")), 0o755)
		if code, out := r.run(nil); code == 0 || !strings.Contains(out, "다시 켜지 못했다") {
			t.Errorf("exit %d: %s", code, out)
		}
	})
}
