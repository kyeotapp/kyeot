package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

// dialAgent opens the agent websocket directly. On failure it returns the HTTP status.
func dialAgent(t *testing.T, h *decktest.Harness, token string) (*websocket.Conn, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), decktest.WaitLimit)
	defer cancel()
	conn, res, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.URL, "http")+"/agent",
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if err != nil {
		if res == nil {
			t.Fatalf("dial: %v", err)
		}
		return nil, res.StatusCode
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn, http.StatusSwitchingProtocols
}

func readServer(t *testing.T, conn *websocket.Conn) protocol.ServerMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), decktest.WaitLimit)
	defer cancel()
	var m protocol.ServerMessage
	if err := wsjson.Read(ctx, conn, &m); err != nil {
		t.Fatalf("read: %v", err)
	}
	return m
}

func rawPost(t *testing.T, url, token string, body []byte, header map[string]string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func agentList(t *testing.T, h *decktest.Harness) []protocol.Agent {
	t.Helper()
	var list []protocol.Agent
	if s := h.API("GET", "/api/agents", nil, &list); s != http.StatusOK {
		t.Fatalf("list: %d", s)
	}
	return list
}

func TestCreateAgentRejectsBadInput(t *testing.T) {
	h := decktest.New(t)
	for name, body := range map[string]string{
		"empty body":   ``,
		"broken json":  `{`,
		"no name":      `{}`,
		"null":         `null`,
		"array":        `[]`,
		"empty name":   `{"name":""}`,
		"spaces only":  `{"name":"   "}`,
		"blank chars":  `{"name":" \t\n "}`,
		"number name":  `{"name":42}`,
		"object name":  `{"name":{"x":1}}`,
		"over 1MB":     `{"name":"` + strings.Repeat("a", 1<<20) + `"}`,
		"trailing obj": `{"name":"a"}{"name":"b"}`,
	} {
		s, out := rawPost(t, h.URL+"/api/agents", decktest.OwnerToken, []byte(body), nil)
		if name == "trailing obj" { // only the first object is read
			if s != http.StatusCreated {
				t.Errorf("%s: %d", name, s)
			}
			continue
		}
		if s != http.StatusBadRequest {
			t.Errorf("%s: %d %v", name, s, out)
		}
	}
	if s, _ := rawPost(t, h.URL+"/api/agents", "wrong-owner-token", []byte(`{"name":"x"}`), nil); s != http.StatusUnauthorized {
		t.Fatalf("wrong owner: %d", s)
	}
	if s, _ := rawPost(t, h.URL+"/api/agents", "", []byte(`{"name":"x"}`), nil); s != http.StatusUnauthorized {
		t.Fatalf("no owner: %d", s)
	}
	if n := len(agentList(t, h)); n != 1 {
		t.Fatalf("only the trailing-object case should create one: %d", n)
	}
}

func TestCreateAgentNamesAndDuplicates(t *testing.T) {
	h := decktest.New(t)
	c := h.ConnectClient()
	a := h.CreateAgent("  🤖 Agent A\t")
	if a.Agent.Name != "🤖 Agent A" {
		t.Fatalf("trimmed name: %q", a.Agent.Name)
	}
	b1, b2 := h.CreateAgent("same name"), h.CreateAgent("same name")
	if b1.Agent.ID == b2.Agent.ID || b1.Token == b2.Token {
		t.Fatal("same name must still give two agents")
	}
	if n := len(agentList(t, h)); n != 3 {
		t.Fatalf("list: %d", n)
	}
	h.StartFakeAgent(b2.Token)
	h.WaitFor(c, "second twin online", decktest.AgentState(b2.Agent.ID, protocol.AgentIdle))
	for _, ag := range agentList(t, h) {
		if ag.ID == b1.Agent.ID && ag.State != protocol.AgentOffline {
			t.Fatalf("twin token must not bring the other online: %q", ag.State)
		}
	}

	if long := h.CreateAgent(strings.Repeat("가", 5000)); long.Agent.Name != strings.Repeat("가", 32) {
		t.Fatalf("long name: %d runes", len([]rune(long.Agent.Name)))
	}
	for in, want := range map[string]string{
		strings.Repeat("😀", 30) + "  가나":    strings.Repeat("😀", 30) + " 가",
		strings.Repeat("a", 31) + " " + "b": strings.Repeat("a", 31),
		"a\x00b\x1bc‮":                      "abc",
		"‮gnp.exe‬ Agent A":                 "gnp.exe Agent A",
		"⁦x⁩‏؜":                             "x",
		"line\nbreak\r\nname":               "line break name",
		"👩‍💻 Team":                          "👩‍💻 Team",
	} {
		if got := h.CreateAgent(in).Agent.Name; got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{"\x00\x1b", "‮‬", " \t‎ "} {
		body, _ := json.Marshal(map[string]string{"name": in})
		if s, _ := rawPost(t, h.URL+"/api/agents", decktest.OwnerToken, body, nil); s != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", in, s)
		}
	}
}

func TestAgentTokenRejections(t *testing.T) {
	h := decktest.New(t)
	rotated := h.CreateAgent("rotate")
	var res struct{ Token string }
	h.API("POST", "/api/agents/"+rotated.Agent.ID+"/token", nil, &res)
	unlinked := h.CreateAgent("unlink")
	h.API("DELETE", "/api/agents/"+unlinked.Agent.ID, nil, nil)

	for name, tok := range map[string]string{
		"empty":           "",
		"wrong":           "not-a-token",
		"owner token":     decktest.OwnerToken,
		"before rotation": rotated.Token,
		"after unlink":    unlinked.Token,
		"unlinked marker": "unlinked:" + unlinked.Agent.ID,
	} {
		if _, s := dialAgent(t, h, tok); s != http.StatusUnauthorized {
			t.Errorf("%s: %d", name, s)
		}
	}
	if _, s := dialAgent(t, h, res.Token); s != http.StatusSwitchingProtocols {
		t.Fatalf("rotated token: %d", s)
	}
	if s := h.API("POST", "/api/agents/"+unlinked.Agent.ID+"/token", nil, nil); s != http.StatusNotFound {
		t.Fatalf("rotate after unlink: %d", s)
	}
	if s := h.API("POST", "/api/agents/"+unlinked.Agent.ID+"/pair", nil, nil); s != http.StatusNotFound {
		t.Fatalf("pair after unlink: %d", s)
	}
	if s := h.API("DELETE", "/api/agents/"+unlinked.Agent.ID, nil, nil); s != http.StatusNotFound {
		t.Fatalf("unlink twice: %d", s)
	}
}

func TestSameTokenSecondConnectionReplacesFirst(t *testing.T) {
	h := decktest.New(t)
	c := h.ConnectClient()
	a := h.CreateAgent("two")
	first, _ := dialAgent(t, h, a.Token)
	h.WaitFor(c, "first online", decktest.AgentState(a.Agent.ID, protocol.AgentIdle))

	second, _ := dialAgent(t, h, a.Token)
	if code := closeCode(t, first); code != 4000 {
		t.Fatalf("first should be replaced with 4000: %d", code)
	}
	h.Quiet(c, "offline while second is attached", 300*time.Millisecond, decktest.AgentState(a.Agent.ID, protocol.AgentOffline))

	wsjson.Write(context.Background(), second, protocol.AgentMessage{Type: "status", State: "working"})
	h.WaitFor(c, "second still works", decktest.AgentState(a.Agent.ID, "working"))
	second.Close(websocket.StatusNormalClosure, "")
	h.WaitFor(c, "offline after second leaves", decktest.AgentState(a.Agent.ID, protocol.AgentOffline))
}

func TestAgentBadMessagesKeepConnection(t *testing.T) {
	h := decktest.New(t)
	c := h.ConnectClient()
	a := h.CreateAgent("messy")
	conn, _ := dialAgent(t, h, a.Token)
	ctx := context.Background()

	for _, raw := range []string{
		`{`, `null`, `[]`, `"hello"`, `{"type":"nope"}`, `{"type":""}`,
		`{"type":"status"}`, `{"type":"status","state":"sleeping"}`,
		`{"type":"message","task":"t1"}`, `{"type":"task.open","task":"t1","title":42}`,
		`{"type":"report","report":"text"}`,
	} {
		conn.Write(ctx, websocket.MessageText, []byte(raw))
		if m := readServer(t, conn); m.Type != "error" || m.Reason != "invalid message" {
			t.Fatalf("%s: %+v", raw, m)
		}
	}
	wsjson.Write(ctx, conn, protocol.AgentMessage{Type: "message", Task: "nobody", Text: "hi"})
	if m := readServer(t, conn); m.Type != "error" || !strings.Contains(m.Reason, "unknown task") {
		t.Fatalf("unknown task: %+v", m)
	}
	wsjson.Write(ctx, conn, protocol.AgentMessage{Type: "hello", Kind: strings.Repeat("k", 41)})
	if m := readServer(t, conn); m.Reason != "invalid hello" {
		t.Fatalf("long kind: %+v", m)
	}
	wsjson.Write(ctx, conn, map[string]any{"type": "hello", "models": []map[string]string{{"role": "", "name": "x"}}})
	if m := readServer(t, conn); m.Reason != "invalid hello" {
		t.Fatalf("empty model role: %+v", m)
	}

	// works without hello: hello is optional
	wsjson.Write(ctx, conn, protocol.AgentMessage{Type: "status", State: "working"})
	h.WaitFor(c, "status works without hello", decktest.AgentState(a.Agent.ID, "working"))

	// only names the server knows remain in features
	feats := make([]string, 100)
	for i := range feats {
		feats[i] = strings.Repeat("f", 80) + strings.Repeat("x", i)
	}
	wsjson.Write(ctx, conn, protocol.AgentMessage{Type: "hello", Kind: "odd", Features: feats})
	h.WaitFor(c, "features saved", func(m protocol.ClientMessage) bool {
		return m.Type == "agent" && m.Agent.ID == a.Agent.ID && len(m.Agent.Features) > 0
	})
	for _, ag := range agentList(t, h) {
		if ag.ID == a.Agent.ID {
			if !slices.Equal(ag.Features, []string{protocol.FeatureDeclared}) {
				t.Fatalf("unknown features must be dropped: %d stored", len(ag.Features))
			}
			if ag.State != "working" {
				t.Fatalf("bad messages must not change state: %q", ag.State)
			}
		}
	}
}

func TestAgentFlappingSettlesOnRealState(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("flicker")
	for range 20 { // disconnect right after attaching
		conn, _ := dialAgent(t, h, a.Token)
		conn.CloseNow()
	}
	var wg sync.WaitGroup
	for range 10 { // concurrently with the same token
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), decktest.WaitLimit)
			defer cancel()
			conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.URL, "http")+"/agent",
				&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + a.Token}}})
			if err == nil {
				conn.Close(websocket.StatusNormalClosure, "")
			}
		}()
	}
	wg.Wait()
	waitState(t, h.URL, a.Agent.ID, protocol.AgentOffline)

	h.StartFakeAgent(a.Token)
	waitState(t, h.URL, a.Agent.ID, protocol.AgentIdle)
}

// Even when unlinking and attaching overlap, an unlinked agent is not revived and no connection remains.
func TestUnlinkRacingConnects(t *testing.T) {
	h := decktest.New(t)
	for range 15 {
		a := h.CreateAgent("race")
		var wg sync.WaitGroup
		results := make(chan string, 8)
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), decktest.WaitLimit)
				defer cancel()
				conn, res, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.URL, "http")+"/agent",
					&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + a.Token}}})
				if err != nil {
					if res != nil && res.StatusCode == http.StatusUnauthorized {
						results <- "401"
					} else {
						results <- "dial: " + err.Error()
					}
					return
				}
				defer conn.CloseNow()
				for {
					if _, _, err := conn.Read(ctx); err != nil {
						switch websocket.CloseStatus(err) {
						case 4000, 4001:
							results <- "closed"
						default:
							results <- "left open: " + err.Error()
						}
						return
					}
				}
			}()
		}
		if s := h.API("DELETE", "/api/agents/"+a.Agent.ID, nil, nil); s != http.StatusNoContent {
			t.Fatalf("unlink: %d", s)
		}
		wg.Wait()
		close(results)
		for r := range results {
			if r != "401" && r != "closed" {
				t.Fatal(r)
			}
		}
		for _, ag := range agentList(t, h) {
			if ag.ID == a.Agent.ID {
				t.Fatal("unlinked agent came back")
			}
		}
	}
}

func TestRecreateSameNameAfterUnlink(t *testing.T) {
	h := decktest.New(t)
	c := h.ConnectClient()
	old := h.CreateAgent("Agent A")
	h.StartFakeAgent(old.Token)
	h.WaitFor(c, "old online", decktest.AgentState(old.Agent.ID, protocol.AgentIdle))
	h.API("DELETE", "/api/agents/"+old.Agent.ID, nil, nil)
	h.WaitFor(c, "removed", func(m protocol.ClientMessage) bool { return m.Type == "agent.removed" && m.AgentID == old.Agent.ID })

	fresh := h.CreateAgent("Agent A")
	if fresh.Agent.ID == old.Agent.ID || fresh.Agent.State != protocol.AgentOffline {
		t.Fatalf("new agent: %+v", fresh.Agent)
	}
	if _, s := dialAgent(t, h, old.Token); s != http.StatusUnauthorized {
		t.Fatalf("old token on the new agent: %d", s)
	}
	h.StartFakeAgent(fresh.Token)
	h.WaitFor(c, "new online", decktest.AgentState(fresh.Agent.ID, protocol.AgentIdle))
	if list := agentList(t, h); len(list) != 1 || list[0].ID != fresh.Agent.ID {
		t.Fatalf("list: %+v", list)
	}
}

func TestOthersCannotTouchMyAgent(t *testing.T) {
	h := decktest.New(t)
	mine := h.CreateAgent("mine")
	friend := invite(t, h, "Bob")
	var theirs decktest.Created
	if s := h.APIAs(friend, "POST", "/api/agents", map[string]string{"name": "Bob's"}, &theirs); s != http.StatusCreated {
		t.Fatalf("invited user creates: %d", s)
	}
	for _, tc := range []struct{ tok, id string }{{friend, mine.Agent.ID}, {decktest.OwnerToken, theirs.Agent.ID}} {
		for _, call := range [][2]string{
			{"POST", "/token"}, {"POST", "/pair"}, {"DELETE", ""},
		} {
			if s := h.APIAs(tc.tok, call[0], "/api/agents/"+tc.id+call[1], nil, nil); s != http.StatusNotFound {
				t.Errorf("%s %s on someone else's agent: %d", call[0], call[1], s)
			}
		}
	}
	if _, s := dialAgent(t, h, mine.Token); s != http.StatusSwitchingProtocols {
		t.Fatal("my token must still work")
	}
	var friendList []protocol.Agent
	h.APIAs(friend, "GET", "/api/agents", nil, &friendList)
	if len(friendList) != 1 || friendList[0].ID != theirs.Agent.ID {
		t.Fatalf("friend sees: %+v", friendList)
	}
}

func pairCall(t *testing.T, h *decktest.Harness, body string) (int, string) {
	t.Helper()
	s, out := rawPost(t, h.URL+"/pair/agent", "", []byte(body), map[string]string{"X-Kyeot-Pair": "1"})
	tok, _ := out["token"].(string)
	return s, tok
}

func issuePair(t *testing.T, h *decktest.Harness, id string) string {
	t.Helper()
	var issued struct{ Code string }
	if s := h.API("POST", "/api/agents/"+id+"/pair", nil, &issued); s != http.StatusOK {
		t.Fatalf("issue: %d", s)
	}
	return issued.Code
}

func TestPairCodeShapesAndConcurrentUse(t *testing.T) {
	h := decktest.New(t)
	c := h.ConnectClient()
	a := h.CreateAgent("pair")
	code := issuePair(t, h, a.Agent.ID)
	for name, body := range map[string]string{
		"5 digits": `{"code":"12345"}`, "7 digits": `{"code":"1234567"}`, "number": `{"code":123456}`,
		"broken": `{`, "empty": ``, "huge": `{"code":"` + strings.Repeat("1", 2000) + `"}`,
	} {
		if s, _ := pairCall(t, h, body); s != http.StatusBadRequest {
			t.Errorf("%s: %d", name, s)
		}
	}

	// the old attached connection drops when pairing changes the token (moving to another computer)
	oldConn, _ := dialAgent(t, h, a.Token)
	h.WaitFor(c, "old online", decktest.AgentState(a.Agent.ID, protocol.AgentIdle))

	var wg sync.WaitGroup
	statuses := make(chan int, 8)
	tokens := make(chan string, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, tok := pairCall(t, h, `{"code":"`+code+`"}`)
			statuses <- s
			if tok != "" {
				tokens <- tok
			}
		}()
	}
	wg.Wait()
	close(statuses)
	close(tokens)
	ok := 0
	for s := range statuses {
		switch s {
		case http.StatusOK:
			ok++
		case http.StatusUnauthorized:
		default:
			t.Fatalf("status %d", s)
		}
	}
	if ok != 1 || len(tokens) != 1 {
		t.Fatalf("one code must pair once: %d ok", ok)
	}
	if code := closeCode(t, oldConn); code != 4002 {
		t.Fatalf("old connection close: %d", code)
	}
	h.StartFakeAgent(<-tokens)
	h.WaitFor(c, "paired online", decktest.AgentState(a.Agent.ID, protocol.AgentIdle))

	gone := h.CreateAgent("to delete")
	stale := issuePair(t, h, gone.Agent.ID)
	h.API("DELETE", "/api/agents/"+gone.Agent.ID, nil, nil)
	if s, _ := pairCall(t, h, `{"code":"`+stale+`"}`); s != http.StatusUnauthorized {
		t.Fatalf("code of unlinked agent: %d", s)
	}
}

// Loopback is a single address, so when one person fails ten times, others on the same computer are blocked too (report "policy judgment").
func TestPairLockoutOverHTTP(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("lockout")
	code := issuePair(t, h, a.Agent.ID)
	wrong := "000000"
	if code == wrong {
		wrong = "000001"
	}
	for i := range 10 {
		if s, _ := pairCall(t, h, `{"code":"`+wrong+`"}`); s != http.StatusUnauthorized {
			t.Fatalf("miss %d: %d", i, s)
		}
	}
	if s, _ := pairCall(t, h, `{"code":"`+code+`"}`); s != http.StatusTooManyRequests {
		t.Fatalf("right code while locked: %d", s)
	}
	if _, s := dialAgent(t, h, a.Token); s != http.StatusSwitchingProtocols {
		t.Fatal("lockout must not touch existing tokens")
	}
}

// The cap is per person. An invited user also gets 20; beyond that, 409 and a reason.
func TestAgentLimitPerPerson(t *testing.T) {
	h := decktest.New(t)
	friend := invite(t, h, "Bob")
	for i := range 20 {
		h.CreateAgent(fmt.Sprintf("owner%d", i))
		if s := h.APIAs(friend, "POST", "/api/agents", map[string]string{"name": fmt.Sprintf("friend%d", i)}, nil); s != http.StatusCreated {
			t.Fatalf("friend %d: %d", i, s)
		}
	}
	for _, token := range []string{decktest.OwnerToken, friend} {
		body, _ := json.Marshal(map[string]string{"name": "one more"})
		s, out := rawPost(t, h.URL+"/api/agents", token, body, nil)
		if s != http.StatusConflict || out["error"] != "에이전트는 20개까지 만들 수 있어요" {
			t.Fatalf("21st: %d %v", s, out)
		}
	}
}
