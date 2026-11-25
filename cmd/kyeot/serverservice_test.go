package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func serverFakeHost(t *testing.T, goos string) (host, *fakeRun) {
	h, f := fakeHost(t, goos)
	h.spec = serverSpec
	h.env = map[string]string{"KYEOT_ADDR": "0.0.0.0:7700"}
	h.ensureToken = func() (bool, error) { return true, nil }
	return h, f
}

func TestServerLaunchdPlist(t *testing.T) {
	h, _ := serverFakeHost(t, "darwin")
	s := launchdService(h)
	if !strings.HasSuffix(s.file, "/Library/LaunchAgents/app.kyeot.server.plist") {
		t.Errorf("file: %s", s.file)
	}
	args, env := plistValues(t, s.content)
	if len(args) != 1 || args[0] != h.exe {
		t.Errorf("the server runs with only the executable and no arguments: %v", args)
	}
	mustContain(t, s.content, "<key>KeepAlive</key>\n\t<true/>", "kyeot-server.log")
	if strings.Contains(s.content, "SuccessfulExit") {
		t.Error("the server restarts even after a clean exit")
	}
	found := false
	for _, p := range env {
		found = found || (p[0] == "KYEOT_ADDR" && p[1] == "0.0.0.0:7700")
	}
	if !found {
		t.Errorf("env: %v", env)
	}
}

func TestServerSystemdUnit(t *testing.T) {
	h, _ := serverFakeHost(t, "linux")
	s := systemdService(h)
	if !strings.HasSuffix(s.file, "/.config/systemd/user/kyeot-server.service") {
		t.Errorf("file: %s", s.file)
	}
	mustContain(t, s.content, "Description=kyeot server", "ExecStart=\"/opt/kyeot/bin/kyeot\"\n", "Restart=always", `Environment="KYEOT_ADDR=0.0.0.0:7700"`)
	if strings.Contains(s.content, " connect") {
		t.Error("the server unit contains connect")
	}
	if s.log != "journalctl --user -u kyeot-server" {
		t.Errorf("log: %s", s.log)
	}
}

func TestServiceInstallUninstall(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			h, f := serverFakeHost(t, goos)
			var out bytes.Buffer
			if err := runService(h, []string{"status"}, &out); err != nil || !strings.Contains(out.String(), "등록돼 있지 않다") {
				t.Fatalf("status before: %v %s", err, out.String())
			}
			out.Reset()
			if err := runService(h, []string{"install"}, &out); err != nil {
				t.Fatal(err, out.String())
			}
			mustContain(t, out.String(), "소유자 키를 만들었다", "서버를 켰다", "kyeot service uninstall", "kyeot pair")
			if !registered(h) {
				t.Fatal("the registration file is missing")
			}
			started := false
			for _, c := range f.calls {
				started = started || strings.Contains(strings.Join(c, " "), "bootstrap") || strings.Contains(strings.Join(c, " "), "enable --now kyeot-server.service")
			}
			if !started {
				t.Errorf("no start command: %v", f.calls)
			}
			out.Reset()
			if err := runService(h, []string{"status"}, &out); err != nil || !strings.Contains(out.String(), "등록돼 있다") {
				t.Fatalf("status after: %v %s", err, out.String())
			}
			out.Reset()
			if err := runService(h, []string{"uninstall"}, &out); err != nil {
				t.Fatal(err)
			}
			mustContain(t, out.String(), "멈추고 지웠다")
			s, _ := serviceFor(h)
			if _, err := os.Stat(s.file); err == nil {
				t.Error("the registration file remains")
			}
			out.Reset()
			if err := runService(h, []string{"uninstall"}, &out); err != nil || !strings.Contains(out.String(), "등록된 서버 서비스가 없다") {
				t.Errorf("second uninstall: %v %s", err, out.String())
			}
		})
	}
}

func TestServiceInstallRefuses(t *testing.T) {
	h, _ := serverFakeHost(t, "linux")
	h.exe = "/tmp/go-build123/kyeot"
	if err := runService(h, []string{"install"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "임시 폴더") {
		t.Errorf("temp executable: %v", err)
	}
	h, _ = serverFakeHost(t, "linux")
	h.uid = 0
	if err := runService(h, []string{"install"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "root") {
		t.Errorf("root: %v", err)
	}
	h, _ = serverFakeHost(t, "linux")
	for _, args := range [][]string{{}, {"nope"}, {"install", "x"}} {
		if err := runService(h, args, &bytes.Buffer{}); err == nil {
			t.Errorf("%v must be rejected", args)
		}
	}
}

func TestServerHostEnvSkipsToken(t *testing.T) {
	t.Setenv("KYEOT_OWNER_TOKEN", "secret-secret-secret")
	t.Setenv("KYEOT_ADDR", "127.0.0.1:7710")
	t.Setenv("KYEOT_NETS", "loopback")
	h := realHostFor(serverSpec)
	if _, ok := h.env["KYEOT_OWNER_TOKEN"]; ok {
		t.Error("the owner token must not go in the service file")
	}
	if h.env["KYEOT_ADDR"] != "127.0.0.1:7710" || h.env["KYEOT_NETS"] != "loopback" {
		t.Errorf("env: %v", h.env)
	}
	if h.sp().label != "app.kyeot.server" {
		t.Errorf("label: %s", h.sp().label)
	}
}

func TestServiceRestart(t *testing.T) {
	h, f := serverFakeHost(t, "darwin")
	var out bytes.Buffer
	if err := runService(h, []string{"restart"}, &out); err != nil || !strings.Contains(out.String(), "등록된 서버 서비스가 없다") {
		t.Fatalf("restart when unregistered: %v %s", err, out.String())
	}
	if len(f.calls) != 0 {
		t.Errorf("when unregistered, no command runs: %v", f.calls)
	}
	if err := runService(h, []string{"install"}, &out); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	out.Reset()
	if err := runService(h, []string{"restart"}, &out); err != nil {
		t.Fatal(err, out.String())
	}
	joined := ""
	for _, c := range f.calls {
		joined += strings.Join(c, " ") + "\n"
	}
	if !strings.Contains(joined, "bootout") || !strings.Contains(joined, "bootstrap") {
		t.Errorf("it must stop then start: %s", joined)
	}
	if !registered(h) {
		t.Error("it must still be registered after restart")
	}
}

func TestServiceRestartFailureKeepsRegistration(t *testing.T) {
	h, f := serverFakeHost(t, "darwin")
	var out bytes.Buffer
	if err := runService(h, []string{"install"}, &out); err != nil {
		t.Fatal(err)
	}
	f.fail = func(c []string) bool { return len(c) > 1 && c[1] == "bootstrap" } // a failure like launchctl 141 Reentrancy avoided
	err := runService(h, []string{"restart"}, &out)
	if err == nil || !strings.Contains(err.Error(), "kyeot service restart") {
		t.Fatalf("it must give the failure reason and the restart command: %v", err)
	}
	if !registered(h) {
		t.Error("even if restart fails, the registration file must stay so it starts at the next login")
	}
}

func TestServiceInstallFailureRemovesNewFile(t *testing.T) {
	h, f := serverFakeHost(t, "darwin")
	f.fail = func(c []string) bool { return len(c) > 1 && c[1] == "bootstrap" }
	if err := runService(h, []string{"install"}, &bytes.Buffer{}); err == nil {
		t.Fatal("a start failure must be an error")
	}
	if registered(h) {
		t.Error("if the first registration fails, the newly written file is deleted")
	}
}

func TestServiceStatusShowsProxyMark(t *testing.T) {
	h, _ := serverFakeHost(t, "darwin")
	var out bytes.Buffer
	if err := runService(h, []string{"status"}, &out); err != nil || strings.Contains(out.String(), "프록시") {
		t.Fatalf("without the marker, quiet: %v %q", err, out.String())
	}
	h.proxyMark = func() string { return "2026-11-18T10:00:00+09:00 X-Forwarded-For" }
	out.Reset()
	if err := runService(h, []string{"status"}, &out); err != nil || !strings.Contains(out.String(), "X-Forwarded-For") || !strings.Contains(out.String(), "지원하지 않는다") {
		t.Fatalf("status: %v %q", err, out.String())
	}
}
