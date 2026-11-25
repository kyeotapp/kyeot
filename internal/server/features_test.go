package server_test

import (
	"bytes"
	"context"
	"log"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

// Features are inferred from messages. Declared in hello gives declared; receiving a report adds report, so the app can decide a feature is "missing".
func TestAgentFeaturesMarkDeclaredAndReport(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	ctx := context.Background()

	features := func(want string) []string {
		for range 50 {
			var list []protocol.Agent
			h.API("GET", "/api/agents", nil, &list)
			for _, ag := range list {
				if ag.ID == a.Agent.ID && slices.Contains(ag.Features, want) {
					return ag.Features
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("feature %q never appeared", want)
		return nil
	}

	agent.Send(ctx, protocol.AgentMessage{Type: "report", Report: []byte(`{"routines":[]}`)})
	if f := features(protocol.FeatureReport); slices.Contains(f, protocol.FeatureDeclared) {
		t.Fatalf("report alone must not mark declared: %v", f)
	}
	agent.Send(ctx, protocol.AgentMessage{Type: "hello", Kind: "Test", Features: []string{protocol.FeatureUI}})
	if f := features(protocol.FeatureDeclared); !slices.Contains(f, protocol.FeatureUI) {
		t.Fatalf("declared features: %v", f)
	}
}

// Only server-known names from hello's features are stored, and the record of dropping is logged once per agent.
func TestHelloFeaturesKeepOnlyKnownNames(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	c := h.ConnectClient()
	agent := h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	ctx := context.Background()

	sent := []string{protocol.FeatureUI, "future.thing", strings.Repeat("y", 300), protocol.FeatureSensorDisk, protocol.FeatureDataRefresh}
	for range 70 {
		sent = append(sent, protocol.FeatureRoutines)
	}
	sent = append(sent, protocol.FeatureCosts) // past the 64th, so dropped
	for range 2 {
		agent.Send(ctx, protocol.AgentMessage{Type: "hello", Kind: "Test", Features: sent})
	}
	h.WaitFor(c, "declared", func(m protocol.ClientMessage) bool {
		return m.Type == "agent" && m.Agent.ID == a.Agent.ID && slices.Contains(m.Agent.Features, protocol.FeatureDeclared)
	})
	agent.Send(ctx, protocol.AgentMessage{Type: "status", State: "working"})
	h.WaitFor(c, "working", decktest.AgentState(a.Agent.ID, "working"))

	var got []string
	var list []protocol.Agent
	h.API("GET", "/api/agents", nil, &list)
	for _, ag := range list {
		if ag.ID == a.Agent.ID {
			got = slices.Sorted(slices.Values(ag.Features))
		}
	}
	want := []string{protocol.FeatureDataRefresh, protocol.FeatureDeclared, protocol.FeatureRoutines, protocol.FeatureSensorDisk, protocol.FeatureUI}
	if !slices.Equal(got, want) {
		t.Fatalf("features %v, want %v", got, want)
	}
	if n := strings.Count(logs.String(), "agent "+a.Agent.ID+" hello: dropping"); n != 1 {
		t.Fatalf("warning logged %d times, want 1:\n%s", n, logs.String())
	}
}
