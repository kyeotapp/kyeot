package hermesbridge

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

func TestRoutineTriggerRunsKnownJobOnly(t *testing.T) {
	home := fakeHome(t, time.Now())
	log := filepath.Join(t.TempDir(), "calls")
	bin := filepath.Join(t.TempDir(), "hermes")
	os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\" >> "+log+"\n"), 0o755)

	b := New(Config{HermesHome: home, HermesBin: bin})
	b.handle(context.Background(), protocol.ServerMessage{Type: protocol.TypeRoutineTrigger, Routine: "job1"})
	b.handle(context.Background(), protocol.ServerMessage{Type: protocol.TypeRoutineTrigger, Routine: "nope"})
	b.handle(context.Background(), protocol.ServerMessage{Type: protocol.TypeRoutineTrigger, Routine: "job1; rm -rf /"})

	got := ""
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end) && got == ""; time.Sleep(20 * time.Millisecond) { // run goes separately
		got = readText(log)
	}
	if got != "cron run job1\n" {
		t.Fatalf("hermes calls:\n%s", got)
	}
}
