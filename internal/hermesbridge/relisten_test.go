package hermesbridge

import (
	"context"
	"net"
	"testing"
)

func TestUIRelistenAfterStop(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	fails := 0
	for i := 0; i < 20; i++ {
		ctxA, cancelA := context.WithCancel(context.Background())
		if _, err := New(Config{UIAddr: addr}).ListenUI(ctxA); err != nil {
			t.Fatalf("A %d: %v", i, err)
		}
		cancelA() // new adapter right after c.stop() in startLocal
		ctxB, cancelB := context.WithCancel(context.Background())
		if _, err := New(Config{UIAddr: addr}).ListenUI(ctxB); err != nil {
			fails++
		}
		cancelB()
		for { // drain fully before the next round
			if l, err := net.Listen("tcp", addr); err == nil {
				l.Close()
				break
			}
		}
	}
	if fails > 0 {
		t.Fatalf("reopen failed %d/20", fails)
	}
}
