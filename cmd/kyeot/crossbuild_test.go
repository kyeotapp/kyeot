package main

import (
	"os"
	"os/exec"
	"testing"
)

// The server must build on every operating system. Mac- and Linux-only calls go in _unix.go / _windows.go.
func TestBuildsOnEveryOS(t *testing.T) {
	if testing.Short() {
		t.Skip("cross builds")
	}
	for _, target := range [][2]string{{"darwin", "arm64"}, {"darwin", "amd64"}, {"linux", "arm64"}, {"linux", "amd64"}, {"windows", "amd64"}, {"windows", "arm64"}} {
		cmd := exec.Command("go", "build", "-o", os.DevNull, "../...")
		cmd.Env = append(os.Environ(), "GOOS="+target[0], "GOARCH="+target[1], "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s/%s: %v\n%s", target[0], target[1], err, out)
		}
	}
}
