package server_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

func TestRoomQuestionCarriesRoomContext(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Bot")
	c := h.ConnectClient()
	agent := rawAgent(t, h, a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	var room protocol.Room
	h.API("POST", "/api/rooms", map[string]any{"name": "Camp vacancies", "agentId": a.Agent.ID}, &room)

	var first protocol.Task
	h.API("POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "Tidy up vacancies", "ask": true}, &first)
	m := readUntil(t, agent, "first task.start", func(m protocol.ServerMessage) bool { return m.Type == "task.start" })
	if !strings.Contains(m.Context, "Camp vacancies") {
		t.Fatalf("a room question carries the room name: %q", m.Context)
	}
	agent.Send(context.Background(), protocol.AgentMessage{Type: "message", Task: first.ID, Text: "Unit A3 is still unbooked"})
	agent.Send(context.Background(), protocol.AgentMessage{Type: "task.end", Task: first.ID, Result: "done"})
	h.WaitFor(c, "done", decktest.TaskState(first.ID, "done"))
	h.API("POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "Let's look again tomorrow", "ask": false}, nil)

	h.API("POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "Check unbooked items", "ask": true}, nil)
	m = readUntil(t, agent, "second task.start", func(m protocol.ServerMessage) bool {
		return m.Type == "task.start" && m.Text == "Check unbooked items"
	})
	for _, want := range []string{"Camp vacancies", "Unit A3 is still unbooked", "Let's look again tomorrow"} {
		if !strings.Contains(m.Context, want) {
			t.Fatalf("room background lacks %q: %q", want, m.Context)
		}
	}
	if strings.Contains(m.Context, "Check unbooked items") {
		t.Fatalf("the current question is not put in the background: %q", m.Context)
	}

	h.API("POST", "/api/tasks/"+first.ID+"/messages", map[string]any{"text": "Continue"}, nil)
	m = readUntil(t, agent, "follow-up", func(m protocol.ServerMessage) bool { return m.Type == "user_message" })
	if m.Context != "" {
		t.Fatalf("a follow-up does not re-attach the room background: %q", m.Context)
	}
}

func TestTaskOutsideRoomHasNoContext(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Bot")
	c := h.ConnectClient()
	agent := rawAgent(t, h, a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	h.API("POST", "/api/tasks", map[string]string{"agentId": a.Agent.ID, "text": "Just a question"}, nil)
	m := readUntil(t, agent, "task.start", func(m protocol.ServerMessage) bool { return m.Type == "task.start" })
	if m.Context != "" {
		t.Fatalf("a question outside a room has no background: %q", m.Context)
	}
}

func TestRoomContextDescribesRoutinesAndUsesQuestionText(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Bot")
	c := h.ConnectClient()
	agent := rawAgent(t, h, a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	agent.Send(context.Background(), protocol.AgentMessage{Type: "report", Report: json.RawMessage(`{"routines":[{"id":"sr1","name":"sulakhyu-watch","title":"Camp watch","cron":"*/10 * * * *","script":"~/.hermes/scripts/sulakhyu.py","about":"Finds camp vacancies","lastRun":1763000000000,"lastStatus":"error","deliver":"Discord"}]}`)})
	h.WaitFor(c, "routines", func(m protocol.ClientMessage) bool {
		return m.Type == "agent" && m.Agent.ID == a.Agent.ID && slices.Contains(m.Agent.Features, "routines")
	})
	var room protocol.Room
	h.API("POST", "/api/rooms", map[string]any{"name": "Camp vacancies", "agentId": a.Agent.ID, "routineIds": []string{"sr1"}}, &room)

	var first protocol.Task
	h.API("POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "Check unbooked items", "ask": true}, &first)
	m := readUntil(t, agent, "first task.start", func(m protocol.ServerMessage) bool { return m.Type == "task.start" })
	for _, want := range []string{"이 방은 아래 루틴의 결과를 받는 곳이다", "Camp watch", "10분마다", "sulakhyu.py", "Finds camp vacancies", "error", "Discord", "이 방의 루틴과 위 글을 가리키는 것으로 본다"} {
		if !strings.Contains(m.Context, want) {
			t.Fatalf("routine-linked room background lacks %q: %q", want, m.Context)
		}
	}
	agent.Send(context.Background(), protocol.AgentMessage{Type: "task.title", Task: first.ID, Title: "Fix Camp watch consecutive failures"})
	h.WaitFor(c, "retitled", func(m protocol.ClientMessage) bool {
		return m.Type == "task" && m.Task.ID == first.ID && m.Task.Title == "Fix Camp watch consecutive failures"
	})
	agent.Send(context.Background(), protocol.AgentMessage{Type: "message", Task: first.ID, Text: "Unit B is still pending"})
	agent.Send(context.Background(), protocol.AgentMessage{Type: "task.end", Task: first.ID, Result: "done"})
	h.WaitFor(c, "done", decktest.TaskState(first.ID, "done"))

	var posts []struct {
		Task protocol.Task `json:"task"`
	}
	h.API("GET", "/api/rooms/"+room.ID+"/posts", nil, &posts)
	if len(posts) != 1 || posts[0].Task.Question != "Check unbooked items" || posts[0].Task.Title != "Fix Camp watch consecutive failures" {
		t.Fatalf("room posts carry the original question even if the title changes: %+v", posts)
	}

	h.API("POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "Booked now?", "ask": true}, nil)
	m = readUntil(t, agent, "second task.start", func(m protocol.ServerMessage) bool {
		return m.Type == "task.start" && m.Text == "Booked now?"
	})
	if !strings.Contains(m.Context, "질문 「Check unbooked items」의 답") || strings.Contains(m.Context, "consecutive failures") {
		t.Fatalf("the background's question line is the original, not the title: %q", m.Context)
	}
}

// A room person post with only files goes into the agent background as attachment names instead of an empty line.
func TestRoomContextNamesTalkAttachments(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Bot")
	c := h.ConnectClient()
	agent := rawAgent(t, h, a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	var room protocol.Room
	h.API("POST", "/api/rooms", map[string]any{"name": "Photo room", "agentId": a.Agent.ID}, &room)
	f := uploadAs(t, h, decktest.OwnerToken, "a.png", "png")
	g := uploadAs(t, h, decktest.OwnerToken, "b.pdf", "pdf")
	h.API("POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "", "ask": false, "attachments": []string{f.ID}}, nil)
	h.API("POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "this too", "ask": false, "attachments": []string{g.ID}}, nil)
	h.API("POST", "/api/rooms/"+room.ID+"/messages", map[string]any{"text": "Look at the photo", "ask": true}, nil)
	m := readUntil(t, agent, "task.start", func(m protocol.ServerMessage) bool { return m.Type == "task.start" })
	for _, want := range []string{"me: [첨부: a.png]", "me: this too [첨부: b.pdf]"} {
		if !strings.Contains(m.Context, want) {
			t.Fatalf("background lacks %q: %q", want, m.Context)
		}
	}
}
