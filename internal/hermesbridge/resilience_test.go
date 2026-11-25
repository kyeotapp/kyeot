package hermesbridge_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/hermesbridge"
	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/server"
)

func (f *fakeHermes) push(runID, name string, fields map[string]any) {
	f.mu.Lock()
	ch := f.runs[runID]
	f.mu.Unlock()
	ch <- ev(runID, name, fields)
}

// cutBridge attaches another connection with the same token to drop the bridge connection; the bridge reattaches after 1s.
func cutBridge(h *decktest.Harness, agent decktest.Created) {
	h.StartFakeAgent(agent.Token)
	time.Sleep(300 * time.Millisecond)
}

func TestBridgeBigRoutineResultSentOnceWithoutReconnectLoop(t *testing.T) {
	h := decktest.New(t)
	_, hermesURL := newFakeHermes(t)
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "cron", "output", "job1"), 0o755)
	os.WriteFile(filepath.Join(home, "cron", "jobs.json"), []byte(`{"jobs":[{"id":"job1","name":"big-result","schedule":{"kind":"cron","expr":"0 9 * * 1"}}]}`), 0o644)
	name := time.Now().Add(-time.Minute).Format("2006-01-02_15-04-05") + ".md"
	big := "# Cron Job: big-result\n\n---\n\nhead part " + strings.Repeat("abcdefg ", 200_000) // about 1.6MB
	os.WriteFile(filepath.Join(home, "cron", "output", "job1", name), []byte(big), 0o644)

	// The screen websocket read limit (32KB) is too small to receive a big result, so check the connect count and what was stored
	target, _ := url.Parse(h.URL)
	var dials atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agent" {
			dials.Add(1)
		}
		httputil.NewSingleHostReverseProxy(target).ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	agent := h.CreateAgent("Agent A")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		hermesbridge.New(hermesbridge.Config{DeckURL: proxy.URL, AgentToken: agent.Token, HermesURL: hermesURL, HermesKey: hermesKey,
			HermesHome: home, RunsEvery: 50 * time.Millisecond}).Run(ctx)
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })

	var run protocol.Task
	for i := 0; i < 60 && run.ID == ""; i++ {
		time.Sleep(50 * time.Millisecond)
		var tasks []protocol.Task
		h.API("GET", "/api/tasks?agent="+agent.Agent.ID, nil, &tasks)
		for _, task := range tasks {
			if task.RoutineID == "job1" {
				run = task
			}
		}
	}
	if run.ID == "" {
		t.Fatal("the big routine result was not stored")
	}
	time.Sleep(800 * time.Millisecond)
	if n := dials.Load(); n != 1 {
		t.Fatalf("deck connected %d times (reconnect loop)", n)
	}
	var events []protocol.Event
	h.API("GET", "/api/tasks/"+run.ID+"/events", nil, &events)
	var all []byte
	for _, e := range events {
		all = append(all, e.Payload...)
	}
	if !bytes.Contains(all, []byte("head part")) || !bytes.Contains(all, []byte("(잘림)")) || len(all) > 1<<20 {
		t.Fatalf("events: %d bytes", len(all))
	}
}

func TestBridgeDeliversReplyFinishedWhileDisconnected(t *testing.T) {
	h := decktest.New(t)
	hermes, hermesURL := newFakeHermes(t)
	c := h.ConnectClient()
	agent := startBridge(t, h, hermesURL, hermesKey)
	h.WaitFor(c, "idle", decktest.AgentState(agent.Agent.ID, "idle"))

	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": agent.Agent.ID, "text": "write long"}, &task)
	h.WaitFor(c, "draft", func(m protocol.ClientMessage) bool { return m.Type == "draft" && m.TaskID == task.ID })

	cutBridge(h, agent)
	hermes.push("run_1", "run.completed", map[string]any{"output": "reply finished while disconnected"})
	h.WaitFor(c, "reply after reconnect", eventWith(task.ID, "message", "reply finished while disconnected"))
	h.WaitFor(c, "task done", decktest.TaskState(task.ID, "done"))
}

func TestBridgeResendsApprovalAskedWhileDisconnected(t *testing.T) {
	h := decktest.New(t)
	hermes, hermesURL := newFakeHermes(t)
	c := h.ConnectClient()
	agent := startBridge(t, h, hermesURL, hermesKey)
	h.WaitFor(c, "idle", decktest.AgentState(agent.Agent.ID, "idle"))

	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": agent.Agent.ID, "text": "write long"}, &task)
	h.WaitFor(c, "draft", func(m protocol.ClientMessage) bool { return m.Type == "draft" && m.TaskID == task.ID })

	cutBridge(h, agent)
	hermes.push("run_1", "approval.request", map[string]any{"command": "rm -rf ~/x", "description": "delete files", "choices": []string{"once", "deny"}, "request_id": "req-9"})
	h.WaitFor(c, "approval after reconnect", eventWith(task.ID, "approval", "rm -rf ~/x"))
	h.WaitFor(c, "back online", decktest.AgentState(agent.Agent.ID, "blocked"))

	var pending []protocol.Event
	h.API("GET", "/api/approvals", nil, &pending)
	if len(pending) != 1 {
		t.Fatalf("pending: %d", len(pending))
	}
	var ap struct{ ID string }
	json.Unmarshal(pending[0].Payload, &ap)
	h.API("POST", "/api/tasks/"+task.ID+"/approvals/"+ap.ID, map[string]string{"choice": "한 번 허용"}, nil)
	h.WaitFor(c, "answered", eventWith(task.ID, "message", "Tidied up"))
}

func TestBridgeReplacedRunDoesNotEndTask(t *testing.T) {
	h := decktest.New(t)
	hermes, hermesURL := newFakeHermes(t)
	c := h.ConnectClient()
	agent := startBridge(t, h, hermesURL, hermesKey)
	h.WaitFor(c, "idle", decktest.AgentState(agent.Agent.ID, "idle"))

	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": agent.Agent.ID, "text": "write long"}, &task)
	h.WaitFor(c, "draft", func(m protocol.ClientMessage) bool { return m.Type == "draft" && m.TaskID == task.ID })
	h.API("POST", "/api/tasks/"+task.ID+"/messages", map[string]string{"text": "continue long"}, nil) // steer 409 → run_2
	for i := 0; i < 100; i++ {
		if s, _, _ := hermes.snapshot(); len(s) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	hermes.push("run_1", "run.completed", map[string]any{"output": "first run reply"})
	h.WaitFor(c, "old reply kept", eventWith(task.ID, "message", "first run reply"))
	h.Quiet(c, "old run ending task", 400*time.Millisecond, func(m protocol.ClientMessage) bool {
		return m.Type == "task" && m.Task.ID == task.ID && protocol.TaskFinished(m.Task.State)
	})
	hermes.push("run_2", "run.completed", map[string]any{"output": "new run reply"})
	h.WaitFor(c, "task done by new run", decktest.TaskState(task.ID, "done"))
}

func TestBridgeUsesSubagentIDAndStatus(t *testing.T) {
	h := decktest.New(t)
	_, hermesURL := newFakeHermes(t)
	c := h.ConnectClient()
	agent := startBridge(t, h, hermesURL, hermesKey)
	h.WaitFor(c, "idle", decktest.AgentState(agent.Agent.ID, "idle"))

	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": agent.Agent.ID, "text": "look it up with a subagent"}, &task)
	h.WaitFor(c, "failed subagent", func(m protocol.ClientMessage) bool {
		return eventWith(task.ID, "tool", "sub-sa1")(m) && bytes.Contains(m.Event.Payload, []byte(`"completed"`)) &&
			bytes.Contains(m.Event.Payload, []byte(`"ok":false`)) && bytes.Contains(m.Event.Payload, []byte("blocked"))
	})
}

func TestBridgeAnswersServerPingDuringSlowStart(t *testing.T) {
	h := decktest.NewWith(t, func(o *server.Options) { o.PingInterval = 100 * time.Millisecond })
	_, hermesURL := newFakeHermes(t)
	c := h.ConnectClient()
	agent := startBridge(t, h, hermesURL, hermesKey)
	h.WaitFor(c, "idle", decktest.AgentState(agent.Agent.ID, "idle"))

	if s := h.API("POST", "/api/tasks", map[string]string{"agentId": agent.Agent.ID, "text": "slow start please"}, nil); s != http.StatusCreated {
		t.Fatalf("start: %d", s)
	}
	h.Quiet(c, "disconnect while starting", time.Second, decktest.AgentState(agent.Agent.ID, "offline"))
}
