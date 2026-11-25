package main

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/store"
)

// An unknown command does not start the server. It creates neither a token nor a DB and prints the usage.
func TestUnknownCommandDoesNotStartServer(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := filepath.Join(t.TempDir(), "kyeot")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	run := func(arg string) (string, string, int, string) {
		home := t.TempDir()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, arg)
		cmd.Dir = home
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "KYEOT_ADDR=127.0.0.1:0", "KYEOT_LANG=ko"}
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("%s: %v", arg, err)
		}
		return stdout.String(), stderr.String(), code, home
	}
	empty := func(dir string) bool {
		n := 0
		filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if p != dir {
				n++
			}
			return nil
		})
		return n == 0
	}

	for _, a := range []string{"help", "--help", "-h"} {
		out, _, code, home := run(a)
		if code != 0 || !strings.Contains(out, "service install") || !empty(home) {
			t.Fatalf("%s: code=%d out=%q", a, code, out)
		}
	}
	_, errOut, code, home := run("hlep")
	if code != 2 || !strings.Contains(errOut, "모르는 명령: hlep") || !strings.Contains(errOut, "쓰는 법") {
		t.Fatalf("hlep: code=%d stderr=%q", code, errOut)
	}
	if !empty(home) {
		t.Fatal("an unknown command created a file (token or DB)")
	}
}

// If the port is already in use (a server is running), a second kyeot ends before touching the DB. It must not overwrite the running server's agent state.
func TestBusyPortLeavesAgentStatesAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := filepath.Join(t.TempDir(), "kyeot")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	home := t.TempDir()
	db := filepath.Join(home, "kyeot.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := st.CreateAgent(store.Owner, "Agent A")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetAgentState(a.ID, protocol.AgentIdle); err != nil {
		t.Fatal(err)
	}
	st.Close()

	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "KYEOT_ADDR=" + busy.Addr().String(), "KYEOT_DB=" + db, "KYEOT_HERMES=off", "KYEOT_LANG=ko"}
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("started although the port was taken: %s", out)
	}

	st, err = store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err := st.Agent(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != protocol.AgentIdle {
		t.Fatalf("the second process overwrote the agent state: %s", got.State)
	}
}
