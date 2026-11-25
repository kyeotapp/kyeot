package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeRun struct {
	calls [][]string
	fail  func(c []string) bool
}

func (f *fakeRun) run(name string, args ...string) error {
	c := append([]string{name}, args...)
	f.calls = append(f.calls, c)
	if f.fail != nil && f.fail(c) {
		return errors.New("fail: " + strings.Join(c, " "))
	}
	return nil
}

func fakeHost(t *testing.T, goos string) (host, *fakeRun) {
	f := &fakeRun{}
	home := t.TempDir()
	return host{goos: goos, exe: "/opt/kyeot/bin/kyeot", home: home, path: "/opt/homebrew/bin:/usr/bin", uid: 501, tmp: []string{"/nonexistent-tmp"},
		hermesHome: filepath.Join(home, ".hermes"), fileKey: true, run: f.run}, f
}

func mustContain(t *testing.T, s string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in:\n%s", w, s)
		}
	}
}

func TestLaunchdPlist(t *testing.T) {
	h, _ := fakeHost(t, "darwin")
	s := launchdService(h)
	if s.file != filepath.Join(h.home, "Library/LaunchAgents/app.kyeot.connect.plist") {
		t.Fatal(s.file)
	}
	logFile := filepath.Join(h.home, "Library/Logs/kyeot-connect.log")
	mustContain(t, s.content,
		"<string>app.kyeot.connect</string>",
		"<string>/opt/kyeot/bin/kyeot</string>\n\t\t<string>connect</string>\n\t</array>",
		"<key>PATH</key>\n\t\t<string>/opt/homebrew/bin:/usr/bin</string>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>",
		"<key>ThrottleInterval</key>",
		"<key>StandardOutPath</key>\n\t<string>"+logFile,
		"<key>StandardErrorPath</key>\n\t<string>"+logFile,
	)
}

// plistValues re-reads the plist as XML and extracts ProgramArguments and EnvironmentVariables.
func plistValues(t *testing.T, content string) (args []string, env [][2]string) {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(content))
	d.Strict = true
	var stack []string
	var lastKey, section, pendingKey, text string
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("plist is not valid XML: %v\n%s", err, content)
		}
		switch v := tok.(type) {
		case xml.StartElement:
			stack = append(stack, v.Name.Local)
			text = ""
			if len(stack) == 3 && (v.Name.Local == "array" || v.Name.Local == "dict") {
				section = lastKey
			}
		case xml.CharData:
			text += string(v)
		case xml.EndElement:
			switch tag := v.Name.Local; {
			case len(stack) == 3 && tag == "key":
				lastKey = text
			case len(stack) == 3:
				section = ""
			case len(stack) == 4 && section == "ProgramArguments" && tag == "string":
				args = append(args, text)
			case len(stack) == 4 && section == "EnvironmentVariables" && tag == "key":
				pendingKey = text
			case len(stack) == 4 && section == "EnvironmentVariables" && tag == "string":
				env = append(env, [2]string{pendingKey, text})
			}
			stack = stack[:len(stack)-1]
		}
	}
	return args, env
}

func TestLaunchdPlistEscapesSpecialPath(t *testing.T) {
	h, _ := fakeHost(t, "darwin")
	h.exe = `/opt/a b/"x"&<y>%z/kyeot`
	h.path = `/opt/<p>&q:/usr/bin`
	h.env = map[string]string{"HERMES_URL": "http://h/?a=1&b=<2>", "BRIDGE_UI_ADDR": ""}
	args, env := plistValues(t, launchdService(h).content)
	if len(args) != 2 || args[0] != h.exe || args[1] != "connect" {
		t.Fatalf("args %q", args)
	}
	want := [][2]string{{"PATH", h.path}, {"BRIDGE_UI_ADDR", ""}, {"HERMES_URL", "http://h/?a=1&b=<2>"}}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("env %q", env)
	}
}

func TestSystemdUnit(t *testing.T) {
	h, _ := fakeHost(t, "linux")
	s := systemdService(h)
	if s.file != filepath.Join(h.home, ".config/systemd/user/kyeot-connect.service") {
		t.Fatal(s.file)
	}
	mustContain(t, s.content,
		`ExecStart="/opt/kyeot/bin/kyeot" connect`+"\n",
		`Environment="PATH=/opt/homebrew/bin:/usr/bin"`,
		"Restart=on-failure", "RestartSec=", "WantedBy=default.target",
	)
	mustContain(t, s.note, "loginctl enable-linger")
}

func TestSystemdUnitEscapes(t *testing.T) {
	h, _ := fakeHost(t, "linux")
	h.exe = `/opt/a b/"x"$y%z\w/kyeot`
	h.path = `/opt/50%/bin:/usr/bin`
	h.env = map[string]string{"HERMES_HOME": `/h/"q"`}
	c := systemdService(h).content
	mustContain(t, c,
		`ExecStart="/opt/a b/\"x\"$$y%%z\\w/kyeot" connect`+"\n",
		`Environment="PATH=/opt/50%%/bin:/usr/bin"`+"\n",
		`Environment="HERMES_HOME=/h/\"q\""`+"\n",
	)
}

func TestServiceEnvSortedNoSecret(t *testing.T) {
	h, _ := fakeHost(t, "linux")
	h.env = map[string]string{"KYEOT_CONNECT_FILE": "/c.json", "HERMES_URL": "http://u", "HERMES_HOME": "/h", "BRIDGE_UI_ADDR": "127.0.0.1:1"}
	t.Setenv("API_SERVER_KEY", "fake-secret-value")
	unit := systemdService(h).content
	mustContain(t, unit, `Environment="PATH=/opt/homebrew/bin:/usr/bin"
Environment="BRIDGE_UI_ADDR=127.0.0.1:1"
Environment="HERMES_HOME=/h"
Environment="HERMES_URL=http://u"
Environment="KYEOT_CONNECT_FILE=/c.json"
`)
	h.goos = "darwin"
	plist := launchdService(h).content
	for _, c := range []string{unit, plist} {
		if strings.Contains(c, "API_SERVER_KEY") || strings.Contains(c, "fake-secret-value") {
			t.Fatalf("secret leaked:\n%s", c)
		}
	}
	_, env := plistValues(t, plist)
	if len(env) != 5 || env[1][0] != "BRIDGE_UI_ADDR" || env[4][0] != "KYEOT_CONNECT_FILE" {
		t.Fatalf("plist env %q", env)
	}
}

func TestStableExe(t *testing.T) {
	have := map[string]bool{"/opt/homebrew/opt/kyeot/bin/kyeot": true}
	exists := func(p string) bool { return have[p] }
	for in, want := range map[string]string{
		"/opt/homebrew/Cellar/kyeot/1.2.3/bin/kyeot":  "/opt/homebrew/opt/kyeot/bin/kyeot",
		"/opt/homebrew/Cellar/other/1.0/bin/kyeot":    "/opt/homebrew/Cellar/other/1.0/bin/kyeot",
		"/opt/homebrew/Cellar/kyeot/1.2.3":            "/opt/homebrew/Cellar/kyeot/1.2.3",
		"/usr/local/bin/kyeot":                        "/usr/local/bin/kyeot",
		"/home/linuxbrew/.linuxbrew/Cellar/kyeot/2/x": "/home/linuxbrew/.linuxbrew/Cellar/kyeot/2/x", // public-ok
	} {
		if got := stableExe(in, exists); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
	have["/home/linuxbrew/.linuxbrew/opt/kyeot/x"] = true                                                                         // public-ok
	if got := stableExe("/home/linuxbrew/.linuxbrew/Cellar/kyeot/2/x", exists); got != "/home/linuxbrew/.linuxbrew/opt/kyeot/x" { // public-ok
		t.Error(got)
	}
}

func TestCleanPath(t *testing.T) {
	if got := cleanPath("/usr/bin::.:bin:/opt/x/bin:"); got != "/usr/bin:/opt/x/bin" {
		t.Fatal(got)
	}
	if got := cleanPath(""); got != "" {
		t.Fatal(got)
	}
}

func TestPreflight(t *testing.T) {
	for name, tc := range map[string]struct {
		mod        func(h *host)
		foreground bool
		want       string // empty means pass
	}{
		"file key":                {func(h *host) {}, false, ""},
		"no key":                  {func(h *host) { h.fileKey = false }, false, "Hermes 를 찾지 못했다"},
		"no key foreground":       {func(h *host) { h.fileKey = false }, true, "Hermes 를 찾지 못했다"},
		"shell key only":          {func(h *host) { h.fileKey, h.shellKey = false, true }, false, "서비스는 셸 변수를 못 본다"},
		"shell key foreground":    {func(h *host) { h.fileKey, h.shellKey = false, true }, true, ""},
		"shell key temp exe":      {func(h *host) { h.fileKey, h.shellKey, h.exe = false, true, "/nonexistent-tmp/kyeot" }, false, ""},
		"shell key windows":       {func(h *host) { h.fileKey, h.shellKey, h.goos = false, true, "windows" }, false, ""},
		"root":                    {func(h *host) { h.uid = 0 }, false, "sudo"},
		"newline in exe":          {func(h *host) { h.exe = "/opt/a\nb/kyeot" }, false, "제어문자"},
		"control in path":         {func(h *host) { h.path = "/usr/bin:/x\x01" }, false, "PATH"},
		"newline in env":          {func(h *host) { h.env = map[string]string{"HERMES_URL": "http://x\n[Service]"} }, false, "HERMES_URL"},
		"foreground not register": {func(h *host) {}, true, ""},
	} {
		for _, goos := range []string{"darwin", "linux"} {
			t.Run(name+"/"+goos, func(t *testing.T) {
				h, f := fakeHost(t, goos)
				tc.mod(&h)
				err := preflight(h, tc.foreground)
				if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
					t.Fatalf("want %q got %v", tc.want, err)
				}
				if len(f.calls) != 0 {
					t.Fatalf("ran %v", f.calls)
				}
			})
		}
	}
}

func TestPreflightShellKeyNamesEnvFile(t *testing.T) {
	h, _ := fakeHost(t, "darwin")
	h.fileKey, h.shellKey = false, true
	err := preflight(h, false)
	if err == nil || !strings.Contains(err.Error(), h.hermesHome+"/.env") || !strings.Contains(err.Error(), "--foreground") {
		t.Fatal(err)
	}
}

func TestPreflightForegroundWhileRegistered(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		h, _ := fakeHost(t, goos)
		if _, err := startService(h, false, io.Discard); err != nil {
			t.Fatal(err)
		}
		if err := preflight(h, true); err == nil || !strings.Contains(err.Error(), "kyeot disconnect") {
			t.Fatalf("%s: %v", goos, err)
		}
		if err := preflight(h, false); err != nil {
			t.Fatalf("%s re-register: %v", goos, err)
		}
	}
}

func TestStartServiceRefusesRootAndControl(t *testing.T) {
	for name, mod := range map[string]func(h *host){
		"root":    func(h *host) { h.uid = 0 },
		"newline": func(h *host) { h.exe = "/opt/a\nExecStartPre=/bin/x\n/kyeot" },
		"env":     func(h *host) { h.env = map[string]string{"HERMES_HOME": "/h\r"} },
	} {
		t.Run(name, func(t *testing.T) {
			h, f := fakeHost(t, "linux")
			mod(&h)
			done, err := startService(h, false, io.Discard)
			if err == nil || done || len(f.calls) != 0 {
				t.Fatalf("done=%v err=%v calls=%v", done, err, f.calls)
			}
			if entries, _ := os.ReadDir(h.home); len(entries) != 0 {
				t.Fatalf("wrote files: %v", entries)
			}
		})
	}
}

func TestStartServiceMacRegistersThenReregisters(t *testing.T) {
	h, f := fakeHost(t, "darwin")
	plist := filepath.Join(h.home, "Library/LaunchAgents/app.kyeot.connect.plist")
	stops := [][]string{{"launchctl", "bootout", "gui/501/app.kyeot.connect"}, {"launchctl", "bootout", "user/501/app.kyeot.connect"}}
	f.fail = func(c []string) bool { return c[1] == "bootout" } // not registered yet, so there is nothing to stop
	done, err := startService(h, false, io.Discard)
	if err != nil || !done {
		t.Fatal(done, err)
	}
	if _, err := os.Stat(plist); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h.home, "Library/Logs")); err != nil {
		t.Fatal("log dir:", err)
	}
	want := append(append([][]string{}, stops...), []string{"launchctl", "bootstrap", "gui/501", plist})
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("first: %v", f.calls)
	}
	f.calls = nil
	if _, err := startService(h, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("again: %v", f.calls)
	}
}

func TestStartServiceLinux(t *testing.T) {
	h, f := fakeHost(t, "linux")
	if done, err := startService(h, false, io.Discard); err != nil || !done {
		t.Fatal(done, err)
	}
	want := [][]string{{"systemctl", "--user", "disable", "--now", "kyeot-connect.service"}, {"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", "--now", "kyeot-connect.service"}}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("%v", f.calls)
	}
}

func TestStartServiceSkips(t *testing.T) {
	for name, tc := range map[string]struct {
		goos, exe  string
		foreground bool
	}{
		"foreground": {"darwin", "/opt/kyeot/bin/kyeot", true},
		"temp dir":   {"darwin", "/nonexistent-tmp/x/kyeot", false},
		"go run":     {"linux", "/var/folders/ab/go-build123/b001/exe/kyeot", false},
		"windows":    {"windows", `C:\kyeot\kyeot.exe`, false},
	} {
		t.Run(name, func(t *testing.T) {
			h, f := fakeHost(t, tc.goos)
			h.exe = tc.exe
			done, err := startService(h, tc.foreground, io.Discard)
			if err != nil || done || len(f.calls) != 0 {
				t.Fatalf("done=%v err=%v calls=%v", done, err, f.calls)
			}
			if entries, _ := os.ReadDir(h.home); len(entries) != 0 {
				t.Fatalf("wrote files: %v", entries)
			}
		})
	}
}

func TestStartServiceMacFallsBackToUserDomain(t *testing.T) {
	h, f := fakeHost(t, "darwin")
	plist := filepath.Join(h.home, "Library/LaunchAgents/app.kyeot.connect.plist")
	f.fail = func(c []string) bool { return c[1] == "bootout" || c[1] == "bootstrap" && c[2] == "gui/501" }
	var out bytes.Buffer
	done, err := startService(h, false, &out)
	if err != nil || !done {
		t.Fatal(done, err)
	}
	gui := []string{"launchctl", "bootstrap", "gui/501", plist}
	want := [][]string{f.calls[0], f.calls[1], gui, gui, gui, {"launchctl", "bootstrap", "user/501", plist}}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("%v", f.calls)
	}
	mustContain(t, out.String(), "사용자 도메인")
	if strings.Contains(out.String(), "재부팅해도 계속") {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(plist); err != nil {
		t.Fatal(err)
	}
}

func TestStartServiceMacBothDomainsFailRemovesPlist(t *testing.T) {
	h, f := fakeHost(t, "darwin")
	plist := filepath.Join(h.home, "Library/LaunchAgents/app.kyeot.connect.plist")
	f.fail = func(c []string) bool { return true }
	done, err := startService(h, false, io.Discard)
	if err == nil || done {
		t.Fatal(done, err)
	}
	mustContain(t, err.Error(), "gui/501", "user/501")
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Fatal("plist left behind:", err)
	}
}

func TestStartServiceRetriesBootstrap(t *testing.T) {
	h, f := fakeHost(t, "darwin")
	tries := 0
	f.fail = func(c []string) bool {
		if c[1] != "bootstrap" {
			return false
		}
		tries++
		return tries < 3
	}
	if done, err := startService(h, false, io.Discard); err != nil || !done || tries != 3 {
		t.Fatalf("done=%v err=%v tries=%d", done, err, tries)
	}
}

func TestDisconnect(t *testing.T) {
	h, f := fakeHost(t, "darwin")
	conn := filepath.Join(h.home, "connect.json")
	t.Setenv("KYEOT_CONNECT_FILE", conn)
	if err := savePrivate(conn, connection{"http://x", "t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := startService(h, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	f.fail = func(c []string) bool { return strings.HasPrefix(c[2], "user/") }
	var out bytes.Buffer
	if err := runDisconnect(h, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.calls, [][]string{{"launchctl", "bootout", "gui/501/app.kyeot.connect"}, {"launchctl", "bootout", "user/501/app.kyeot.connect"}}) {
		t.Fatalf("%v", f.calls)
	}
	mustContain(t, out.String(), "서비스를 멈추고 지웠다", "앱에서 이 에이전트를 지운다")
	for _, p := range []string{conn, filepath.Join(h.home, "Library/LaunchAgents/app.kyeot.connect.plist")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still there: %v", p, err)
		}
	}
	f.calls = nil
	if err := runDisconnect(h, io.Discard); err != nil || len(f.calls) != 0 {
		t.Fatalf("second disconnect: %v %v", err, f.calls)
	}
}

func TestDisconnectReportsStopFailure(t *testing.T) {
	h, f := fakeHost(t, "darwin")
	t.Setenv("KYEOT_CONNECT_FILE", filepath.Join(h.home, "none.json"))
	if _, err := startService(h, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	f.fail = func(c []string) bool { return true }
	var out bytes.Buffer
	if err := runDisconnect(h, &out); err != nil {
		t.Fatal(err)
	}
	mustContain(t, out.String(), "서비스를 멈추지 못했다: fail: launchctl bootout gui/501", "로그아웃하거나 재부팅하면 멈춘다", "앱에서 이 에이전트를 지운다")
	if strings.Contains(out.String(), "멈추고 지웠다") {
		t.Fatal(out.String())
	}
}

func TestDisconnectLinuxReloads(t *testing.T) {
	h, f := fakeHost(t, "linux")
	t.Setenv("KYEOT_CONNECT_FILE", filepath.Join(h.home, "none.json"))
	if _, err := startService(h, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(h.home, ".config/systemd/user/kyeot-connect.service")
	f.calls = nil
	f.fail = func(c []string) bool {
		if c[2] == "daemon-reload" { // the unit file must be deleted first
			if _, err := os.Stat(unit); err == nil {
				t.Error("daemon-reload before unit removed")
			}
		}
		return false
	}
	if err := runDisconnect(h, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"systemctl", "--user", "disable", "--now", "kyeot-connect.service"}, {"systemctl", "--user", "daemon-reload"}}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("%v", f.calls)
	}
}
