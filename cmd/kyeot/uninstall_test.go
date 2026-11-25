package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	deckui "github.com/kyeotapp/kyeot/integrations/hermes/deck-ui"
)

// Builds the installed layout in a temp HOME: two service registrations, connection info, executable, keys and history, logs.
func installedPlan(t *testing.T) (uninstallPlan, *fakeRun) {
	t.Helper()
	server, f := serverFakeHost(t, "darwin")
	server.exe = filepath.Join(server.home, ".local", "bin", "kyeot")
	agent := server
	agent.spec = connectSpec
	data := filepath.Join(server.home, "Library", "Application Support", "kyeot")
	logf := filepath.Join(server.home, "Library", "Logs", "kyeot-server.log")
	p := uninstallPlan{server: server, agent: agent, connectFile: filepath.Join(data, "connect.json"), dataDirs: []string{data}, logFiles: []string{logf}}
	for path, body := range map[string]string{
		launchdService(server).file:        "plist",
		launchdService(agent).file:         "plist",
		server.exe:                         "bin",
		server.exe + ".prev":               "old bin",
		filepath.Join(data, "owner-token"): "tok",
		filepath.Join(data, "kyeot.db"):    "db",
		p.connectFile:                      "{}",
		logf:                               "log",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return p, f
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func TestUninstallKeepsDataByDefault(t *testing.T) {
	p, _ := installedPlan(t)
	var out bytes.Buffer
	if err := runUninstall(p, nil, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{launchdService(p.server).file, launchdService(p.agent).file, p.server.exe, p.server.exe + ".prev", p.connectFile} {
		if exists(gone) {
			t.Errorf("remained: %s", gone)
		}
	}
	for _, kept := range []string{filepath.Join(p.dataDirs[0], "owner-token"), filepath.Join(p.dataDirs[0], "kyeot.db"), p.logFiles[0]} {
		if !exists(kept) {
			t.Errorf("a plain uninstall deleted the records: %s", kept)
		}
	}
	mustContain(t, out.String(), "지웠다:", "남겼다:", "kyeot uninstall --purge")
}

func TestUninstallPurgeAsksFirst(t *testing.T) {
	p, _ := installedPlan(t)
	var out bytes.Buffer
	if err := runUninstall(p, []string{"--purge"}, strings.NewReader("no\n"), &out); err != nil {
		t.Fatal(err)
	}
	if !exists(p.server.exe) || !exists(p.dataDirs[0]) {
		t.Fatal("deleted although the answer was not yes")
	}
	mustContain(t, out.String(), "그만뒀다")

	out.Reset()
	if err := runUninstall(p, []string{"--purge"}, strings.NewReader("yes\n"), &out); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{p.server.exe, p.dataDirs[0], p.logFiles[0]} {
		if exists(gone) {
			t.Errorf("remained after --purge: %s", gone)
		}
	}
}

func TestUninstallLeavesForeignBinary(t *testing.T) {
	p, _ := installedPlan(t)
	other := filepath.Join(t.TempDir(), "kyeot")
	os.WriteFile(other, []byte("bin"), 0o755)
	p.server.exe = other
	var out bytes.Buffer
	if err := runUninstall(p, []string{"--purge", "--yes"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if !exists(other) {
		t.Fatal("deleted an executable the install script did not lay down")
	}
	mustContain(t, out.String(), "두었다")
}

func TestUninstallNothing(t *testing.T) {
	h, _ := serverFakeHost(t, "darwin")
	var out bytes.Buffer
	if err := runUninstall(uninstallPlan{server: h, agent: h}, nil, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	mustContain(t, out.String(), "지울 것이 없다")
}

func TestUninstallRemovesOurSkillsOnly(t *testing.T) {
	p, _ := installedPlan(t)
	def, prof, edited := t.TempDir(), t.TempDir(), t.TempDir()
	p.hermesHomes = []string{def, prof, edited, filepath.Join(t.TempDir(), "missing")}
	deckui.Install(def)
	deckui.InstallKey(prof, "profkey-0123456789abcd")
	deckui.Install(edited)
	os.WriteFile(deckui.Path(edited), append(append([]byte{}, deckui.Skill...), "\nedited line\n"...), 0o644)
	bak := deckui.Path(def) + ".bak-20250101-000000"
	os.WriteFile(bak, []byte("old"), 0o644)

	var out bytes.Buffer
	if err := runUninstall(p, nil, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{deckui.Path(def), deckui.Path(prof)} {
		if exists(gone) {
			t.Errorf("our skill remained: %s", gone)
		}
	}
	if !exists(deckui.Path(edited)) || !exists(bak) {
		t.Error("deleted an edited copy or .bak")
	}
	mustContain(t, out.String(), "화면 스킬 "+deckui.Path(prof), "손본 사본이거나 링크라 두었다")
}
