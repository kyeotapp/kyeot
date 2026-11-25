package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	deckui "github.com/kyeotapp/kyeot/integrations/hermes/deck-ui"
	"github.com/kyeotapp/kyeot/internal/hermesbridge"
)

func TestUIKeyIsStableAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hermes-agent.work.ui-key")
	k1, err := uiKey(path)
	if err != nil || !hermesbridge.UIKeyPattern.MatchString(k1) {
		t.Fatalf("%q %v", k1, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	if k2, _ := uiKey(path); k2 != k1 {
		t.Fatal("reading again must give the same key")
	}
	other, _ := uiKey(filepath.Join(filepath.Dir(path), "hermes-agent.home.ui-key"))
	if other == k1 {
		t.Fatal("each profile must have a different key")
	}
}

func TestProfileGetsUIAndOwnSkill(t *testing.T) {
	router := hermesbridge.NewUIRouter("127.0.0.1:0")
	home := t.TempDir()
	p := hermesbridge.Profile{ID: "work", Name: "Work", Config: hermesbridge.Config{HermesHome: home, UIAddr: "127.0.0.1:7701"}}
	l := localProfile(p, "127.0.0.1:7700", filepath.Join(t.TempDir(), "hermes-agent"), "Hermes", router)
	if l.NoUI {
		t.Fatal("a non-default profile must also use the screen address")
	}
	if off := localProfile(p, "127.0.0.1:7700", "x", "Hermes", nil); !off.NoUI {
		t.Fatal("without a router, screen upload is off")
	}

	cfg := p.Config
	cfg.UIRouter, cfg.UIKey = router, "workkey-0123456789abcd"
	installSkill(cfg)
	got, err := os.ReadFile(deckui.Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "127.0.0.1:7701/h/workkey-0123456789abcd") || strings.Contains(strings.ReplaceAll(string(got), "127.0.0.1:7701/h/", ""), "127.0.0.1:7701") {
		t.Fatal("in a profile skill, every address must be its own key address")
	}
}

func TestProfileUIKeyFailureTurnsScreenOff(t *testing.T) {
	router := hermesbridge.NewUIRouter("127.0.0.1:0")
	cfg := hermesbridge.Config{UIAddr: "127.0.0.1:7701", UIRouter: router}
	blocked := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocked, nil, 0o600)
	got := profileUI(cfg, "work", filepath.Join(blocked, "sub", "hermes-agent.work.ui-key")) // under a file, so it cannot be created
	if got.UIRouter != nil || got.UIAddr != "" || got.UIKey != "" || !got.UIProfile {
		t.Fatalf("if the key cannot be made, screen upload must be off: %+v", got)
	}
	ok := profileUI(cfg, "work", filepath.Join(t.TempDir(), "k"))
	if ok.UIRouter == nil || !hermesbridge.UIKeyPattern.MatchString(ok.UIKey) || !ok.UIProfile {
		t.Fatalf("success path: %+v", ok)
	}
	if d := profileUI(cfg, "default", filepath.Join(t.TempDir(), "k")); d.UIKey != "" || d.UIProfile {
		t.Fatalf("default has no key: %+v", d)
	}
}
