package hermesbridge_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/hermesbridge"
)

// startUIBridge attaches a bridge with the local address open to the deck and returns that address and the agent.
func startUIBridge(t *testing.T, h *decktest.Harness) (string, decktest.Created) {
	t.Helper()
	_, hermesURL := newFakeHermes(t)
	agent := h.CreateAgent("Agent A")
	b := hermesbridge.New(hermesbridge.Config{DeckURL: h.URL, AgentToken: agent.Token, HermesURL: hermesURL, HermesKey: hermesKey,
		UIAddr: "127.0.0.1:0", UITimeout: 2 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	addr, err := b.ListenUI(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		b.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	ui := "http://" + addr
	for deadline := time.Now().Add(decktest.WaitLimit); call(t, "GET", ui+"/ui", "", nil) == http.StatusServiceUnavailable; { // until the bridge gets the deck connection
		if time.Now().After(deadline) {
			t.Fatal("bridge never attached to deck")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ui, agent
}

type uiReply struct {
	OK     bool   `json:"ok"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

func call(t *testing.T, method, url, body string, out any) int {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Error(err)
		return 0
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Errorf("%s %s: %v %s", method, url, err, raw)
		}
	}
	return res.StatusCode
}

func revenue(id string) string {
	return fmt.Sprintf(`{"version":1,"id":%q,"title":"Revenue","icon":"chart.bar","blocks":[{"type":"stat","label":"This month","value":1200000,"unit":"KRW","compare":950000,"better":"up"}]}`, id)
}

type ownerUI struct {
	Screens []struct {
		ID string `json:"id"`
	} `json:"screens"`
	Tabs []string `json:"tabs"`
}

func (u ownerUI) has(id string) bool {
	for _, s := range u.Screens {
		if s.ID == id {
			return true
		}
	}
	return false
}

func TestBridgeUIPutReachesOwner(t *testing.T) {
	h := decktest.New(t)
	c := h.ConnectClient()
	ui, agent := startUIBridge(t, h)
	h.WaitFor(c, "bridge idle", decktest.AgentState(agent.Agent.ID, "idle"))

	var cat map[string]any
	if s := call(t, "GET", ui+"/ui/catalog", "", &cat); s != http.StatusOK || cat["components"] == nil {
		t.Fatalf("catalog: %d %v", s, cat)
	}

	var res uiReply
	if s := call(t, "POST", ui+"/ui/put", revenue("revenue"), &res); s != http.StatusOK || !res.OK || res.ID != "revenue" {
		t.Fatalf("put: %d %+v", s, res)
	}
	var owner ownerUI
	h.API("GET", "/api/agents/"+agent.Agent.ID+"/ui", nil, &owner)
	if !owner.has("revenue") {
		t.Fatalf("owner API should show revenue: %+v", owner)
	}

	if s := call(t, "POST", ui+"/ui/put", revenue("week"), &res); s != http.StatusOK || !res.OK {
		t.Fatalf("put week: %d %+v", s, res)
	}
	var now ownerUI
	if s := call(t, "GET", ui+"/ui", "", &now); s != http.StatusOK || !now.has("revenue") || !now.has("week") {
		t.Fatalf("GET /ui: %d %+v", s, now)
	}

	if s := call(t, "POST", ui+"/ui/layout", `{"tabs":["revenue","week"]}`, &res); s != http.StatusOK || !res.OK {
		t.Fatalf("layout: %d %+v", s, res)
	}
	call(t, "GET", ui+"/ui", "", &now)
	if strings.Join(now.Tabs, ",") != "revenue,week" {
		t.Fatalf("tabs: %v", now.Tabs)
	}

	if s := call(t, "POST", ui+"/ui/remove", `{"id":"revenue"}`, &res); s != http.StatusOK || !res.OK || res.ID != "revenue" {
		t.Fatalf("remove: %d %+v", s, res)
	}
	owner = ownerUI{}
	h.API("GET", "/api/agents/"+agent.Agent.ID+"/ui", nil, &owner)
	if owner.has("revenue") {
		t.Fatalf("revenue should be gone: %+v", owner)
	}
}

func TestBridgeUIRejectionCarriesReason(t *testing.T) {
	h := decktest.New(t)
	c := h.ConnectClient()
	ui, agent := startUIBridge(t, h)
	h.WaitFor(c, "bridge idle", decktest.AgentState(agent.Agent.ID, "idle"))

	var res uiReply
	bad := `{"version":1,"id":"revenue","title":"Revenue","icon":"chart.bar","blocks":[{"type":"pie"}]}`
	if s := call(t, "POST", ui+"/ui/put", bad, &res); s != http.StatusBadRequest || res.OK || !strings.Contains(res.Reason, "blocks[0]") {
		t.Fatalf("bad put: %d %+v", s, res)
	}
	if s := call(t, "POST", ui+"/ui/layout", `{"tabs":["nope"]}`, &res); s != http.StatusBadRequest || !strings.Contains(res.Reason, "없는 화면") {
		t.Fatalf("bad layout: %d %+v", s, res)
	}
	if s := call(t, "POST", ui+"/ui/put", `[1,2]`, &res); s != http.StatusBadRequest || res.Reason == "" {
		t.Fatalf("not an object: %d %+v", s, res)
	}
}

func TestBridgeUIConcurrentRequestsGetOwnReplies(t *testing.T) {
	h := decktest.New(t)
	c := h.ConnectClient()
	ui, agent := startUIBridge(t, h)
	h.WaitFor(c, "bridge idle", decktest.AgentState(agent.Agent.ID, "idle"))

	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("screen-%d", i)
			body, wantOK, wantStatus := revenue(id), true, http.StatusOK
			if i%2 == 1 {
				body = strings.Replace(body, `"version":1`, `"version":2`, 1)
				wantOK, wantStatus = false, http.StatusBadRequest
			}
			var res uiReply
			if s := call(t, "POST", ui+"/ui/put", body, &res); s != wantStatus || res.OK != wantOK || (wantOK && res.ID != id) || (!wantOK && !strings.Contains(res.Reason, "version")) {
				t.Errorf("%s: %d %+v", id, s, res)
			}
		}()
	}
	wg.Wait()
}

func TestBridgeUIWithoutDeck(t *testing.T) {
	b := hermesbridge.New(hermesbridge.Config{DeckURL: "http://127.0.0.1:1", UIAddr: "127.0.0.1:0"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr, err := b.ListenUI(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var res uiReply
	if s := call(t, "GET", "http://"+addr+"/ui", "", &res); s != http.StatusServiceUnavailable || res.Reason == "" {
		t.Fatalf("no deck: %d %+v", s, res)
	}

	if _, err := hermesbridge.New(hermesbridge.Config{UIAddr: "0.0.0.0:0"}).ListenUI(ctx); err == nil {
		t.Fatal("0.0.0.0 should be refused")
	}
}

type dataUI struct {
	Data map[string]struct {
		Value     json.RawMessage `json:"value"`
		UpdatedAt int64           `json:"updatedAt"`
	} `json:"data"`
}

func TestBridgeDataPutReachesOwner(t *testing.T) {
	h := decktest.New(t)
	c := h.ConnectClient()
	ui, agent := startUIBridge(t, h)
	h.WaitFor(c, "bridge idle", decktest.AgentState(agent.Agent.ID, "idle"))

	var res uiReply
	if s := call(t, "POST", ui+"/data/put", `{"name":"revenue","data":{"month":{"total":1200000}}}`, &res); s != http.StatusOK || !res.OK || res.ID != "revenue" {
		t.Fatalf("put: %d %+v", s, res)
	}
	var owner dataUI
	h.API("GET", "/api/agents/"+agent.Agent.ID+"/ui", nil, &owner)
	if string(owner.Data["revenue"].Value) != `{"month":{"total":1200000}}` || owner.Data["revenue"].UpdatedAt == 0 {
		t.Fatalf("owner data: %+v", owner)
	}
	var now dataUI
	if s := call(t, "GET", ui+"/ui", "", &now); s != http.StatusOK || now.Data["revenue"].Value == nil {
		t.Fatalf("GET /ui data: %d %+v", s, now)
	}

	big := `{"name":"big","data":{"s":"` + strings.Repeat("x", 262144-8) + `"}}`
	if s := call(t, "POST", ui+"/data/put", big, &res); s != http.StatusOK || !res.OK {
		t.Fatalf("a 256KB bundle does not hit the bridge body limit: %d %+v", s, res)
	}

	for name, body := range map[string]string{
		"deck":     `{"name":"deck","data":{"x":1}}`,
		"array":    `{"name":"arr","data":[1]}`,
		"no name":  `{"data":{"x":1}}`,
		"not JSON": `{`,
	} {
		if s := call(t, "POST", ui+"/data/put", body, &res); s != http.StatusBadRequest || res.OK || res.Reason == "" {
			t.Errorf("%s: %d %+v", name, s, res)
		}
	}

	if s := call(t, "POST", ui+"/data/remove", `{"name":"revenue"}`, &res); s != http.StatusOK || !res.OK || res.ID != "revenue" {
		t.Fatalf("remove: %d %+v", s, res)
	}
	if s := call(t, "POST", ui+"/data/remove", `{"name":"revenue"}`, &res); s != http.StatusBadRequest || !strings.Contains(res.Reason, "없는 묶음") {
		t.Fatalf("remove again: %d %+v", s, res)
	}
	if s := call(t, "POST", ui+"/data/remove", `{}`, &res); s != http.StatusBadRequest {
		t.Fatalf("remove without name: %d", s)
	}
}

func TestBridgeDataConcurrentPuts(t *testing.T) {
	h := decktest.New(t)
	c := h.ConnectClient()
	ui, agent := startUIBridge(t, h)
	h.WaitFor(c, "bridge idle", decktest.AgentState(agent.Agent.ID, "idle"))

	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name, wantOK := fmt.Sprintf("b%d", i), true
			if i%2 == 1 {
				name, wantOK = fmt.Sprintf("B%d", i), false
			}
			var res uiReply
			s := call(t, "POST", ui+"/data/put", fmt.Sprintf(`{"name":%q,"data":{"i":%d}}`, name, i), &res)
			if res.OK != wantOK || res.ID != name || (wantOK && s != http.StatusOK) || (!wantOK && s != http.StatusBadRequest) {
				t.Errorf("%s: %d %+v", name, s, res)
			}
		}()
	}
	wg.Wait()
}
