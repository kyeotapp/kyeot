package server_test

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/server"
)

// If saving the task or message fails after attaching, the attachment is rolled back so sending the same file again succeeds.
func TestUploadAttachRolledBackOnStoreFailure(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "kyeot.db")
	h := decktest.NewWith(t, func(o *server.Options) { o.DBPath = dbPath })
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	a := h.CreateAgent("Agent A")
	h.StartFakeAgent(a.Token)
	waitState(t, h.URL, a.Agent.ID, protocol.AgentIdle)
	var up protocol.Attachment
	h.Post("/api/uploads?name=a.png", "image/png", []byte("PNGDATA"), &up)
	start := map[string]any{"agentId": a.Agent.ID, "text": "work", "attachments": []string{up.ID}}

	exec(`CREATE TRIGGER fail_task BEFORE INSERT ON tasks BEGIN SELECT RAISE(ABORT, 'injected'); END`)
	if s := h.API("POST", "/api/tasks", start, nil); s < 500 {
		t.Fatalf("injected task failure: %d", s)
	}
	exec(`DROP TRIGGER fail_task`)
	var task protocol.Task
	if s := h.API("POST", "/api/tasks", start, &task); s != http.StatusCreated {
		t.Fatalf("resend after failed task: %d", s)
	}

	var up2 protocol.Attachment
	h.Post("/api/uploads?name=b.png", "image/png", []byte("PNGDATA"), &up2)
	msg := map[string]any{"text": "more", "attachments": []string{up2.ID}}
	exec(`CREATE TRIGGER fail_msg BEFORE INSERT ON events WHEN NEW.type = 'user_message' BEGIN SELECT RAISE(ABORT, 'injected'); END`)
	if s := h.API("POST", "/api/tasks/"+task.ID+"/messages", msg, nil); s < 500 {
		t.Fatalf("injected message failure: %d", s)
	}
	exec(`DROP TRIGGER fail_msg`)
	if s := h.API("POST", "/api/tasks/"+task.ID+"/messages", msg, nil); s != http.StatusNoContent {
		t.Fatalf("resend after failed message: %d", s)
	}
}
