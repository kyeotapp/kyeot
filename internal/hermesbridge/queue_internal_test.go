package hermesbridge

import (
	"context"
	"testing"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

func TestFullWorkQueueTellsTheApp(t *testing.T) {
	b := New(Config{})
	b.workers["task:t1"] = make(chan protocol.ServerMessage) // always full since nobody receives
	b.dispatch(context.Background(), protocol.ServerMessage{Type: "user_message", Task: "t1", Text: "one more"})
	b.mu.Lock()
	if len(b.pending) != 1 || b.pending[0].Type != "message" {
		t.Fatalf("report the dropped message but do not close a task with earlier work running: %+v", b.pending)
	}
	b.pending = nil
	b.mu.Unlock()
	b.workers["task:t2"] = make(chan protocol.ServerMessage)
	b.dispatch(context.Background(), protocol.ServerMessage{Type: "task.start", Task: "t2", Text: "new job"})
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) != 2 || b.pending[1].Type != "task.end" || b.pending[1].Result != protocol.TaskFailed {
		t.Fatalf("a dropped new job is closed as failed: %+v", b.pending)
	}
}

func TestTrimPendingKeepsTaskEnd(t *testing.T) {
	var q []protocol.AgentMessage
	q = append(q, protocol.AgentMessage{Type: "task.end", Task: "old", Result: protocol.TaskDone})
	for range maxPending + 5 {
		q = append(q, protocol.AgentMessage{Type: "message", Task: "t", Text: "reply"})
	}
	q = trimPending(q)
	if len(q) != maxPending || q[0].Type != "task.end" || q[0].Task != "old" {
		t.Fatalf("dropped an old task.end: len %d, first %+v", len(q), q[0])
	}
	ends := make([]protocol.AgentMessage, maxPending+3)
	for i := range ends {
		ends[i] = protocol.AgentMessage{Type: "task.end"}
	}
	if got := trimPending(ends); len(got) != maxPending {
		t.Fatalf("the limit holds even when all are task.end: %d", len(got))
	}
}
