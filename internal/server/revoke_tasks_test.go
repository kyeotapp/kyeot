package server_test

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/server"
	"github.com/kyeotapp/kyeot/internal/store"
)

func TestRevokedUsersOpenTasksFail(t *testing.T) {
	db := filepath.Join(t.TempDir(), "k.db")
	h := decktest.NewWith(t, func(o *server.Options) { o.DBPath, o.AgentGrace = db, time.Hour })
	tok2 := invite(t, h, "Bob")
	var fa decktest.Created
	if s := h.APIAs(tok2, "POST", "/api/agents", map[string]string{"name": "Bob bot"}, &fa); s != http.StatusCreated {
		t.Fatalf("friend agent: %d", s)
	}
	rawAgent(t, h, fa.Token)
	waitStateAs(t, h, tok2, fa.Agent.ID)
	var task protocol.Task
	if s := h.APIAs(tok2, "POST", "/api/tasks", map[string]string{"agentId": fa.Agent.ID, "text": "Do it"}, &task); s != http.StatusCreated {
		t.Fatalf("task: %d", s)
	}
	var friendID string
	var me struct {
		ID string `json:"ownerId"`
	}
	h.APIAs(tok2, "GET", "/api/me", nil, &me)
	friendID = me.ID
	if s := h.API("DELETE", "/api/owners/"+friendID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("revoke: %d", s)
	}
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err := st.Task(task.ID)
	if err != nil || got.State != protocol.TaskFailed {
		t.Fatalf("open task of a revoked user: %+v %v", got.State, err)
	}
}

func waitStateAs(t *testing.T, h *decktest.Harness, tok, agentID string) {
	t.Helper()
	for deadline := time.Now().Add(decktest.WaitLimit); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		var agents []protocol.Agent
		h.APIAs(tok, "GET", "/api/agents", nil, &agents)
		for _, a := range agents {
			if a.ID == agentID && a.State == protocol.AgentIdle {
				return
			}
		}
	}
	t.Fatal("agent never idle")
}
