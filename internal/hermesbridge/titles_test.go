package hermesbridge_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/hermesbridge"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

// stateDB builds a folder with only the columns of Hermes state.db sessions that are used for titles.
func stateDB(t *testing.T) (home string, put func(session, title string, started time.Time)) {
	t.Helper()
	home = t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE sessions (id TEXT PRIMARY KEY, source TEXT, title TEXT, title_source TEXT, started_at REAL NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return home, func(session, title string, started time.Time) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO sessions (id, source, title, title_source, started_at) VALUES (?, 'api_server', ?, 'llm', ?)
			ON CONFLICT(id) DO UPDATE SET title = excluded.title`, session, title, float64(started.Unix())); err != nil {
			t.Fatal(err)
		}
	}
}

func titled(taskID, want string) func(protocol.ClientMessage) bool {
	return func(m protocol.ClientMessage) bool {
		return m.Type == "task" && m.Task.ID == taskID && m.Task.Title == want
	}
}

func TestBridgeSendsHermesTitleAfterRun(t *testing.T) {
	h := decktest.New(t)
	_, hermesURL := newFakeHermes(t)
	home, put := stateDB(t)
	c := h.ConnectClient()
	agent := startBridgeWith(t, h, hermesbridge.Config{HermesURL: hermesURL, HermesKey: hermesKey, HermesHome: home,
		TitleDelays: []time.Duration{50 * time.Millisecond, 300 * time.Millisecond, time.Second}})
	h.WaitFor(c, "idle", decktest.AgentState(agent.Agent.ID, "idle"))

	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": agent.Agent.ID, "text": "Tidy up the downloads folder"}, &task)
	h.WaitFor(c, "blocked", decktest.AgentState(agent.Agent.ID, "blocked"))
	var pending []protocol.Event
	h.API("GET", "/api/approvals", nil, &pending)
	h.API("POST", "/api/tasks/"+task.ID+"/approvals/ap-1", map[string]string{"choice": "한 번 허용"}, nil)
	h.WaitFor(c, "done", decktest.TaskState(task.ID, "done"))

	put("deck-"+task.ID, "Downloads folder\ntidy", time.Now()) // Hermes names the conversation after the reply
	h.WaitFor(c, "hermes title", titled(task.ID, "Downloads folder tidy"))
}

func TestBridgeSendsRecentTitlesOnConnect(t *testing.T) {
	h := decktest.New(t)
	_, hermesURL := newFakeHermes(t)
	home, put := stateDB(t)
	c := h.ConnectClient()
	agent := h.CreateAgent("Agent A")
	opener := h.StartFakeAgent(agent.Token)
	opener.Send(context.Background(), protocol.AgentMessage{Type: "task.open", Task: "old-1", Title: "first message as is"})
	opener.Send(context.Background(), protocol.AgentMessage{Type: "task.open", Task: "old-2", Title: "old conversation"})
	h.WaitFor(c, "opened", titled("old-2", "old conversation"))
	opener.Close()
	h.WaitFor(c, "offline", decktest.AgentState(agent.Agent.ID, "offline"))

	put("deck-old-1", "title by Agent A", time.Now().Add(-48*time.Hour))
	put("deck-old-2", "title over a month old", time.Now().AddDate(0, 0, -40))
	put("deck-not-in-deck", "conversation the deck does not know", time.Now())
	put("discord-1", "not a deck conversation", time.Now())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		hermesbridge.New(hermesbridge.Config{DeckURL: h.URL, AgentToken: agent.Token, HermesURL: hermesURL, HermesKey: hermesKey, HermesHome: home}).Run(ctx)
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	h.WaitFor(c, "collected title", titled("old-1", "title by Agent A"))

	var tasks []protocol.Task
	h.API("GET", "/api/tasks", nil, &tasks)
	for _, x := range tasks {
		if x.ID == "old-2" && x.Title != "old conversation" {
			t.Fatalf("session older than 30 days was sent: %q", x.Title)
		}
	}
}

// fakeDeck is a deck server that records every message the bridge sends.
func fakeDeck(t *testing.T) (url string, got chan protocol.AgentMessage, say func(protocol.ServerMessage)) {
	got = make(chan protocol.AgentMessage, 256)
	conns := make(chan *websocket.Conn, 4)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conns <- conn
		for {
			var m protocol.AgentMessage
			if wsjson.Read(r.Context(), conn, &m) != nil {
				return
			}
			got <- m
		}
	}))
	t.Cleanup(ts.Close)
	var conn *websocket.Conn
	return ts.URL, got, func(m protocol.ServerMessage) {
		if conn == nil {
			conn = <-conns
		}
		wsjson.Write(context.Background(), conn, m)
	}
}

func TestBridgeDoesNotRepeatSameTitle(t *testing.T) {
	_, hermesURL := newFakeHermes(t)
	home, put := stateDB(t)
	put("deck-t1", "same title", time.Now())
	deckURL, got, say := fakeDeck(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		hermesbridge.New(hermesbridge.Config{DeckURL: deckURL, AgentToken: "x", HermesURL: hermesURL, HermesKey: hermesKey, HermesHome: home,
			TitleDelays: []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond}}).Run(ctx)
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })

	titles := 0
	wait := func(what string, pred func(protocol.AgentMessage) bool) {
		t.Helper()
		timeout := time.After(decktest.WaitLimit)
		for {
			select {
			case m := <-got:
				if m.Type == "task.title" {
					titles++
				}
				if pred(m) {
					return
				}
			case <-timeout:
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	wait("connect title", func(m protocol.AgentMessage) bool {
		return m.Type == "task.title" && m.Task == "t1" && m.Title == "same title"
	})
	say(protocol.ServerMessage{Type: "task.start", Task: "t1", Text: "tidy up"})
	var approvalID string
	wait("approval", func(m protocol.AgentMessage) bool { approvalID = m.ID; return m.Type == "approval" })
	say(protocol.ServerMessage{Type: "approval_result", Task: "t1", ID: approvalID, Choice: "한 번 허용"})
	wait("task end", func(m protocol.AgentMessage) bool { return m.Type == "task.end" })
	time.Sleep(200 * time.Millisecond) // all three re-reads pass
	for len(got) > 0 {
		if m := <-got; m.Type == "task.title" {
			titles++
		}
	}
	if titles != 1 {
		t.Fatalf("same title sent %d times", titles)
	}
}
