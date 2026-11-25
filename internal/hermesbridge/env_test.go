package hermesbridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFromEnvReadsKeyFromHermesHome(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, ".env"), []byte("OTHER=1\nAPI_SERVER_KEY=\"from-file\"\n"), 0o600)
	t.Setenv("HERMES_HOME", home)
	t.Setenv("API_SERVER_KEY", "")
	t.Setenv("HERMES_URL", "")
	if cfg := FromEnv(); cfg.HermesKey != "from-file" || cfg.HermesURL != "http://127.0.0.1:8642" || cfg.UIAddr != "127.0.0.1:7701" {
		t.Fatalf("%+v", cfg)
	}
	t.Setenv("API_SERVER_KEY", "from-env")
	t.Setenv("BRIDGE_UI_ADDR", "")
	if cfg := FromEnv(); cfg.HermesKey != "from-env" || cfg.UIAddr != "" {
		t.Fatalf("env should win and empty BRIDGE_UI_ADDR turns the UI off: %+v", cfg)
	}
	t.Setenv("HERMES_HOME", t.TempDir())
	t.Setenv("API_SERVER_KEY", "")
	if cfg := FromEnv(); cfg.HermesKey != "" {
		t.Fatal("no .env means no key")
	}
}

func TestEnvFileValue(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(p, []byte("# API_SERVER_KEY=commented\nAPI_SERVER_KEY_OLD=old\nAPI_SERVER_KEY=first\nexport API_SERVER_KEY = 'last' \nB=x # note\nC=\"unbalanced\nD=\"abc\" # note\nexport\tE=tab\nexportF=glued\n"), 0o600)
	for key, want := range map[string]string{"API_SERVER_KEY": "last", "B": "x", "C": "\"unbalanced", "D": "abc", "E": "tab", "F": "", "NONE": ""} {
		if got := envFileValue(p, key); got != want {
			t.Errorf("%s: %q", key, got)
		}
	}
}
