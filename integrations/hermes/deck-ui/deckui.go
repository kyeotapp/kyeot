// Package deckui embeds the Hermes screen skill (SKILL.md) in the binary and installs it into the Hermes folder.
package deckui

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//go:embed SKILL.md
var Skill []byte

var versionLine = regexp.MustCompile(`(?m)^version:\s*(\S+)\s*$`)

// frontMatter is the end offset of the first --- block, or 0 if absent.
func frontMatter(skill []byte) int {
	if !bytes.HasPrefix(skill, []byte("---\n")) {
		return 0
	}
	end := bytes.Index(skill[4:], []byte("\n---"))
	if end < 0 {
		return 0
	}
	return 4 + end
}

// Version is the version value in the SKILL.md front matter, or "" if absent.
func Version(skill []byte) string {
	m := versionLine.FindSubmatch(skill[:frontMatter(skill)])
	if m == nil {
		return ""
	}
	return string(m[1])
}

// Body is the content without the front matter's version line; used to check that version and content move together.
func Body(skill []byte) []byte {
	n := frontMatter(skill)
	return append(versionLine.ReplaceAll(skill[:n:n], nil), skill[n:]...)
}

// Install results
const (
	Skipped   = "skipped"   // no Hermes folder
	Installed = "installed" // not present, installed
	Updated   = "updated"   // replaced; old version kept as .bak-<time>
	Current   = "current"   // same content
	Kept      = "kept"      // same or newer version, left alone (may be a hand-edited copy)
)

// Path is the skill file location under hermesHome.
func Path(hermesHome string) string {
	return filepath.Join(hermesHome, "skills", "productivity", "deck-ui", "SKILL.md")
}

// defaultAddr is the on-Mac address the skill body calls; non-default profiles get /h/<key> appended.
const defaultAddr = "127.0.0.1:7701"

var profileAddr = regexp.MustCompile(`127\.0\.0\.1:7701/h/[A-Za-z0-9_-]+`)

// For is the skill for a profile that takes a key; an empty key returns Skill unchanged.
func For(key string) []byte {
	if key == "" {
		return Skill
	}
	return bytes.ReplaceAll(Skill, []byte(defaultAddr), []byte(defaultAddr+"/h/"+key))
}

// unkeyed is the body with the profile key removed; used to recognize our copy that differs only by key.
func unkeyed(skill []byte) []byte {
	return profileAddr.ReplaceAll(skill, []byte(defaultAddr))
}

// Install installs the skill into hermesHome, or replaces it if the installed version is old.
func Install(hermesHome string) (string, error) {
	return InstallKey(hermesHome, "")
}

// InstallKey is like Install but installs the skill with the profile key; our copy differing only by key is replaced directly.
func InstallKey(hermesHome, key string) (string, error) {
	skill := For(key)
	if hermesHome == "" {
		return Skipped, nil
	}
	if fi, err := os.Stat(hermesHome); err != nil || !fi.IsDir() {
		return Skipped, nil
	}
	dest := Path(hermesHome)
	if fi, err := os.Lstat(dest); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return Kept, nil // dev copy linked to the source
	}
	old, err := os.ReadFile(dest)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", err
		}
		return Installed, write(dest, skill)
	case err != nil:
		return "", err
	case bytes.Equal(old, skill):
		return Current, nil
	case bytes.Equal(unkeyed(old), Skill):
		return Updated, write(dest, skill)
	case Compare(Version(old), Version(skill)) >= 0:
		return Kept, nil
	}
	backup := dest + ".bak-" + time.Now().Format("20060102-150405")
	if err := os.WriteFile(backup, old, 0o644); err != nil {
		return "", fmt.Errorf("백업 %s: %w", backup, err)
	}
	return Updated, write(dest, skill)
}

// Remove results
const (
	Removed  = "removed"   // unchanged from our install, removed
	MovedOld = "moved-old" // old version, moved to .bak-<time>
)

// Remove deletes the skill we installed. The current version (including key-only differences) is deleted; an old version is moved to .bak.
// Symlinks, hand-edited same versions, and newer versions are Kept. Absent is Skipped. .bak files are left alone.
func Remove(hermesHome string) (string, error) {
	if hermesHome == "" {
		return Skipped, nil
	}
	dest := Path(hermesHome)
	fi, err := os.Lstat(dest)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Skipped, nil
	case err != nil:
		return "", err
	case fi.Mode()&fs.ModeSymlink != 0 || !fi.Mode().IsRegular():
		return Kept, nil
	}
	home, herr := filepath.EvalSymlinks(hermesHome)
	if real, err := filepath.EvalSymlinks(filepath.Dir(dest)); err != nil || herr != nil || real != filepath.Dir(Path(home)) {
		return Kept, nil // if the folder is a link (dotfiles etc.), don't delete the source side
	}
	old, err := os.ReadFile(dest)
	if err != nil {
		return "", err
	}
	switch {
	case bytes.Equal(unkeyed(old), Skill):
		if err := os.Remove(dest); err != nil {
			return "", err
		}
		os.Remove(filepath.Dir(dest)) // only removed when empty
		return Removed, nil
	case Version(old) != "" && Compare(Version(old), Version(Skill)) < 0:
		if err := os.Rename(dest, dest+".bak-"+time.Now().Format("20060102-150405")); err != nil {
			return "", err
		}
		return MovedOld, nil
	}
	return Kept, nil
}

func write(dest string, skill []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".SKILL.md.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(skill); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}

// Compare compares "1.2.0"-style versions; unreadable fields count as 0.
func Compare(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(as), len(bs)) {
		x, y := part(as, i), part(bs, i)
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func part(s []string, i int) int {
	if i >= len(s) {
		return 0
	}
	n, _ := strconv.Atoi(s[i])
	return n
}
