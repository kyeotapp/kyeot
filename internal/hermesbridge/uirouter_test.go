package hermesbridge_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/hermesbridge"
)

// startRouted starts a bridge attached at the key slot of router.
func startRouted(t *testing.T, h *decktest.Harness, r *hermesbridge.UIRouter, key, name string) decktest.Created {
	t.Helper()
	_, hermesURL := newFakeHermes(t)
	agent := h.CreateAgent(name)
	b := hermesbridge.New(hermesbridge.Config{DeckURL: h.URL, AgentToken: agent.Token, HermesURL: hermesURL, HermesKey: hermesKey,
		UIAddr: "127.0.0.1:0", UIRouter: r, UIKey: key, UITimeout: 2 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		b.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	return agent
}

// waitRoute waits until router is open and the bridge at the prefix slot has attached to the deck, then returns its address.
func waitRoute(t *testing.T, r *hermesbridge.UIRouter, prefix string) string {
	t.Helper()
	for deadline := time.Now().Add(decktest.WaitLimit); ; {
		if addr := r.Addr(); addr != "127.0.0.1:0" {
			base := "http://" + addr
			res, err := http.Get(base + prefix + "/ui")
			if err == nil {
				res.Body.Close()
				if res.StatusCode == http.StatusOK {
					return base
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not attach", prefix)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func screensOf(t *testing.T, h *decktest.Harness, agentID string) ownerUI {
	t.Helper()
	var u ownerUI
	h.API("GET", "/api/agents/"+agentID+"/ui", nil, &u)
	return u
}

func TestUIRouterSendsEachProfileToItsOwnAgent(t *testing.T) {
	h := decktest.New(t)
	r := hermesbridge.NewUIRouter("127.0.0.1:0")
	t.Cleanup(r.Close)
	const keyB = "profileB-key-0123456789"
	def := startRouted(t, h, r, "", "Agent A")
	prof := startRouted(t, h, r, keyB, "Agent B")
	bystander := h.CreateAgent("Agent C")
	base := waitRoute(t, r, "")
	waitRoute(t, r, "/h/"+keyB)

	var res uiReply
	if s := call(t, "POST", base+"/ui/put", revenue("home"), &res); s != http.StatusOK || !res.OK {
		t.Fatalf("default put: %d %+v", s, res)
	}
	if s := call(t, "POST", base+"/h/"+keyB+"/ui/put", revenue("work"), &res); s != http.StatusOK || !res.OK {
		t.Fatalf("profile put: %d %+v", s, res)
	}
	if u := screensOf(t, h, def.Agent.ID); !u.has("home") || u.has("work") {
		t.Fatalf("default agent screen: %+v", u)
	}
	if u := screensOf(t, h, prof.Agent.ID); !u.has("work") || u.has("home") {
		t.Fatalf("profile agent screen: %+v", u)
	}
	if u := screensOf(t, h, bystander.Agent.ID); len(u.Screens) != 0 {
		t.Fatalf("another agent's screen changed: %+v", u)
	}

	// a profile address cannot delete or see the default screen
	call(t, "POST", base+"/h/"+keyB+"/ui/remove", `{"id":"home"}`, &res)
	if !screensOf(t, h, def.Agent.ID).has("home") {
		t.Fatal("a profile address deleted the default screen")
	}
	var seen ownerUI
	call(t, "GET", base+"/h/"+keyB+"/ui", "", &seen)
	if seen.has("home") {
		t.Fatalf("the default screen is visible at a profile address: %+v", seen)
	}

	for _, bad := range []string{"/h/wrong-key-0123456789/ui", "/h/short/ui", "/h/" + keyB + "x/ui", "/h//ui"} {
		if s := call(t, "POST", base+bad+"/put", revenue("x"), nil); s != http.StatusNotFound {
			t.Fatalf("%s: %d, must be 404", bad, s)
		}
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, odd := range []string{"/h/../ui/put", "/h/" + keyB + "//ui/put", "/h/" + keyB + "/./ui/put", "/h/" + keyB + "/../ui/put", "/h/" + keyB + "/x/../ui/put", "/h/" + keyB + "%2Fui/put"} {
		req, _ := http.NewRequest("POST", base+odd, strings.NewReader(revenue("x")))
		req.Header.Set("Content-Type", "application/json")
		res, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: %d (Location %q), must be 404", odd, res.StatusCode, res.Header.Get("Location"))
		}
	}
	for _, id := range []string{def.Agent.ID, prof.Agent.ID, bystander.Agent.ID} {
		if screensOf(t, h, id).has("x") {
			t.Fatalf("a screen was created at %s with a wrong key", id)
		}
	}
}

func TestUIRouterDetachesStoppedProfile(t *testing.T) {
	h := decktest.New(t)
	r := hermesbridge.NewUIRouter("127.0.0.1:0")
	t.Cleanup(r.Close)
	const key = "stopme-key-0123456789"
	_, hermesURL := newFakeHermes(t)
	agent := h.CreateAgent("Agent B")
	b := hermesbridge.New(hermesbridge.Config{DeckURL: h.URL, AgentToken: agent.Token, HermesURL: hermesURL, HermesKey: hermesKey,
		UIAddr: "127.0.0.1:0", UIRouter: r, UIKey: key, UITimeout: 2 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	base := waitRoute(t, r, "/h/"+key)
	cancel()
	<-done
	if s := call(t, "GET", base+"/h/"+key+"/ui", "", nil); s != http.StatusNotFound {
		t.Fatalf("stopped profile slot: %d", s)
	}
}

// A profile bridge without a key cannot take the default slot (/ui/…).
func TestUIRouterProfileWithoutKeyCannotTakeDefault(t *testing.T) {
	h := decktest.New(t)
	r := hermesbridge.NewUIRouter("127.0.0.1:0")
	t.Cleanup(r.Close)
	def := startRouted(t, h, r, "", "Agent A")
	base := waitRoute(t, r, "")
	_, hermesURL := newFakeHermes(t)
	prof := h.CreateAgent("Agent B")
	b := hermesbridge.New(hermesbridge.Config{DeckURL: h.URL, AgentToken: prof.Token, HermesURL: hermesURL, HermesKey: hermesKey,
		UIAddr: "127.0.0.1:0", UIRouter: r, UIProfile: true, UITimeout: 2 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	c := h.ConnectClient()
	h.WaitFor(c, "profile idle", decktest.AgentState(prof.Agent.ID, "idle"))
	var res uiReply
	if s := call(t, "POST", base+"/ui/put", revenue("home"), &res); s != http.StatusOK || !res.OK {
		t.Fatalf("default put: %d %+v", s, res)
	}
	if !screensOf(t, h, def.Agent.ID).has("home") || screensOf(t, h, prof.Agent.ID).has("home") {
		t.Fatal("a keyless profile took the default slot")
	}
}
