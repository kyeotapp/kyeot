package decktest

import (
	"testing"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

func agentMsg(id string) protocol.ClientMessage {
	return protocol.ClientMessage{Type: "agent", Agent: &protocol.Agent{ID: id, State: "idle"}}
}

// The server may send the two agents' idle in either order. An earlier condition must not discard a later condition's message.
func TestWaitAllAnyOrder(t *testing.T) {
	c := &Client{msgs: make(chan protocol.ClientMessage, 8)}
	h := &Harness{t: t}
	c.msgs <- agentMsg("B")
	c.msgs <- protocol.ClientMessage{Type: "task"}
	c.msgs <- agentMsg("A")
	c.msgs <- protocol.ClientMessage{Type: "after"}
	h.WaitAll(c, "A, B idle", AgentState("A", "idle"), AgentState("B", "idle"))
	if m := <-c.msgs; m.Type != "after" {
		t.Fatalf("WaitAll must stop at the last match, next: %+v", m)
	}
}
