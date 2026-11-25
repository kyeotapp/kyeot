package server_test

import (
	"net/http"
	"testing"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/server"
)

func TestUploadTotalQuotaCountsSentFiles(t *testing.T) {
	h := decktest.NewWith(t, func(o *server.Options) { o.UploadQuota, o.UploadTotalQuota = 1000, 10 })
	a := h.CreateAgent("Agent A")
	h.StartFakeAgent(a.Token)
	waitState(t, h.URL, a.Agent.ID, protocol.AgentIdle)
	var up protocol.Attachment
	if s := h.Post("/api/uploads?name=a", "text/plain", []byte("123456"), &up); s != http.StatusCreated {
		t.Fatalf("first: %d", s)
	}
	var task protocol.Task
	if s := h.API("POST", "/api/tasks", map[string]any{"agentId": a.Agent.ID, "text": "look", "attachments": []string{up.ID}}, &task); s != http.StatusCreated {
		t.Fatalf("attach: %d", s)
	}
	if s := h.Post("/api/uploads?name=b", "text/plain", []byte("12345"), nil); s != http.StatusInsufficientStorage {
		t.Fatalf("attachments on messages must count toward the total: %d", s)
	}
	if s := h.Post("/api/uploads?name=c", "text/plain", []byte("1234"), nil); s != http.StatusCreated {
		t.Fatalf("fits: %d", s)
	}
}
