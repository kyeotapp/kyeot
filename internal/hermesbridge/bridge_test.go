package hermesbridge_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/hermesbridge"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

const hermesKey = "fake-hermes-key-0123456789"

// fakeHermes imitates /v1/runs of the Hermes api_server.
// The first input runs one tool, asks for approval and completes once approved; later inputs complete right away.
type fakeHermes struct {
	mu        sync.Mutex
	runs      map[string]chan string
	sessions  []string
	inputs    []string
	instructs []string
	approvals []map[string]string
	stops     []string
	clarifies []map[string]any
	n         int
	expired   bool // the approval already expired on the Hermes side
}

func newFakeHermes(t *testing.T) (*fakeHermes, string) {
	f := &fakeHermes{runs: map[string]chan string{}}
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+hermesKey {
				http.Error(w, `{"error":"bad key"}`, http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("POST /v1/runs", auth(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.n++
		id := fmt.Sprintf("run_%d", f.n)
		ch := make(chan string, 16)
		f.runs[id] = ch
		f.sessions = append(f.sessions, body["session_id"])
		f.inputs = append(f.inputs, body["input"])
		f.instructs = append(f.instructs, body["instructions"])
		first := f.n == 1
		f.mu.Unlock()
		if strings.Contains(body["input"], "slow start") { // blocking the read loop prevents answering the server ping
			time.Sleep(600 * time.Millisecond)
		}
		if strings.Contains(body["input"], "subagent") {
			ch <- ev(id, "subagent.start", map[string]any{"subagent_id": "sa1", "goal": "research"})
			ch <- ev(id, "subagent.complete", map[string]any{"subagent_id": "sa1", "status": "failed", "summary": "blocked"})
			ch <- ev(id, "run.completed", map[string]any{"output": "done"})
		} else if strings.Contains(body["input"], "slow tool") { // Hermes sends duration as 0
			go func() {
				ch <- ev(id, "tool.started", map[string]any{"tool": "terminal", "preview": "find ~", "tool_call_id": "slow1"})
				time.Sleep(1200 * time.Millisecond)
				ch <- ev(id, "tool.completed", map[string]any{"tool": "terminal", "duration": 0, "error": false, "tool_call_id": "slow1"})
				ch <- ev(id, "run.completed", map[string]any{"output": "done"})
			}()
		} else if strings.Contains(body["input"], "failing tool") { // preview is the first 500 chars, so a long output cuts the exit code
			ch <- ev(id, "tool.started", map[string]any{"tool": "terminal", "preview": "sleep 99", "tool_call_id": "f1"})
			ch <- ev(id, "tool.completed", map[string]any{"tool": "terminal", "error": true, "tool_call_id": "f1",
				"preview": `{"output": "[Command timed out after 60s]", "exit_code": 124, "error": null}`})
			ch <- ev(id, "tool.started", map[string]any{"tool": "terminal", "preview": "grep x", "tool_call_id": "f2"})
			ch <- ev(id, "tool.completed", map[string]any{"tool": "terminal", "error": true, "tool_call_id": "f2",
				"preview": `{"output": "` + strings.Repeat("a", 480) + "..."})
			ch <- ev(id, "run.completed", map[string]any{"output": "done"})
		} else if strings.Contains(body["input"], "long") {
			ch <- ev(id, "message.delta", map[string]any{"delta": "half-written reply"})
		} else if strings.Contains(body["input"], "choose") {
			ch <- ev(id, "clarify.request", map[string]any{"question": "Which way should I log in?", "choices": []string{"Kakao", "Naver"}, "request_id": "cq-1", "execution_generation": 7})
		} else if first {
			ch <- ev(id, "run.started", nil)
			ch <- ev(id, "tool.started", map[string]any{"tool": "terminal", "preview": "ls ~/Downloads", "tool_call_id": "tc1"})
			ch <- ev(id, "tool.completed", map[string]any{"tool": "terminal", "duration": 0.42, "error": false, "tool_call_id": "tc1"})
			ch <- ev(id, "message.delta", map[string]any{"delta": "Looked at the list.\n\nI will delete the old folder."})
			ch <- ev(id, "approval.request", map[string]any{"command": "rm -rf ~/Downloads/old", "description": "delete files",
				"pattern_key": "recursive delete", "choices": []string{"once", "session", "always", "deny"}, "request_id": "req-1"})
		} else {
			ch <- ev(id, "message.delta", map[string]any{"delta": "Continuing "})
			ch <- ev(id, "message.delta", map[string]any{"delta": "replying"})
			ch <- ev(id, "run.completed", map[string]any{"output": ""})
		}
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"run_id": id, "status": "queued"})
	}))
	mux.HandleFunc("GET /v1/runs/{id}/events", auth(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		ch := f.runs[r.PathValue("id")]
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, ": ok\n\n")
		w.(http.Flusher).Flush()
		for seq := 0; ; seq++ {
			select {
			case data, open := <-ch:
				if !open {
					return
				}
				fmt.Fprintf(w, "id: %d\ndata: %s\n\n", seq, data)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	}))
	mux.HandleFunc("POST /v1/runs/{id}/approval", auth(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		id := r.PathValue("id")
		f.mu.Lock()
		f.approvals = append(f.approvals, body)
		ch := f.runs[id]
		expired := f.expired
		f.mu.Unlock()
		if expired {
			http.Error(w, `{"error":{"message":"Run has no pending approval","code":"approval_not_pending"}}`, http.StatusConflict)
			return
		}
		if body["choice"] == "deny" {
			ch <- ev(id, "run.completed", map[string]any{"output": "Did not delete"})
		} else {
			ch <- ev(id, "run.completed", map[string]any{"output": "Tidied up"})
		}
		json.NewEncoder(w).Encode(map[string]string{"run_id": id, "choice": body["choice"]})
	}))
	mux.HandleFunc("POST /v1/runs/{id}/clarify", auth(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		id := r.PathValue("id")
		f.mu.Lock()
		f.clarifies = append(f.clarifies, body)
		ch := f.runs[id]
		f.mu.Unlock()
		ch <- ev(id, "run.completed", map[string]any{"output": fmt.Sprint(body["answer"], " it is")})
		json.NewEncoder(w).Encode(map[string]string{"run_id": id})
	}))
	mux.HandleFunc("POST /v1/runs/{id}/steer", auth(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"not running"}`, http.StatusConflict)
	}))
	mux.HandleFunc("POST /v1/runs/{id}/stop", auth(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		f.mu.Lock()
		f.stops = append(f.stops, id)
		ch := f.runs[id]
		f.mu.Unlock()
		ch <- ev(id, "run.cancelled", map[string]any{"interrupted": true})
		json.NewEncoder(w).Encode(map[string]string{"run_id": id, "status": "stopping"})
	}))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return f, ts.URL
}

func ev(runID, name string, fields map[string]any) string {
	m := map[string]any{"event": name, "run_id": runID, "timestamp": 1.0}
	for k, v := range fields {
		m[k] = v
	}
	raw, _ := json.Marshal(m)
	return string(raw)
}

func (f *fakeHermes) snapshot() (sessions []string, approvals []map[string]string, stops []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sessions...), append([]map[string]string(nil), f.approvals...), append([]string(nil), f.stops...)
}

func startBridge(t *testing.T, h *decktest.Harness, hermesURL, key string) decktest.Created {
	return startBridgeWith(t, h, hermesbridge.Config{HermesURL: hermesURL, HermesKey: key})
}

func startBridgeWith(t *testing.T, h *decktest.Harness, cfg hermesbridge.Config) decktest.Created {
	agent := h.CreateAgent("Agent A")
	cfg.DeckURL, cfg.AgentToken = h.URL, agent.Token
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		hermesbridge.New(cfg).Run(ctx)
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	return agent
}

func eventWith(taskID, typ, needle string) func(protocol.ClientMessage) bool {
	return func(m protocol.ClientMessage) bool {
		return m.Type == "event" && m.Event.TaskID == taskID && m.Event.Type == typ && bytes.Contains(m.Event.Payload, []byte(needle))
	}
}

func TestBridgeApprovalAndFollowUp(t *testing.T) {
	h := decktest.New(t)
	hermes, hermesURL := newFakeHermes(t)
	c := h.ConnectClient()
	agent := startBridge(t, h, hermesURL, hermesKey)
	h.WaitFor(c, "bridge idle", decktest.AgentState(agent.Agent.ID, "idle"))

	var task protocol.Task
	if s := h.API("POST", "/api/tasks", map[string]string{"agentId": agent.Agent.ID, "text": "Tidy up the downloads folder"}, &task); s != http.StatusCreated {
		t.Fatalf("start: %d", s)
	}
	h.WaitFor(c, "tool started", eventWith(task.ID, "tool", "ls ~/Downloads"))
	h.WaitFor(c, "agent blocked", decktest.AgentState(agent.Agent.ID, "blocked"))

	var pending []protocol.Event
	h.API("GET", "/api/approvals", nil, &pending)
	if len(pending) != 1 {
		t.Fatalf("pending: %d", len(pending))
	}
	var ap struct {
		ID       string            `json:"id"`
		Question string            `json:"question"`
		Options  []string          `json:"options"`
		Context  string            `json:"context"`
		Request  string            `json:"request"`
		Impact   []string          `json:"impact"`
		Scopes   map[string]string `json:"scopes"`
	}
	json.Unmarshal(pending[0].Payload, &ap)
	if got := strings.Join(ap.Options, ","); got != "한 번 허용,이번 세션 허용,항상 허용,거절" {
		t.Fatalf("options: %s", got)
	}
	if ap.Context != "I will delete the old folder." || ap.Request != "Tidy up the downloads folder" {
		t.Fatalf("context: %q request: %q", ap.Context, ap.Request)
	}
	if len(ap.Impact) == 0 || ap.Impact[0] != "종류: 폴더째 지우기 (recursive delete)" {
		t.Fatalf("impact: %q", ap.Impact)
	}
	if ap.Scopes["항상 허용"] == "" || ap.Scopes["이번 세션 허용"] == "" || ap.Scopes["거절"] != "" {
		t.Fatalf("scopes: %v", ap.Scopes)
	}
	if !strings.Contains(ap.Question, "rm -rf ~/Downloads/old") {
		t.Fatalf("question: %q", ap.Question)
	}

	h.API("POST", "/api/tasks/"+task.ID+"/approvals/"+ap.ID, map[string]string{"choice": "한 번 허용"}, nil)
	h.WaitFor(c, "final reply", eventWith(task.ID, "message", "Tidied up"))
	h.WaitFor(c, "task done", decktest.TaskState(task.ID, "done"))
	h.WaitFor(c, "bridge idle again", decktest.AgentState(agent.Agent.ID, "idle"))

	_, approvals, _ := hermes.snapshot()
	if len(approvals) != 1 || approvals[0]["choice"] != "once" || approvals[0]["request_id"] != "req-1" {
		t.Fatalf("hermes approval: %v", approvals)
	}

	h.API("POST", "/api/tasks/"+task.ID+"/messages", map[string]string{"text": "continue"}, nil)
	h.WaitFor(c, "follow-up reply", eventWith(task.ID, "message", "Continuing replying"))
	h.WaitFor(c, "done again", decktest.TaskState(task.ID, "done"))

	sessions, _, _ := hermes.snapshot()
	want := "deck-" + task.ID
	if len(sessions) != 2 || sessions[0] != want || sessions[1] != want {
		t.Fatalf("sessions: %v (want both %s)", sessions, want)
	}
}

func TestBridgeCancelStopsHermesRun(t *testing.T) {
	h := decktest.New(t)
	hermes, hermesURL := newFakeHermes(t)
	c := h.ConnectClient()
	agent := startBridge(t, h, hermesURL, hermesKey)
	h.WaitFor(c, "idle", decktest.AgentState(agent.Agent.ID, "idle"))

	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": agent.Agent.ID, "text": "job to cancel"}, &task)
	h.WaitFor(c, "blocked", decktest.AgentState(agent.Agent.ID, "blocked"))
	h.API("POST", "/api/tasks/"+task.ID+"/cancel", nil, nil)
	h.WaitFor(c, "idle after cancel", decktest.AgentState(agent.Agent.ID, "idle"))

	if _, _, stops := hermes.snapshot(); len(stops) != 1 || stops[0] != "run_1" {
		t.Fatalf("stops: %v", stops)
	}
}

func TestBridgeCancelKeepsPartialReply(t *testing.T) {
	h := decktest.New(t)
	_, hermesURL := newFakeHermes(t)
	c := h.ConnectClient()
	agent := startBridge(t, h, hermesURL, hermesKey)
	h.WaitFor(c, "idle", decktest.AgentState(agent.Agent.ID, "idle"))

	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": agent.Agent.ID, "text": "write long"}, &task)
	h.WaitFor(c, "draft", func(m protocol.ClientMessage) bool { return m.Type == "draft" && m.TaskID == task.ID })
	h.API("POST", "/api/tasks/"+task.ID+"/cancel", nil, nil)
	h.WaitFor(c, "task cancelled", decktest.TaskState(task.ID, "cancelled"))
	h.WaitFor(c, "partial reply", eventWith(task.ID, "message", "half-written reply"))
}

func TestBridgeMeasuresToolDuration(t *testing.T) {
	h := decktest.New(t)
	_, hermesURL := newFakeHermes(t)
	c := h.ConnectClient()
	agent := startBridge(t, h, hermesURL, hermesKey)
	h.WaitFor(c, "idle", decktest.AgentState(agent.Agent.ID, "idle"))

	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": agent.Agent.ID, "text": "use slow tool"}, &task)
	h.WaitFor(c, "measured duration", func(m protocol.ClientMessage) bool {
		return eventWith(task.ID, "tool", "completed")(m) && !bytes.Contains(m.Event.Payload, []byte(`"0.`)) && bytes.Contains(m.Event.Payload, []byte("초"))
	})
}

func TestBridgeReportsHermesFailure(t *testing.T) {
	h := decktest.New(t)
	_, hermesURL := newFakeHermes(t)
	c := h.ConnectClient()
	agent := startBridge(t, h, hermesURL, "wrong-key-0123456789")
	h.WaitFor(c, "idle", decktest.AgentState(agent.Agent.ID, "idle"))

	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": agent.Agent.ID, "text": "any job"}, &task)
	h.WaitFor(c, "failure message", eventWith(task.ID, "message", "hermes 401"))
	h.WaitFor(c, "task failed", decktest.TaskState(task.ID, "failed"))
}

func TestBridgeReconnects(t *testing.T) {
	h := decktest.New(t)
	_, hermesURL := newFakeHermes(t)
	c := h.ConnectClient()
	agent := startBridge(t, h, hermesURL, hermesKey)
	h.WaitFor(c, "idle", decktest.AgentState(agent.Agent.ID, "idle"))

	// When another connection attaches with the same token, the server drops the bridge connection; the bridge must reattach.
	intruder := h.StartFakeAgent(agent.Token)
	intruder.Close()
	h.WaitFor(c, "offline", decktest.AgentState(agent.Agent.ID, "offline"))
	h.WaitFor(c, "bridge back", decktest.AgentState(agent.Agent.ID, "idle"))
}

func TestBridgeForwardsRoutinesAndReport(t *testing.T) {
	h := decktest.New(t)
	hermes, hermesURL := newFakeHermes(t)
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "cron", "output", "job1"), 0o755)
	os.WriteFile(filepath.Join(home, "cron", "jobs.json"), []byte(`{"jobs":[{"id":"job1","name":"contest-alert","schedule":{"kind":"cron","expr":"0 9 * * 1"}}]}`), 0o644)
	write := func(at time.Time, text string) {
		name := at.Format("2006-01-02_15-04-05") + ".md"
		os.WriteFile(filepath.Join(home, "cron", "output", "job1", name), []byte("# Cron Job: contest-alert\n\n---\n\n"+text), 0o644)
	}
	write(time.Now().Add(-time.Hour), "past result")

	c := h.ConnectClient()
	agent := startBridgeWith(t, h, hermesbridge.Config{HermesURL: hermesURL, HermesKey: hermesKey, HermesHome: home, RunsEvery: 100 * time.Millisecond})
	h.WaitFor(c, "report", func(m protocol.ClientMessage) bool {
		return m.Type == "report" && m.AgentID == agent.Agent.ID && bytes.Contains(m.Report, []byte("매주 월 09:00"))
	})
	h.WaitFor(c, "past run", func(m protocol.ClientMessage) bool {
		return m.Type == "event" && bytes.Contains(m.Event.Payload, []byte("past result"))
	})

	write(time.Now(), "new result")
	var runTask string
	h.WaitFor(c, "new run", func(m protocol.ClientMessage) bool {
		if m.Type == "event" && bytes.Contains(m.Event.Payload, []byte("new result")) {
			runTask = m.Event.TaskID
			return true
		}
		return false
	})

	// When a conversation creates a routine, report right away without waiting for the 5-minute cycle
	time.Sleep(20 * time.Millisecond)
	os.WriteFile(filepath.Join(home, "cron", "jobs.json"), []byte(`{"jobs":[{"id":"job1","name":"contest-alert","schedule":{"kind":"cron","expr":"0 9 * * 1"}},{"id":"job2","name":"new-routine","schedule":{"kind":"cron","expr":"0 8 * * *"}}]}`), 0o644)
	h.WaitFor(c, "report after jobs.json change", func(m protocol.ClientMessage) bool {
		return m.Type == "report" && bytes.Contains(m.Report, []byte("new-routine"))
	})

	h.API("POST", "/api/tasks/"+runTask+"/messages", map[string]string{"text": "details"}, nil)
	h.WaitFor(c, "reply reached hermes", decktest.TaskState(runTask, "waiting")) // the fake Hermes asks for approval on the first run
	hermes.mu.Lock()
	input := hermes.inputs[len(hermes.inputs)-1]
	hermes.mu.Unlock()
	if !strings.Contains(input, "new result") || !strings.Contains(input, "---\n\ndetails\n\n[곁 앱 안내") {
		t.Fatalf("hermes input should carry the routine output: %q", input)
	}

	var tasks []protocol.Task
	h.API("GET", "/api/tasks?agent="+agent.Agent.ID, nil, &tasks)
	runs := 0
	for _, task := range tasks {
		if task.RoutineID == "job1" {
			runs++
		}
	}
	if runs != 2 {
		t.Fatalf("routine runs stored: %d (a resend must not duplicate)", runs)
	}
}

func TestBridgeTurnsClarifyIntoChoice(t *testing.T) {
	h := decktest.New(t)
	hermes, hermesURL := newFakeHermes(t)
	c := h.ConnectClient()
	agent := startBridge(t, h, hermesURL, hermesKey)
	h.WaitFor(c, "idle", decktest.AgentState(agent.Agent.ID, "idle"))

	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": agent.Agent.ID, "text": "choose the login method"}, &task)
	h.WaitFor(c, "choice", func(m protocol.ClientMessage) bool {
		return m.Type == "event" && m.Event.Type == "approval" && bytes.Contains(m.Event.Payload, []byte(`"kind":"choice"`))
	})
	var pending []protocol.Event
	h.API("GET", "/api/approvals", nil, &pending)
	var ap struct{ ID string }
	json.Unmarshal(pending[0].Payload, &ap)

	h.API("POST", "/api/tasks/"+task.ID+"/approvals/"+ap.ID, map[string]string{"choice": "Naver"}, nil)
	h.WaitFor(c, "answer reflected", eventWith(task.ID, "message", "Naver it is"))

	hermes.mu.Lock()
	got := hermes.clarifies
	hermes.mu.Unlock()
	if len(got) != 1 || got[0]["answer"] != "Naver" || got[0]["request_id"] != "cq-1" || got[0]["execution_generation"] != float64(7) {
		t.Fatalf("clarify body: %v", got)
	}
}

func TestBridgeHandsAttachmentsToHermes(t *testing.T) {
	h := decktest.New(t)
	hermes, hermesURL := newFakeHermes(t)
	home := t.TempDir()
	c := h.ConnectClient()
	agent := startBridgeWith(t, h, hermesbridge.Config{HermesURL: hermesURL, HermesKey: hermesKey, HermesHome: home})
	h.WaitFor(c, "bridge idle", decktest.AgentState(agent.Agent.ID, "idle"))

	var up protocol.Attachment
	if s := h.Post("/api/uploads?name=영수증.pdf", "application/pdf", []byte("%PDF-1"), &up); s != http.StatusCreated {
		t.Fatalf("upload: %d", s)
	}
	if s := h.API("POST", "/api/tasks", map[string]any{"agentId": agent.Agent.ID, "text": "tidy up", "attachments": []string{up.ID}}, nil); s != http.StatusCreated {
		t.Fatalf("start: %d", s)
	}
	var input string
	for i := 0; i < 100 && input == ""; i++ {
		hermes.mu.Lock()
		if len(hermes.inputs) > 0 {
			input = hermes.inputs[0]
		}
		hermes.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	path := filepath.Join(home, "kyeot-uploads", up.ID[:8]+"-영수증.pdf")
	if !strings.HasPrefix(input, "tidy up") || !strings.Contains(input, path+" (application/pdf, 6B)") {
		t.Fatalf("hermes input: %q", input)
	}
	if got, _ := os.ReadFile(path); string(got) != "%PDF-1" {
		t.Fatalf("saved file: %q", got)
	}
}

func TestBridgeSendsLanguageWithoutTouchingHermesConfig(t *testing.T) {
	h := decktest.New(t)
	hermes, hermesURL := newFakeHermes(t)
	home := t.TempDir()
	config := filepath.Join(home, "config.yaml")
	orig := []byte("model: x\nauxiliary:\n  title_generation:\n    language: English\n")
	if err := os.WriteFile(config, orig, 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(config)
	c := h.ConnectClient()
	agent := startBridgeWith(t, h, hermesbridge.Config{HermesURL: hermesURL, HermesKey: hermesKey, HermesHome: home})
	h.WaitFor(c, "idle", decktest.AgentState(agent.Agent.ID, "idle"))

	var task protocol.Task
	if s := h.API("POST", "/api/tasks", map[string]string{"agentId": agent.Agent.ID, "text": "안녕", "lang": "ko"}, &task); s != http.StatusCreated {
		t.Fatalf("start: %d", s)
	}
	deadline := time.Now().Add(2 * time.Second)
	var got []string
	for time.Now().Before(deadline) {
		hermes.mu.Lock()
		got = append([]string(nil), hermes.instructs...)
		hermes.mu.Unlock()
		if len(got) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(got) != 1 || !strings.Contains(got[0], "Korean") {
		t.Fatalf("instructions: %q", got)
	}
	raw, _ := os.ReadFile(config)
	after, _ := os.Stat(config)
	if !bytes.Equal(raw, orig) || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("Hermes config.yaml changed: %q", raw)
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 1 {
		t.Fatalf("a file appeared in the Hermes folder: %v", entries)
	}
}

func TestBridgeExplainsToolFailure(t *testing.T) {
	h := decktest.New(t)
	_, hermesURL := newFakeHermes(t)
	home := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE messages (id INTEGER PRIMARY KEY, session_id TEXT, role TEXT, content TEXT, tool_call_id TEXT)`); err != nil {
		t.Fatal(err)
	}
	c := h.ConnectClient()
	agent := startBridgeWith(t, h, hermesbridge.Config{HermesURL: hermesURL, HermesKey: hermesKey, HermesHome: home})
	h.WaitFor(c, "idle", decktest.AgentState(agent.Agent.ID, "idle"))

	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": agent.Agent.ID, "text": "use failing tool"}, &task)
	if _, err := db.Exec(`INSERT INTO messages (session_id, role, content, tool_call_id) VALUES (?, 'tool', ?, 'f2')`, "deck-"+task.ID,
		`{"output": "`+strings.Repeat("a", 900)+`", "exit_code": 1, "error": null, "exit_code_meaning": "No matches found (not an error)"}`); err != nil {
		t.Fatal(err)
	}
	h.WaitFor(c, "timeout from preview", eventWith(task.ID, "tool", "60초 시간 초과"))
	h.WaitFor(c, "exit code from state.db", eventWith(task.ID, "tool", "종료 코드 1: No matches found"))
}

func TestBridgeExplainsExpiredApproval(t *testing.T) {
	h := decktest.New(t)
	hermes, hermesURL := newFakeHermes(t)
	c := h.ConnectClient()
	agent := startBridge(t, h, hermesURL, hermesKey)
	h.WaitFor(c, "bridge idle", decktest.AgentState(agent.Agent.ID, "idle"))

	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": agent.Agent.ID, "text": "Tidy up the downloads folder"}, &task)
	h.WaitFor(c, "agent blocked", decktest.AgentState(agent.Agent.ID, "blocked"))
	var pending []protocol.Event
	h.API("GET", "/api/approvals", nil, &pending)
	var ap struct {
		ID string `json:"id"`
	}
	json.Unmarshal(pending[0].Payload, &ap)
	hermes.mu.Lock()
	hermes.expired = true
	hermes.mu.Unlock()

	h.API("POST", "/api/tasks/"+task.ID+"/approvals/"+ap.ID, map[string]string{"choice": "한 번 허용"}, nil)
	h.WaitFor(c, "expired notice", eventWith(task.ID, "message", "승인 시간이 지나"))
}

func TestBridgeRunRoutineOutlivesMinuteAndReportsFailure(t *testing.T) {
	h := decktest.New(t)
	_, hermesURL := newFakeHermes(t)
	home := t.TempDir()
	out := filepath.Join(home, "cron", "output", "job1")
	os.MkdirAll(out, 0o755)
	os.WriteFile(filepath.Join(home, "cron", "jobs.json"), []byte(`{"jobs":[{"id":"job1","name":"contest-alert","schedule":{"kind":"cron","expr":"0 9 * * 1"}}]}`), 0o644)
	mode := filepath.Join(t.TempDir(), "mode")
	bin := filepath.Join(t.TempDir(), "hermes")
	os.WriteFile(bin, []byte(`#!/bin/sh
[ "$1 $2" = "cron run" ] || exit 0
m=$(cat `+mode+`)
case "$m" in
ok) sleep 2; printf '# Cron Job: contest-alert\n\n---\n\nlong run result text' > "`+out+`/$(date +%Y-%m-%d_%H-%M-%S).md"; exit 0;;
fail) sleep 2; echo "could not find JSON in Haiku response (rc=1)" >&2; exit 1;;
slow) sleep 30;;
esac
`), 0o755)

	c := h.ConnectClient()
	agent := startBridgeWith(t, h, hermesbridge.Config{HermesURL: hermesURL, HermesKey: hermesKey, HermesHome: home, HermesBin: bin,
		RunsEvery: 100 * time.Millisecond, RoutineRunTimeout: 3 * time.Second})
	h.WaitFor(c, "idle", decktest.AgentState(agent.Agent.ID, "idle"))
	trigger := func() {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); ; time.Sleep(100 * time.Millisecond) { // 409 until hello announces routines.run
			code := h.API("POST", "/api/agents/"+agent.Agent.ID+"/routines/job1/run", nil, nil)
			if code < 300 {
				return
			}
			if time.Now().After(end) {
				t.Fatalf("run: %d", code)
			}
		}
	}
	seen := map[string]bool{}
	waitRun := func(what, text, state string) { // the run takes seconds, so watch the API instead of WaitFor(2s)
		t.Helper()
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
			var tasks []protocol.Task
			h.API("GET", "/api/tasks?agent="+agent.Agent.ID, nil, &tasks)
			for _, task := range tasks {
				if task.RoutineID != "job1" || seen[task.ID] {
					continue
				}
				var evs []protocol.Event
				h.API("GET", "/api/tasks/"+task.ID+"/events", nil, &evs)
				for _, e := range evs {
					if bytes.Contains(e.Payload, []byte(text)) {
						seen[task.ID] = true
						if task.State != state {
							t.Fatalf("%s: state %s, want %s", what, task.State, state)
						}
						return
					}
				}
			}
		}
		t.Fatalf("timeout waiting for %s", what)
	}

	// (1) a run longer than a short fixed limit (the old 1 minute) is not killed and its result arrives
	os.WriteFile(mode, []byte("ok"), 0o644)
	trigger()
	waitRun("long run result", "long run result text", "done")

	// (2) a failure is failed with stderr, (3) pressing again while running is rejected
	os.WriteFile(mode, []byte("fail"), 0o644)
	trigger()
	time.Sleep(300 * time.Millisecond)
	trigger()
	waitRun("busy", "이미 돌고 있어요", "failed")
	waitRun("failure", "could not find JSON", "failed")

	// (4) being killed at the time limit is reported as failed
	os.WriteFile(mode, []byte("slow"), 0o644)
	trigger()
	waitRun("timeout", "끝나지 않아 멈췄어요", "failed")
}

// Only a bridge that opened the screen address adds the deck-ui skill note to the first input; a profile without the screen address cannot reach the skill address.
func TestBridgePointsToDeckUISkillOnlyWithUI(t *testing.T) {
	for _, ui := range []string{"", "127.0.0.1:0"} {
		h := decktest.New(t)
		hermes, hermesURL := newFakeHermes(t)
		c := h.ConnectClient()
		agent := startBridgeWith(t, h, hermesbridge.Config{HermesURL: hermesURL, HermesKey: hermesKey, HermesHome: t.TempDir(), UIAddr: ui})
		h.WaitFor(c, "bridge idle", decktest.AgentState(agent.Agent.ID, "idle"))
		if s := h.API("POST", "/api/tasks", map[string]any{"agentId": agent.Agent.ID, "text": "make a tab"}, nil); s != http.StatusCreated {
			t.Fatalf("start: %d", s)
		}
		var input string
		for i := 0; i < 100 && input == ""; i++ {
			hermes.mu.Lock()
			if len(hermes.inputs) > 0 {
				input = hermes.inputs[0]
			}
			hermes.mu.Unlock()
			time.Sleep(20 * time.Millisecond)
		}
		if !strings.Contains(input, "[곁 앱 안내") {
			t.Fatalf("ui %q: no guide: %q", ui, input)
		}
		if got := strings.Contains(input, "deck-ui 스킬"); got != (ui != "") {
			t.Fatalf("ui %q: deck-ui line = %v: %q", ui, got, input)
		}
	}
}
