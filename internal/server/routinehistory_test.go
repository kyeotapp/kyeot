package server_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

func TestQuietRoutineRunGoesToHistoryOnly(t *testing.T) {
	h, id, agent := manageAgent(t, nil)
	c := h.ConnectClient()
	path := "/api/agents/" + id + "/routines/job1/history"
	at := time.Now().Add(-time.Hour).UnixMilli()
	quiet := protocol.AgentMessage{Type: "routine.run", Routine: "job1", RoutineName: "daily", Run: "2026-11-18_18-00-45", At: at, Quiet: true}
	agent.Send(context.Background(), quiet)
	agent.Send(context.Background(), quiet)
	// Messages on one connection are processed in order, so once the later run with text shows up, the earlier two are done
	agent.Send(context.Background(), protocol.AgentMessage{Type: "routine.run", Routine: "job2", Run: "r1", Text: "result"})
	h.WaitFor(c, "job2 task", func(m protocol.ClientMessage) bool { return m.Task != nil && m.Task.RoutineID == "job2" })
	h.Quiet(c, "job1 must not become a conversation", 200*time.Millisecond, func(m protocol.ClientMessage) bool {
		return m.Task != nil && m.Task.RoutineID == "job1"
	})

	var tasks []protocol.Task
	h.API("GET", "/api/tasks", nil, &tasks)
	for _, x := range tasks {
		if x.RoutineID == "job1" {
			t.Fatalf("quiet run created a task: %+v", x)
		}
	}
	var runs []protocol.RoutineRun
	if s := h.API("GET", path, nil, &runs); s != http.StatusOK || len(runs) != 1 || runs[0].At != at || runs[0].Status != "ok" {
		t.Fatalf("same quiet twice should give 1 history line: %d %+v", s, runs)
	}

	tok := invite(t, h, "Bob")
	if s := h.APIAs(tok, "GET", path, nil, nil); s != http.StatusNotFound {
		t.Fatalf("another user's agent history should be 404: %d", s)
	}
}
