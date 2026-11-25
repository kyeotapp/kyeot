package hermesbridge

import (
	"context"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

// An approval answer arriving after a restart made us forget the run cannot be passed to Hermes. Dropping it silently leaves the task running.
func TestForgottenApprovalAnswerClosesTask(t *testing.T) {
	b := New(Config{})
	b.answer(context.Background(), "t1", "ap-1", "한 번 허용")
	if len(b.pending) != 2 || b.pending[0].Text != restartedApproval || b.pending[1].Type != "task.end" || b.pending[1].Result != protocol.TaskFailed {
		t.Fatalf("forgotten approval answer: %+v", b.pending)
	}

	b2 := New(Config{})
	b2.runs["t2"] = &run{id: "r1", approvals: map[string]approval{}}
	b2.answer(context.Background(), "t2", "ap-9", "한 번 허용") // the run is alive and only that approval was already handled
	if len(b2.pending) != 0 {
		t.Fatalf("an overlapping answer for an active run must not close the task: %+v", b2.pending)
	}

	b3 := New(Config{})
	b3.ended = map[string]time.Time{"t3": time.Now()} // the run just ended and is sending task.end
	b3.answer(context.Background(), "t3", "ap-1", "한 번 허용")
	if len(b3.pending) != 0 {
		t.Fatalf("an answer for a just-ended task must not produce a restart failure: %+v", b3.pending)
	}
}
