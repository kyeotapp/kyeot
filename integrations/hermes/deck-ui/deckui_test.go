package deckui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sha256 of SKILL.md per version, without the version line. When the content changes, bump version and add a line here.
var bodyHashes = map[string]string{
	"1.2.0": "4bb4b21e4b9577ecc7ed5cff7a69327fc95936c79c9175b60def947c8fcd63b8",
	"1.2.1": "1f79ecfdfb2b682ed1ac7e487bbb1127d6d8386e78a5fb237c200ee5aae3180a",
	"1.2.2": "9e2d841ca1128e92827ca276e14dccd9eece9d0d602efe9cae1ad5a0c9b66623",
	"1.2.3": "b1c9382f3d94ed61784c257eae5a273cc87bc7a80f8071cdcb4c6c4fd55eb282",
	"1.2.4": "241ebe35e9db6d42a88a70457b7d64b4c6e1d0dc5ed530fd9cfdab0bffa3241e",
}

func TestVersionMovesWithContent(t *testing.T) {
	v := Version(Skill)
	sum := sha256.Sum256(Body(Skill))
	got := hex.EncodeToString(sum[:])
	want, ok := bodyHashes[v]
	if !ok {
		t.Fatalf("version %s is not in bodyHashes. Add a line: %q: %q", v, v, got)
	}
	if want != got {
		t.Fatalf("SKILL.md content differs from version %s. Bump version and add the new version to bodyHashes (current hash %s)", v, got)
	}
	for old := range bodyHashes {
		if Compare(old, v) > 0 {
			t.Fatalf("SKILL.md version %s is lower than the already used version %s", v, old)
		}
	}
}

func TestVersionOnlyInFrontMatter(t *testing.T) {
	skill := []byte("---\nname: x\nversion: 1.0.0\n---\n\nversion: 9.9.9\n")
	if v := Version(skill); v != "1.0.0" {
		t.Fatalf("version %q", v)
	}
	if !bytes.Contains(Body(skill), []byte("version: 9.9.9")) || bytes.Contains(Body(skill), []byte("1.0.0")) {
		t.Fatalf("body %q", Body(skill))
	}
}

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.1.0", "1.2.0", -1}, {"1.10.0", "1.9.0", 1}, {"1.2", "1.2.0", 0}, {"", "1.0.0", -1}, {"2.0.0", "1.9.9", 1},
	} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func withVersion(v string) []byte {
	return versionLine.ReplaceAll(Skill, []byte("version: "+v))
}

func backups(t *testing.T, home string) []string {
	t.Helper()
	m, _ := filepath.Glob(Path(home) + ".bak-*")
	return m
}

func TestInstall(t *testing.T) {
	t.Run("no Hermes", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), ".hermes")
		if got, err := Install(home); err != nil || got != Skipped {
			t.Fatalf("got %q %v", got, err)
		}
		if _, err := os.Stat(home); err == nil {
			t.Fatal("created a Hermes folder")
		}
		if got, _ := Install(""); got != Skipped {
			t.Fatalf("empty path %q", got)
		}
	})
	t.Run("no skill", func(t *testing.T) {
		home := t.TempDir()
		if got, err := Install(home); err != nil || got != Installed {
			t.Fatalf("got %q %v", got, err)
		}
		if b, _ := os.ReadFile(Path(home)); !bytes.Equal(b, Skill) {
			t.Fatal("content differs")
		}
		if got, _ := Install(home); got != Current {
			t.Fatalf("on reinstall %q", got)
		}
		if len(backups(t, home)) != 0 {
			t.Fatal("left a backup though content was the same")
		}
	})
	t.Run("outdated", func(t *testing.T) {
		home := t.TempDir()
		old := append(withVersion("1.1.0"), "\nold line\n"...)
		os.MkdirAll(filepath.Dir(Path(home)), 0o755)
		os.WriteFile(Path(home), old, 0o644)
		if got, err := Install(home); err != nil || got != Updated {
			t.Fatalf("got %q %v", got, err)
		}
		if b, _ := os.ReadFile(Path(home)); !bytes.Equal(b, Skill) {
			t.Fatal("was not replaced by the new version")
		}
		bk := backups(t, home)
		if len(bk) != 1 {
			t.Fatalf("backup %v", bk)
		}
		if b, _ := os.ReadFile(bk[0]); !bytes.Equal(b, old) {
			t.Fatal("backup is not the old content")
		}
		if !strings.HasPrefix(filepath.Base(bk[0]), "SKILL.md.bak-") {
			t.Fatal("backup name is SKILL.md — Hermes would read it as a skill")
		}
	})
	t.Run("linked copy", func(t *testing.T) {
		home := t.TempDir()
		src := filepath.Join(t.TempDir(), "SKILL.md")
		os.WriteFile(src, withVersion("1.0.0"), 0o644)
		os.MkdirAll(filepath.Dir(Path(home)), 0o755)
		os.Symlink(src, Path(home))
		if got, err := Install(home); err != nil || got != Kept {
			t.Fatalf("got %q %v", got, err)
		}
		if fi, _ := os.Lstat(Path(home)); fi.Mode()&os.ModeSymlink == 0 {
			t.Fatal("broke the link")
		}
	})
	t.Run("hand-edited same version, newer version", func(t *testing.T) {
		for _, v := range []string{Version(Skill), "99.0.0"} {
			home := t.TempDir()
			mine := append(withVersion(v), "\nline added by user\n"...)
			os.MkdirAll(filepath.Dir(Path(home)), 0o755)
			os.WriteFile(Path(home), mine, 0o644)
			if got, err := Install(home); err != nil || got != Kept {
				t.Fatalf("%s: got %q %v", v, got, err)
			}
			if b, _ := os.ReadFile(Path(home)); !bytes.Equal(b, mine) {
				t.Fatalf("%s: overwrote", v)
			}
		}
	})
}
