package server_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/fakeagent"
	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/push"
	"github.com/kyeotapp/kyeot/internal/server"
)

type sent struct {
	device push.Device
	notice push.Notice
}

type fakeSender struct {
	got chan sent
	err error
}

func (f *fakeSender) Send(_ context.Context, d push.Device, n push.Notice) error {
	f.got <- sent{d, n}
	return f.err
}

const iosToken = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"

func pushHarness(t *testing.T) (*decktest.Harness, *fakeSender) {
	t.Helper()
	f := &fakeSender{got: make(chan sent, 16)}
	h := decktest.NewWith(t, func(o *server.Options) { o.Push = f })
	return h, f
}

func registerDevice(t *testing.T, h *decktest.Harness, token, env string) {
	t.Helper()
	if s := h.API("POST", "/api/devices", map[string]string{"token": token, "platform": "ios", "env": env}, nil); s != http.StatusNoContent {
		t.Fatalf("register device: %d", s)
	}
}

func nextPush(t *testing.T, f *fakeSender) sent {
	t.Helper()
	select {
	case s := <-f.got:
		return s
	case <-time.After(2 * time.Second):
		t.Fatal("push was not sent")
		return sent{}
	}
}

func noPush(t *testing.T, f *fakeSender) {
	t.Helper()
	select {
	case s := <-f.got:
		t.Fatalf("push that must not be sent: %+v", s.notice)
	case <-time.After(300 * time.Millisecond):
	}
}

func openTask(agent *fakeagent.Agent, id string, msgs ...string) {
	ctx := context.Background()
	agent.Send(ctx, protocol.AgentMessage{Type: "task.open", Task: id, Title: "Job"})
	for _, m := range msgs {
		agent.Send(ctx, protocol.AgentMessage{Type: "message", Task: id, Text: m})
	}
}

func TestPushOnTaskEndWhenAppClosed(t *testing.T) {
	h, f := pushHarness(t)
	a := h.CreateAgent("Agent A")
	registerDevice(t, h, iosToken, "sandbox")
	agent := h.StartFakeAgent(a.Token)

	long := strings.Repeat("a", 130)
	openTask(agent, "t1", "First reply", long)
	agent.Send(context.Background(), protocol.AgentMessage{Type: "task.end", Task: "t1", Result: "done"})
	s := nextPush(t, f)
	if s.notice.Title != "Agent A" || s.notice.Body != strings.Repeat("a", 120) || s.notice.TaskID != "t1" || s.notice.AgentID != a.Agent.ID || len(s.notice.ServerID) != 32 {
		t.Fatalf("notice: %+v", s.notice)
	}
	if s.device.Token != iosToken || s.device.Env != "sandbox" {
		t.Fatalf("device: %+v", s.device)
	}

	openTask(agent, "t2")
	agent.Send(context.Background(), protocol.AgentMessage{Type: "task.end", Task: "t2", Result: "failed"})
	if s := nextPush(t, f); s.notice.Body != "답이 도착했어요" || s.notice.TaskID != "t2" {
		t.Fatalf("failed without message: %+v", s.notice)
	}

	openTask(agent, "t3", "Dropped")
	agent.Send(context.Background(), protocol.AgentMessage{Type: "task.end", Task: "t3", Result: "cancelled"})
	noPush(t, f)
}

func TestNoPushWhileAppConnected(t *testing.T) {
	h, f := pushHarness(t)
	a := h.CreateAgent("Agent A")
	registerDevice(t, h, iosToken, "sandbox")
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))

	openTask(agent, "t1", "Reply")
	agent.Send(context.Background(), protocol.AgentMessage{Type: "task.end", Task: "t1", Result: "done"})
	h.WaitFor(c, "done", decktest.TaskState("t1", "done"))
	noPush(t, f)
}

func TestPushOnApproval(t *testing.T) {
	h, f := pushHarness(t)
	a := h.CreateAgent("Agent A")
	registerDevice(t, h, iosToken, "production")
	agent := h.StartFakeAgent(a.Token)

	openTask(agent, "t1")
	agent.Send(context.Background(), protocol.AgentMessage{Type: "approval", Task: "t1", ID: "a1", Question: "Pay now?", Options: []string{"Yes", "No"}})
	s := nextPush(t, f)
	if s.notice.Body != "Pay now?" || s.notice.Title != "Agent A" || s.device.Env != "production" {
		t.Fatalf("push: %+v", s)
	}
}

func TestPushOnRoutineRun(t *testing.T) {
	h, f := pushHarness(t)
	a := h.CreateAgent("Agent A")
	registerDevice(t, h, iosToken, "sandbox")
	agent := h.StartFakeAgent(a.Token)

	run := protocol.AgentMessage{Type: "routine.run", Routine: "job1", Run: "r1", Text: "This week's contest"}
	agent.Send(context.Background(), run)
	if s := nextPush(t, f); s.notice.Body != "This week's contest" {
		t.Fatalf("routine push: %+v", s.notice)
	}
	agent.Send(context.Background(), run) // the same run is neither stored nor notified again
	noPush(t, f)
}

func TestDeviceRoutes(t *testing.T) {
	h, f := pushHarness(t)
	for _, b := range []map[string]string{
		{"token": "abc", "platform": "ios", "env": "sandbox"},
		{"token": strings.Repeat("z", 64), "platform": "ios", "env": "sandbox"},
		{"token": strings.Repeat("a", 31), "platform": "ios", "env": "sandbox"},
		{"token": strings.Repeat("a", 201), "platform": "ios", "env": "sandbox"},
		{"token": iosToken, "platform": "ios", "env": "staging"},
		{"token": iosToken, "platform": "android", "env": "sandbox"},
	} {
		if s := h.API("POST", "/api/devices", b, nil); s != http.StatusBadRequest {
			t.Fatalf("%v: %d", b, s)
		}
	}
	if s := h.APIAs("wrong-token", "POST", "/api/devices", map[string]string{"token": iosToken, "platform": "ios", "env": "sandbox"}, nil); s != http.StatusUnauthorized {
		t.Fatalf("unauthorized: %d", s)
	}
	if s := h.API("DELETE", "/api/devices/xyz", nil, nil); s != http.StatusBadRequest {
		t.Fatalf("delete bad token: %d", s)
	}

	registerDevice(t, h, iosToken, "sandbox")
	registerDevice(t, h, iosToken, "production") // the same token is overwritten

	a := h.CreateAgent("Agent A")
	agent := h.StartFakeAgent(a.Token)
	openTask(agent, "t1", "Reply")
	agent.Send(context.Background(), protocol.AgentMessage{Type: "task.end", Task: "t1", Result: "done"})
	if s := nextPush(t, f); s.device.Env != "production" {
		t.Fatalf("env not overwritten: %+v", s.device)
	}
	noPush(t, f) // one device only once

	if s := h.API("DELETE", "/api/devices/"+iosToken, nil, nil); s != http.StatusNoContent {
		t.Fatalf("delete: %d", s)
	}
	openTask(agent, "t2", "Reply")
	agent.Send(context.Background(), protocol.AgentMessage{Type: "task.end", Task: "t2", Result: "done"})
	noPush(t, f)
}

func TestInvalidDeviceTokenIsRemoved(t *testing.T) {
	h, f := pushHarness(t)
	f.err = push.ErrBadToken
	a := h.CreateAgent("Agent A")
	registerDevice(t, h, iosToken, "sandbox")
	agent := h.StartFakeAgent(a.Token)

	openTask(agent, "t1", "Reply")
	agent.Send(context.Background(), protocol.AgentMessage{Type: "task.end", Task: "t1", Result: "done"})
	nextPush(t, f)
	time.Sleep(100 * time.Millisecond) // deletion happens after the send, in the same goroutine

	openTask(agent, "t2", "Reply")
	agent.Send(context.Background(), protocol.AgentMessage{Type: "task.end", Task: "t2", Result: "done"})
	noPush(t, f)
}
