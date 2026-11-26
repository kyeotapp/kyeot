package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

// An agent naming another owner's upload must not get the server to sign a URL for it.
func TestAgentCannotGetOthersUploadSigned(t *testing.T) {
	h := decktest.New(t)
	var victim protocol.Attachment
	if s := h.Post("/api/uploads?name=secret.png", "image/png", []byte("PNGDATA"), &victim); s != http.StatusCreated {
		t.Fatalf("upload: %d", s)
	}

	tok2 := invite(t, h, "Bob")
	var bot decktest.Created
	if s := h.APIAs(tok2, "POST", "/api/agents", map[string]string{"name": "bobbot"}, &bot); s != http.StatusCreated {
		t.Fatalf("agent: %d", s)
	}
	agent := h.StartFakeAgent(bot.Token)
	waitStateAs(t, h, tok2, bot.Agent.ID)

	own := uploadAs(t, h, tok2, "mine.txt", "mine")
	var task protocol.Task
	if s := h.APIAs(tok2, "POST", "/api/tasks", map[string]any{"agentId": bot.Agent.ID, "text": "hi", "attachments": []string{own.ID}}, &task); s != http.StatusCreated {
		t.Fatalf("start: %d", s)
	}
	agent.Send(context.Background(), map[string]any{
		"type": "message", "task": task.ID, "text": "planted",
		"attachments": []map[string]any{{"id": victim.ID, "name": "x.png", "mime": "image/png", "size": 7}},
	})

	var events []protocol.Event
	for deadline := time.Now().Add(decktest.WaitLimit); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		events = nil
		h.APIAs(tok2, "GET", "/api/tasks/"+task.ID+"/events", nil, &events)
		if strings.Contains(eventsText(events), "planted") {
			break
		}
	}
	if !strings.Contains(eventsText(events), "planted") {
		t.Fatalf("agent message never stored: %s", eventsText(events))
	}
	userSigned := false
	for _, e := range events {
		if strings.Contains(string(e.Payload), victim.ID) {
			t.Fatalf("event carries the other owner's upload: %s %s", e.Source, e.Payload)
		}
		var p struct{ Attachments []protocol.Attachment }
		json.Unmarshal(e.Payload, &p)
		if e.Source == "user" && len(p.Attachments) == 1 && strings.Contains(p.Attachments[0].URL, "sig=") {
			userSigned = true
		}
	}
	if !userSigned {
		t.Fatalf("user attachment not signed: %s", eventsText(events))
	}
}

func eventsText(es []protocol.Event) string {
	var b strings.Builder
	for _, e := range es {
		b.WriteString(e.Source + " " + string(e.Payload) + "\n")
	}
	return b.String()
}
