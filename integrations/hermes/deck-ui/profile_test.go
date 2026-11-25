package deckui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestForPutsKeyInEveryAddress(t *testing.T) {
	got := string(For("abcDEF_123-x"))
	if strings.Count(string(Skill), "127.0.0.1:7701") == 0 {
		t.Fatal("skill has no on-Mac address")
	}
	if strings.Count(got, "127.0.0.1:7701") != strings.Count(got, "127.0.0.1:7701/h/abcDEF_123-x") {
		t.Fatal("address without key remains")
	}
	if !bytes.Equal(For(""), Skill) || !bytes.Equal(unkeyed([]byte(got)), Skill) {
		t.Fatal("skill without key must equal the original")
	}
}

func TestInstallKeyReplacesOurCopyWithOldKey(t *testing.T) {
	home := t.TempDir()
	if res, err := InstallKey(home, "oldkey"); err != nil || res != Installed {
		t.Fatalf("%s %v", res, err)
	}
	if res, _ := InstallKey(home, "oldkey"); res != Current {
		t.Fatalf("same key %s", res)
	}
	if res, err := InstallKey(home, "newkey"); err != nil || res != Updated {
		t.Fatalf("our copy differing only by key must be replaced: %s %v", res, err)
	}
	got, _ := os.ReadFile(Path(home))
	if !bytes.Contains(got, []byte("7701/h/newkey")) || bytes.Contains(got, []byte("oldkey")) {
		t.Fatal("was not changed to the new key")
	}
	edited := append(append([]byte{}, got...), "\nedited line\n"...)
	os.WriteFile(Path(home), edited, 0o644)
	if res, _ := InstallKey(home, "newkey"); res != Kept {
		t.Fatalf("hand-edited same version must be left: %s", res)
	}
}

func TestRemove(t *testing.T) {
	old := []byte(strings.Replace(string(Skill), "version: "+Version(Skill), "version: 0.0.1", 1))
	newer := []byte(strings.Replace(string(Skill), "version: "+Version(Skill), "version: 99.0.0", 1))
	edited := append(append([]byte{}, Skill...), "\nedited line\n"...)
	cases := []struct {
		name    string
		content []byte
		link    bool
		want    string
		gone    bool
	}{
		{"current version", Skill, false, Removed, true},
		{"profile key", For("k1"), false, Removed, true},
		{"old version", old, false, MovedOld, true},
		{"hand-edited same version", edited, false, Kept, false},
		{"newer version", newer, false, Kept, false},
		{"link", Skill, true, Kept, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			dest := Path(home)
			os.MkdirAll(filepath.Dir(dest), 0o755)
			bak := dest + ".bak-20250101-000000"
			os.WriteFile(bak, []byte("old backup"), 0o644)
			if c.link {
				src := filepath.Join(home, "src.md")
				os.WriteFile(src, c.content, 0o644)
				os.Symlink(src, dest)
			} else {
				os.WriteFile(dest, c.content, 0o644)
			}
			res, err := Remove(home)
			if err != nil || res != c.want {
				t.Fatalf("%s %v, want %s", res, err, c.want)
			}
			_, err = os.Lstat(dest)
			if gone := os.IsNotExist(err); gone != c.gone {
				t.Fatalf("removed? %v, want %v", gone, c.gone)
			}
			if b, _ := os.ReadFile(bak); string(b) != "old backup" {
				t.Fatal("touched .bak")
			}
			if c.want == MovedOld {
				m, _ := filepath.Glob(dest + ".bak-*")
				if len(m) != 2 {
					t.Fatalf("old version was not moved to .bak: %v", m)
				}
			}
		})
	}
	if res, err := Remove(t.TempDir()); err != nil || res != Skipped {
		t.Fatalf("Skipped expected when absent: %s %v", res, err)
	}
}

func TestRemoveKeepsSkillBehindLinkedFolder(t *testing.T) {
	home, real := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(home, "skills"), 0o755)
	os.Symlink(real, filepath.Join(home, "skills", "productivity"))
	if _, err := Install(home); err != nil {
		t.Fatal(err)
	}
	if res, err := Remove(home); err != nil || res != Kept {
		t.Fatalf("skill under linked folder: %s %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(real, "deck-ui", "SKILL.md")); err != nil {
		t.Fatal("deleted the skill on the link target side")
	}
}
