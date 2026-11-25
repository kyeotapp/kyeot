package server_test

import (
	"net/http"
	"testing"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

// A task conversation also rejects an attachment already attached to another message with 400, as room messages do, and leaves no message or task.
func TestTaskRejectsReusedUpload(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	h.StartFakeAgent(a.Token)
	waitState(t, h.URL, a.Agent.ID, protocol.AgentIdle)
	var up, fresh protocol.Attachment
	h.Post("/api/uploads?name=a.png", "image/png", []byte("PNGDATA"), &up)
	h.Post("/api/uploads?name=b.png", "image/png", []byte("PNGDATA"), &fresh)
	var task protocol.Task
	if s := h.API("POST", "/api/tasks", map[string]any{"agentId": a.Agent.ID, "text": "Message A", "attachments": []string{up.ID}}, &task); s != http.StatusCreated {
		t.Fatalf("start: %d", s)
	}
	events := func() int {
		var ev []protocol.Event
		h.API("GET", "/api/tasks/"+task.ID+"/events", nil, &ev)
		n := 0
		for _, e := range ev {
			if e.Source == "user" { // fake agent replies may arrive late, so count only people messages
				n++
			}
		}
		return n
	}
	tasks := func() int {
		var ts []protocol.Task
		h.API("GET", "/api/tasks?agent="+a.Agent.ID, nil, &ts)
		return len(ts)
	}
	beforeEvents, beforeTasks := events(), tasks()

	var body struct{ Error string }
	if s := h.API("POST", "/api/tasks/"+task.ID+"/messages", map[string]any{"text": "Message B", "attachments": []string{fresh.ID, up.ID}}, &body); s != http.StatusBadRequest {
		t.Fatalf("reuse in message: %d", s)
	}
	if body.Error != "attachment already used in another message" {
		t.Fatalf("reuse message body: %v", body)
	}
	if s := h.API("POST", "/api/tasks", map[string]any{"agentId": a.Agent.ID, "text": "New task", "attachments": []string{up.ID}}, nil); s != http.StatusBadRequest {
		t.Fatalf("reuse in new task: %d", s)
	}
	if s := h.API("POST", "/api/tasks/"+task.ID+"/messages", map[string]any{"text": "Twice", "attachments": []string{fresh.ID, fresh.ID}}, nil); s != http.StatusBadRequest {
		t.Fatalf("same upload twice in one message: %d", s)
	}
	if n := events(); n != beforeEvents {
		t.Fatalf("rejected message was stored: %d → %d events", beforeEvents, n)
	}
	if n := tasks(); n != beforeTasks {
		t.Fatalf("rejected task was created: %d → %d tasks", beforeTasks, n)
	}
	if s := h.API("POST", "/api/tasks/"+task.ID+"/messages", map[string]any{"text": "Message C", "attachments": []string{fresh.ID}}, nil); s != http.StatusNoContent {
		t.Fatalf("fresh upload after a rejected batch must still attach: %d", s)
	}
}
