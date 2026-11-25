package server

import (
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/store"
)

// One address's lock lifts once its failure records pass pairTTL. Other addresses are not blocked meanwhile.
func TestPairAddressLockLiftsAfterWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	p := &pairStore{now: func() time.Time { return now }}
	for range pairMaxFails {
		p.take("000000", "127.0.0.1")
	}
	code, _ := p.issue(store.Owner, "a1")
	if _, ok, blocked := p.take(code, "127.0.0.1"); ok || !blocked {
		t.Fatal("address should be locked")
	}
	now = now.Add(pairTTL - time.Second)
	if _, _, blocked := p.take(code, "127.0.0.1"); !blocked {
		t.Fatal("still locked just before the window ends")
	}
	now = now.Add(2 * time.Second)
	fresh, _ := p.issue(store.Owner, "a1")
	if pc, ok, blocked := p.take(fresh, "127.0.0.1"); !ok || blocked || pc.agentID != "a1" {
		t.Fatalf("lock should lift: ok=%v blocked=%v", ok, blocked)
	}
}
