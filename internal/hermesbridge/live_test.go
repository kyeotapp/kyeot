//go:build hermeslive

package hermesbridge_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/hermesbridge"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

// Runs the bridge against a real Hermes gateway (HERMES_URL, API_SERVER_KEY or HERMES_HOME/.env).
// The deck server is the in-memory test one. Start with scripts/check-hermes-live.sh.

const liveWait = 3 * time.Minute

func liveSetup(t *testing.T) (*decktest.Harness, *decktest.Client, decktest.Created) {
	if os.Getenv("HERMES_URL") == "" {
		t.Skip("set HERMES_URL to the Hermes gateway to check; this writes sessions into it")
	}
	cfg := hermesbridge.FromEnv()
	if cfg.HermesKey == "" {
		t.Skip("no API_SERVER_KEY")
	}
	cfg.HermesHome, cfg.UIAddr = "", "" // read nothing more, open no local port
	h := decktest.New(t)
	c := h.ConnectClient()
	agent := startBridgeWith(t, h, cfg)
	h.WaitFor(c, "bridge idle", decktest.AgentState(agent.Agent.ID, "idle"))
	return h, c, agent
}

func liveState(h *decktest.Harness, id string) string {
	var tasks []protocol.Task
	h.API("GET", "/api/tasks", nil, &tasks)
	for _, t := range tasks {
		if t.ID == id {
			return t.State
		}
	}
	return ""
}

func liveStart(t *testing.T, h *decktest.Harness, agentID, text string) protocol.Task {
	var task protocol.Task
	h.API("POST", "/api/tasks", map[string]string{"agentId": agentID, "text": text}, &task)
	return task
}

// liveApproval waits until the task asks; it fails if the task ends first.
func liveApproval(t *testing.T, h *decktest.Harness, c *decktest.Client, task protocol.Task, kind string) (string, []string) {
	t.Helper()
	deadline := time.Now().Add(liveWait)
	for time.Now().Before(deadline) {
		var pending []protocol.Event
		h.API("GET", "/api/approvals", nil, &pending)
		for _, e := range pending {
			var p struct {
				ID      string   `json:"id"`
				Kind    string   `json:"kind"`
				Options []string `json:"options"`
			}
			if e.TaskID == task.ID && json.Unmarshal(e.Payload, &p) == nil && p.Kind == kind {
				return p.ID, p.Options
			}
		}
		if st := liveState(h, task.ID); protocol.TaskFinished(st) {
			t.Fatalf("task ended as %s before asking; check Hermes approvals.mode or the model's tool use", st)
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("no %q approval within %s", kind, liveWait)
	return "", nil
}

func liveEnd(t *testing.T, h *decktest.Harness, c *decktest.Client, task protocol.Task, want string) {
	t.Helper()
	deadline := time.Now().Add(liveWait)
	for time.Now().Before(deadline) {
		if st := liveState(h, task.ID); protocol.TaskFinished(st) {
			if st != want {
				t.Fatalf("task ended as %s, want %s", st, want)
			}
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("task did not end within %s", liveWait)
}

// liveCommand must make Hermes ask; KYEOT_LIVE_PROMPT replaces it when the approval mode lets it through.
var liveCommand = cmpOr(os.Getenv("KYEOT_LIVE_PROMPT"),
	`Use the terminal tool to run exactly this command and nothing else, then reply with its output: python3 -c "print('kyeot-live-' + 'ok')"`)

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func TestLiveApproveOnce(t *testing.T) {
	h, c, agent := liveSetup(t)
	task := liveStart(t, h, agent.Agent.ID, liveCommand)
	id, _ := liveApproval(t, h, c, task, "")
	h.API("POST", "/api/tasks/"+task.ID+"/approvals/"+id, map[string]string{"choice": "한 번 허용"}, nil)
	h.WaitFor(c, "command output", eventWith(task.ID, "message", "kyeot-live-ok"))
	liveEnd(t, h, c, task, protocol.TaskDone)
}

func TestLiveDeny(t *testing.T) {
	h, c, agent := liveSetup(t)
	task := liveStart(t, h, agent.Agent.ID, liveCommand)
	id, _ := liveApproval(t, h, c, task, "")
	h.API("POST", "/api/tasks/"+task.ID+"/approvals/"+id, map[string]string{"choice": "거절"}, nil)
	liveEnd(t, h, c, task, protocol.TaskDone)
}

func TestLiveClarify(t *testing.T) {
	h, c, agent := liveSetup(t)
	task := liveStart(t, h, agent.Agent.ID, `Before anything else, use the clarify tool to ask me to pick one of exactly two choices: "red" or "blue". After I answer, reply with only the color I picked.`)
	id, options := liveApproval(t, h, c, task, "choice")
	pick := options[len(options)-1]
	h.API("POST", "/api/tasks/"+task.ID+"/approvals/"+id, map[string]string{"choice": pick}, nil)
	h.WaitFor(c, "answer used", func(m protocol.ClientMessage) bool {
		return m.Type == "event" && m.Event.TaskID == task.ID && m.Event.Type == "message" &&
			bytes.Contains(bytes.ToLower(m.Event.Payload), []byte(strings.ToLower(pick)))
	})
	liveEnd(t, h, c, task, protocol.TaskDone)
}

func TestLiveStopWhileWaiting(t *testing.T) {
	h, c, agent := liveSetup(t)
	task := liveStart(t, h, agent.Agent.ID, liveCommand)
	liveApproval(t, h, c, task, "")
	h.API("POST", "/api/tasks/"+task.ID+"/cancel", nil, nil)
	liveEnd(t, h, c, task, protocol.TaskCancelled)
	h.WaitFor(c, "bridge idle", decktest.AgentState(agent.Agent.ID, "idle"))
}
