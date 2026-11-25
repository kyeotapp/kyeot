package hermesbridge_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/hermesbridge"
)

type versionsReply struct {
	Versions []struct {
		Version int    `json:"version"`
		Current bool   `json:"current"`
		Summary string `json:"summary"`
	} `json:"versions"`
	Reason string `json:"reason"`
}

func habits(title string) string {
	return fmt.Sprintf(`{"version":1,"id":"habits","title":%q,"icon":"checkmark","blocks":[{"type":"divider"}]}`, title)
}

func ownerTitle(t *testing.T, h *decktest.Harness, agentID, screenID string) string {
	t.Helper()
	var st struct {
		Screens []struct {
			ID   string `json:"id"`
			Spec struct {
				Title string `json:"title"`
			} `json:"spec"`
		} `json:"screens"`
	}
	h.API("GET", "/api/agents/"+agentID+"/ui", nil, &st)
	for _, s := range st.Screens {
		if s.ID == screenID {
			return s.Spec.Title
		}
	}
	return ""
}

// A fresh conversation has no memory of earlier specs, so the agent must list versions and revert by number.
func TestBridgeUIVersionsAndRevert(t *testing.T) {
	h := decktest.New(t)
	c := h.ConnectClient()
	ui, agent := startUIBridge(t, h)
	h.WaitFor(c, "bridge idle", decktest.AgentState(agent.Agent.ID, "idle"))

	var res uiReply
	for i := 1; i <= 5; i++ {
		if s := call(t, "POST", ui+"/ui/put", habits(fmt.Sprintf("v%d", i)), &res); s != http.StatusOK || !res.OK {
			t.Fatalf("put v%d: %d %+v", i, s, res)
		}
	}
	var vs versionsReply
	if s := call(t, "GET", ui+"/ui/screens/habits/versions", "", &vs); s != http.StatusOK || len(vs.Versions) != 5 {
		t.Fatalf("versions: %d %+v", s, vs)
	}
	if v := vs.Versions[0]; v.Version != 5 || !v.Current || !strings.Contains(v.Summary, `"v5"`) || vs.Versions[1].Current {
		t.Fatalf("newest first, only it current, summary has title: %+v", vs.Versions)
	}

	// "two versions back" = versions[2]
	back := vs.Versions[2].Version
	if s := call(t, "POST", ui+"/ui/screens/habits/revert", fmt.Sprintf(`{"version":%d}`, back), &res); s != http.StatusOK || !res.OK || res.ID != "habits" || !strings.Contains(res.Reason, "as version 6") {
		t.Fatalf("revert: %d %+v", s, res)
	}
	if got := ownerTitle(t, h, agent.Agent.ID, "habits"); got != "v3" {
		t.Fatalf("owner sees %q, want v3", got)
	}

	if s := call(t, "POST", ui+"/ui/screens/habits/revert", `{"version":99}`, &res); s != http.StatusBadRequest || res.OK || !strings.Contains(res.Reason, "no version 99") {
		t.Fatalf("missing version: %d %+v", s, res)
	}
	if s := call(t, "POST", ui+"/ui/screens/habits/revert", `{}`, &res); s != http.StatusBadRequest {
		t.Fatalf("no version: %d %+v", s, res)
	}
	if s := call(t, "GET", ui+"/ui/screens/nope/versions", "", &vs); s != http.StatusNotFound || !strings.Contains(vs.Reason, "no such screen") {
		t.Fatalf("unknown screen: %d %+v", s, vs)
	}

	// A removed screen keeps its versions; reverting brings it back.
	if s := call(t, "POST", ui+"/ui/remove", `{"id":"habits"}`, &res); s != http.StatusOK || !res.OK {
		t.Fatalf("remove: %d %+v", s, res)
	}
	if got := ownerTitle(t, h, agent.Agent.ID, "habits"); got != "" {
		t.Fatalf("removed screen still shown: %q", got)
	}
	if s := call(t, "POST", ui+"/ui/screens/habits/revert", `{"version":2}`, &res); s != http.StatusOK || !res.OK {
		t.Fatalf("revert removed: %d %+v", s, res)
	}
	if got := ownerTitle(t, h, agent.Agent.ID, "habits"); got != "v2" {
		t.Fatalf("revert should bring the removed screen back as v2, got %q", got)
	}
}

// Another profile is another agent of the same owner; another user is an agent of a different owner. Neither may touch habits.
func TestBridgeUIRevertOnlyOwnScreens(t *testing.T) {
	h := decktest.New(t)
	c := h.ConnectClient()
	ui, agent := startUIBridge(t, h)
	h.WaitFor(c, "bridge idle", decktest.AgentState(agent.Agent.ID, "idle"))
	var res uiReply
	call(t, "POST", ui+"/ui/put", habits("mine-1"), &res)
	call(t, "POST", ui+"/ui/put", habits("mine-2"), &res)

	profileUI, _ := startUIBridge(t, h)
	strangerUI := startBridgeFor(t, h, secondOwnerAgent(t, h))

	for name, other := range map[string]string{"other profile": profileUI, "other user": strangerUI} {
		var vs versionsReply
		if s := call(t, "GET", other+"/ui/screens/habits/versions", "", &vs); s != http.StatusNotFound || len(vs.Versions) != 0 {
			t.Fatalf("%s sees versions: %d %+v", name, s, vs)
		}
		if s := call(t, "POST", other+"/ui/screens/habits/revert", `{"version":1}`, &res); s != http.StatusBadRequest || res.OK {
			t.Fatalf("%s reverted: %d %+v", name, s, res)
		}
	}
	if got := ownerTitle(t, h, agent.Agent.ID, "habits"); got != "mine-2" {
		t.Fatalf("habits changed to %q", got)
	}
}

// secondOwnerAgent pairs a second user and returns an agent token they own.
func secondOwnerAgent(t *testing.T, h *decktest.Harness) string {
	t.Helper()
	var inv struct{ Code string }
	if s := h.API("POST", "/api/owners/invite", nil, &inv); s != http.StatusOK {
		t.Fatalf("invite: %d", s)
	}
	body, _ := json.Marshal(map[string]string{"code": inv.Code, "name": "stranger"})
	req, _ := http.NewRequest("POST", h.URL+"/pair/owner", bytes.NewReader(body))
	req.Header.Set("X-Kyeot-Pair", "1")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var owner struct{ Token string }
	json.NewDecoder(r.Body).Decode(&owner)
	var created decktest.Created
	if s := h.APIAs(owner.Token, "POST", "/api/agents", map[string]string{"name": "stranger-bot"}, &created); s != http.StatusCreated {
		t.Fatalf("stranger agent: %d", s)
	}
	return created.Token
}

func startBridgeFor(t *testing.T, h *decktest.Harness, token string) string {
	t.Helper()
	_, hermesURL := newFakeHermes(t)
	b := hermesbridge.New(hermesbridge.Config{DeckURL: h.URL, AgentToken: token, HermesURL: hermesURL, HermesKey: hermesKey,
		UIAddr: "127.0.0.1:0", UITimeout: 2 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	addr, err := b.ListenUI(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	ui := "http://" + addr
	for deadline := time.Now().Add(decktest.WaitLimit); call(t, "GET", ui+"/ui", "", nil) == http.StatusServiceUnavailable; {
		if time.Now().After(deadline) {
			t.Fatal("bridge never attached to deck")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ui
}
