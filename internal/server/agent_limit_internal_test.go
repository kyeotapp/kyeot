package server

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"testing"

	"github.com/kyeotapp/kyeot/internal/store"
)

// Even when created concurrently, there are never more than 20. Excess requests get 409 and a reason.
func TestAgentLimitHoldsUnderConcurrency(t *testing.T) {
	srv := newLocal(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, refused := 0, 0
	for i := range 40 {
		wg.Go(func() {
			_, _, err := srv.hub.CreateAgent(store.Owner, fmt.Sprintf("a%d", i))
			mu.Lock()
			defer mu.Unlock()
			var ae *ActionError
			switch {
			case err == nil:
				ok++
			case errors.As(err, &ae) && ae.Status == http.StatusConflict && ae.Msg == "에이전트는 20개까지 만들 수 있어요":
				refused++
			default:
				t.Errorf("unexpected: %v", err)
			}
		})
	}
	wg.Wait()
	if ok != 20 || refused != 20 || agentCount(t, srv) != 20 {
		t.Fatalf("ok %d refused %d stored %d", ok, refused, agentCount(t, srv))
	}

	// the same-Mac Hermes path has the same cap
	if _, err := srv.LocalAgent("Hermes", filepath.Join(t.TempDir(), "hermes-agent")); err == nil {
		t.Fatal("local agent must hit the same limit")
	}
	// releasing frees a slot
	list, _ := srv.store.Agents(store.Owner)
	srv.store.UnlinkAgent(list[0].ID)
	if _, err := srv.LocalAgent("Hermes", filepath.Join(t.TempDir(), "hermes-agent")); err != nil {
		t.Fatalf("after unlink: %v", err)
	}
}

// Old data already over 20 is left as is; only creating new ones is blocked.
func TestAgentLimitKeepsOldData(t *testing.T) {
	srv := newLocal(t)
	for i := range 25 {
		if _, _, err := srv.store.CreateAgent(store.Owner, fmt.Sprintf("old%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := srv.hub.CreateAgent(store.Owner, "new"); err == nil {
		t.Fatal("must refuse")
	}
	if n := agentCount(t, srv); n != 25 {
		t.Fatalf("old agents changed: %d", n)
	}
}
