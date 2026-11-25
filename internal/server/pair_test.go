package server

import (
	"fmt"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/store"
)

func TestPairCodeOnceExpiryAndThrottle(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	p := &pairStore{now: func() time.Time { return now }}

	code, _ := p.issue(store.Owner, "a1")
	if len(code) != 6 {
		t.Fatalf("code %q", code)
	}
	if pc, ok, _ := p.take(code, "h"); !ok || pc.agentID != "a1" {
		t.Fatal("first use should succeed")
	}
	if _, ok, _ := p.take(code, "h"); ok {
		t.Fatal("code must be single use")
	}

	old, _ := p.issue(store.Owner, "a1")
	fresh, _ := p.issue(store.Owner, "a1")
	if _, ok, _ := p.take(old, "h2"); ok && old != fresh {
		t.Fatal("reissuing should drop the previous code")
	}

	late, _ := p.issue(store.Owner, "a2")
	now = now.Add(pairTTL + time.Second)
	if _, ok, _ := p.take(late, "h3"); ok {
		t.Fatal("expired code must fail")
	}

	good, _ := p.issue(store.Owner, "a3")
	for i := 0; i < pairMaxFails; i++ {
		p.take("000000", "bad")
	}
	if _, ok, blocked := p.take(good, "bad"); ok || !blocked {
		t.Fatal("too many failures should block that address even with a right code")
	}
	if _, ok, _ := p.take(good, "other"); !ok {
		t.Fatal("other addresses are not blocked")
	}
}

func TestPairManyAddressesCannotLockOthersOut(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	p := &pairStore{now: func() time.Time { return now }}
	code, _ := p.issue(store.Owner, "a1")
	other, _ := p.issue(store.Owner, "a2")
	wrong := fmt.Sprintf("%06d", (atoi(code)+1)%1_000_000)
	if wrong == other {
		wrong = fmt.Sprintf("%06d", (atoi(code)+2)%1_000_000)
	}
	for i := 0; i < pairCodeMiss-1; i++ { // several addresses split the wrong guesses
		p.take(wrong, fmt.Sprintf("10.0.%d.%d", i/9, i%9)) // public-ok
	}
	if pc, ok, blocked := p.take(code, "10.9.9.9"); !ok || blocked || pc.agentID != "a1" { // public-ok
		t.Fatal("others' wrong guesses must not block another address's correct code")
	}
	now = now.Add(pairTTL + time.Second)
	p.take(wrong, "10.0.0.99") // public-ok
	if len(p.fails) != 1 {
		t.Fatalf("old entries should be swept: %d", len(p.fails))
	}
}

func TestPairCodeDroppedAfterTooManyMisses(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	p := &pairStore{now: func() time.Time { return now }}
	code, _ := p.issue(store.Owner, "a1")
	wrong := fmt.Sprintf("%06d", (atoi(code)+1)%1_000_000)
	for i := 0; i < pairCodeMiss; i++ {
		p.take(wrong, fmt.Sprintf("10.1.%d.%d", i/200, i%200)) // public-ok
	}
	if _, ok, _ := p.take(code, "10.9.9.9"); ok { // public-ok
		t.Fatal("a code guessed at too often should be dropped")
	}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func TestPairKeyGroupsIPv6(t *testing.T) {
	if pairKey("[2001:db8::1]:1") != pairKey("[2001:db8::ffff]:2") {
		t.Fatal("same /64 should count together")
	}
	if pairKey("10.0.0.1:1") == pairKey("10.0.0.2:1") { // public-ok
		t.Fatal("IPv4 addresses count separately")
	}
}

func TestPairDropOnUnlink(t *testing.T) {
	p := &pairStore{}
	code, _ := p.issue(store.Owner, "a1")
	p.drop("a1")
	if _, ok, _ := p.take(code, "h"); ok {
		t.Fatal("unlinked agent's code should be gone")
	}
}

func TestPairDropOwner(t *testing.T) {
	p := &pairStore{}
	code, _ := p.issue("friend", "r1")
	keep, _ := p.issue(store.Owner, "r2")
	p.dropOwner("friend")
	if _, ok, _ := p.take(code, "h"); ok {
		t.Fatal("revoked owner's code should be gone")
	}
	if _, ok, _ := p.take(keep, "h"); !ok {
		t.Fatal("other codes stay")
	}
}
