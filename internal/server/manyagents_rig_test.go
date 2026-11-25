package server_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/fakeagent"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

// Test rig attaching many people x many agents to one server. Keeps every message received.

type rigOwner struct {
	name, token, id string
	agents          []*rigAgent
	client          *rigClient
}

type rigAgent struct {
	id, token string
	owner     *rigOwner
	fa        *fakeagent.Agent

	mu    sync.Mutex
	got   []protocol.ServerMessage
	tasks map[string]bool // task ids the server gave this agent (task.start) and tasks it opened itself
}

type rigClient struct {
	owner *rigOwner

	mu      sync.Mutex
	got     []protocol.ClientMessage
	changed chan struct{}
	doneAt  map[string]time.Time
	waiters map[string]chan struct{}
}

func newRigOwners(t *testing.T, h *decktest.Harness, owners, perOwner int) []*rigOwner {
	t.Helper()
	out := make([]*rigOwner, owners)
	for i := range out {
		o := &rigOwner{name: fmt.Sprintf("Person%d", i)}
		if i == 0 {
			o.token = decktest.OwnerToken
		} else {
			o.token = invite(t, h, o.name)
		}
		var me struct{ OwnerID string }
		h.APIAs(o.token, "GET", "/api/me", nil, &me)
		o.id = me.OwnerID
		for j := 0; j < perOwner; j++ {
			var c decktest.Created
			if s := h.APIAs(o.token, "POST", "/api/agents", map[string]string{"name": fmt.Sprintf("%s-%d", o.name, j)}, &c); s != http.StatusCreated {
				t.Fatalf("create agent %d/%d: %d", i, j, s)
			}
			o.agents = append(o.agents, &rigAgent{id: c.Agent.ID, token: c.Token, owner: o, tasks: map[string]bool{}})
		}
		out[i] = o
	}
	return out
}

func (o *rigOwner) connect(t *testing.T, h *decktest.Harness) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.URL, "http")+"/client?token="+o.token, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetReadLimit(4 << 20)
	c := &rigClient{owner: o, changed: make(chan struct{}), doneAt: map[string]time.Time{}, waiters: map[string]chan struct{}{}}
	go func() {
		for {
			var m protocol.ClientMessage
			if wsjson.Read(ctx, conn, &m) != nil {
				return
			}
			c.add(m)
		}
	}()
	t.Cleanup(func() { conn.CloseNow(); cancel() })
	pctx, pcancel := context.WithTimeout(ctx, decktest.WaitLimit)
	defer pcancel()
	if err := conn.Ping(pctx); err != nil { // the pong comes only after the server registers with the hub
		t.Fatal(err)
	}
	o.client = c
}

func (c *rigClient) add(m protocol.ClientMessage) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, m)
	if m.Type == "task" && m.Task != nil && protocol.TaskFinished(m.Task.State) {
		if _, ok := c.doneAt[m.Task.ID]; !ok {
			c.doneAt[m.Task.ID] = now
		}
		if w := c.waiters[m.Task.ID]; w != nil {
			close(w)
			delete(c.waiters, m.Task.ID)
		}
	}
	close(c.changed)
	c.changed = make(chan struct{})
}

// waitDone returns the time the notice that the task finished arrived.
func (c *rigClient) waitDone(taskID string, limit time.Duration) (time.Time, bool) {
	c.mu.Lock()
	if at, ok := c.doneAt[taskID]; ok {
		c.mu.Unlock()
		return at, true
	}
	w := c.waiters[taskID]
	if w == nil {
		w = make(chan struct{})
		c.waiters[taskID] = w
	}
	c.mu.Unlock()
	select {
	case <-w:
	case <-time.After(limit):
		return time.Time{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.doneAt[taskID], true
}

func (c *rigClient) waitFor(t *testing.T, what string, pred func(protocol.ClientMessage) bool) {
	t.Helper()
	deadline := time.After(decktest.WaitLimit)
	seen := 0
	for {
		c.mu.Lock()
		for ; seen < len(c.got); seen++ {
			if pred(c.got[seen]) {
				c.mu.Unlock()
				return
			}
		}
		ch := c.changed
		c.mu.Unlock()
		select {
		case <-ch:
		case <-deadline:
			t.Errorf("%s: timeout waiting for %s", c.owner.name, what)
			return
		}
	}
}

func (c *rigClient) snapshot() []protocol.ClientMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]protocol.ClientMessage(nil), c.got...)
}

// start attaches over a fake agent connection, keeps what it receives, and replies.
// task.start -> tool, reply, approval request (finishes right away if the body has "quick" or "room").
// approval_result, user_message -> reply + task.end.
// Every reply carries from:<own id> so the screen side can check which agent wrote a post.
func (a *rigAgent) start(t *testing.T, h *decktest.Harness) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	fa, err := fakeagent.Dial(ctx, h.URL, a.token)
	if err != nil {
		t.Fatal(err)
	}
	fa.Conn.SetReadLimit(4 << 20)
	a.fa = fa
	t.Cleanup(func() { fa.Close(); cancel() })
	send := func(m protocol.AgentMessage) { fa.Send(ctx, m) }
	from := "from:" + a.id
	ok := true
	send(protocol.AgentMessage{Type: "status", State: protocol.AgentIdle})
	go func() {
		for {
			var m protocol.ServerMessage
			if wsjson.Read(ctx, fa.Conn, &m) != nil {
				return
			}
			a.mu.Lock()
			a.got = append(a.got, m)
			if m.Type == "task.start" {
				a.tasks[m.Task] = true
			}
			a.mu.Unlock()
			switch m.Type {
			case "task.start":
				if strings.Contains(m.Text, "quick") || strings.Contains(m.Text, "room") {
					send(protocol.AgentMessage{Type: "message", Task: m.Task, Text: from + " " + m.Text})
					send(protocol.AgentMessage{Type: "task.end", Task: m.Task, Result: protocol.TaskDone})
					continue
				}
				send(protocol.AgentMessage{Type: "tool", Task: m.Task, Call: "c1", Phase: "started", Name: "look", Summary: from})
				send(protocol.AgentMessage{Type: "tool", Task: m.Task, Call: "c1", Phase: "completed", Name: "look", Summary: from, OK: &ok})
				send(protocol.AgentMessage{Type: "draft", Task: m.Task, Text: from + " typing"})
				send(protocol.AgentMessage{Type: "approval", Task: m.Task, ID: "ap", Question: from + " proceed?", Options: []string{"Approve", "Reject"}})
			case "approval_result", "user_message":
				send(protocol.AgentMessage{Type: "message", Task: m.Task, Text: from + " " + m.Type + " " + m.Text + m.Choice})
				send(protocol.AgentMessage{Type: "task.end", Task: m.Task, Result: protocol.TaskDone})
			}
		}
	}()
}

func (a *rigAgent) snapshot() []protocol.ServerMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]protocol.ServerMessage(nil), a.got...)
}

func (a *rigAgent) knows(task string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tasks[task]
}

func (a *rigAgent) open(task string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tasks[task] = true
}

// waitAgentError waits until the agent receives an error whose reason contains want.
func (a *rigAgent) waitError(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(decktest.WaitLimit)
	for time.Now().Before(deadline) {
		for _, m := range a.snapshot() {
			if m.Type == "error" && strings.Contains(m.Reason, want) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("agent %s: no error containing %q", a.id, want)
}

func allIdle(t *testing.T, owners []*rigOwner) {
	t.Helper()
	for _, o := range owners {
		for _, a := range o.agents {
			o.client.waitFor(t, a.id+" idle", decktest.AgentState(a.id, protocol.AgentIdle))
		}
	}
}
