package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The example files the skill points to must also exist on the user's computer. Examples follow even if SKILL.md is already identical.
func TestInstallHermesSkillCopiesExamples(t *testing.T) {
	home := t.TempDir()
	run := func() {
		t.Helper()
		cmd := exec.Command("sh", "install-hermes-skill.sh")
		cmd.Env = append(os.Environ(), "HERMES_HOME="+home)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	run()
	ex := filepath.Join(home, "skills", "productivity", "deck-ui", "examples", "netwatch")
	os.RemoveAll(ex) // reinstalling after only the examples vanished must bring them back
	run()
	for _, f := range []string{"SKILL.md", "examples/netwatch/netwatch.sh", "examples/netwatch/netwatch_deck.py", "examples/netwatch/README.md"} {
		if _, err := os.Stat(filepath.Join(home, "skills", "productivity", "deck-ui", f)); err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
	}
}
