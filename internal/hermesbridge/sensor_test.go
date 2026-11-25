package hermesbridge_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/hermesbridge"
	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/sensor/disk"
	"github.com/kyeotapp/kyeot/internal/sensor/netwatch"
	"github.com/kyeotapp/kyeot/internal/sensor/system"
)

type countProbe struct{ n atomic.Int32 }

func (p *countProbe) Measure(context.Context) (netwatch.Raw, error) {
	p.n.Add(1)
	return netwatch.Raw{GW: "192.0.2.1", GwMs: "2", WanMs: "15", DNS: "NXDOMAIN", TCP: "5", UDP: "1", Pwr: "AC"}, nil
}
func (*countProbe) Snapshot(context.Context, netwatch.Raw) string { return "" }

func netwatchSamples(t *testing.T, h *decktest.Harness, agentID string) int {
	t.Helper()
	var owner dataUI
	h.API("GET", "/api/agents/"+agentID+"/ui", nil, &owner)
	var b netwatch.Bundle
	if json.Unmarshal(owner.Data["netwatch"].Value, &b) != nil {
		return 0
	}
	return b.SamplesToday
}

func waitUntil(t *testing.T, what string, f func() bool) {
	t.Helper()
	for range 250 {
		if f() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("waited but never became true: %s", what)
}

func TestBridgeSensorPublishesAndRefreshes(t *testing.T) {
	h := decktest.New(t)
	c := h.ConnectClient()
	_, hermesURL := newFakeHermes(t)
	probe := &countProbe{}
	var disks atomic.Int32
	diskMeasure := func() (disk.Usage, error) { disks.Add(1); return disk.Usage{Used: 1e9, Avail: 3e9}, nil }
	var cpus atomic.Int32
	systemMeasure := func(context.Context) system.Sample {
		cpus.Add(1)
		return system.Sample{MemUsed: 1 << 30, MemTotal: 2 << 30}
	}
	agent := startBridgeWith(t, h, hermesbridge.Config{HermesURL: hermesURL, HermesKey: hermesKey, SensorDir: t.TempDir(), SensorProbe: probe, DiskMeasure: diskMeasure, SystemMeasure: systemMeasure})
	id := agent.Agent.ID
	h.WaitFor(c, "bridge idle", decktest.AgentState(id, "idle"))

	waitUntil(t, "netwatch bundle right after connecting", func() bool { return netwatchSamples(t, h, id) >= 1 })
	waitUntil(t, "measures once on enabling and once on connecting", func() bool { return probe.n.Load() >= 2 })
	waitUntil(t, "sensor feature declared in hello", func() bool {
		var agents []protocol.Agent
		h.API("GET", "/api/agents", nil, &agents)
		i := slices.IndexFunc(agents, func(a protocol.Agent) bool { return a.ID == id })
		return i >= 0 && slices.Contains(agents[i].Features, protocol.FeatureSensorNetwork) && slices.Contains(agents[i].Features, protocol.FeatureDataRefresh) &&
			slices.Contains(agents[i].Features, protocol.FeatureSensorDisk) && slices.Contains(agents[i].Features, protocol.FeatureSensorSystem)
	})
	waitUntil(t, "system bundle", func() bool {
		var owner dataUI
		h.API("GET", "/api/agents/"+id+"/ui", nil, &owner)
		var b system.Bundle
		return json.Unmarshal(owner.Data["system"].Value, &b) == nil && b.MemPct != nil && *b.MemPct == 50
	})
	c0 := cpus.Load()
	if s := h.API("POST", "/api/agents/"+id+"/data/system/refresh", nil, nil); s != http.StatusAccepted {
		t.Fatalf("system refresh: %d", s)
	}
	waitUntil(t, "system refresh", func() bool { return cpus.Load() > c0 })
	waitUntil(t, "disk bundle", func() bool {
		var owner dataUI
		h.API("GET", "/api/agents/"+id+"/ui", nil, &owner)
		var b disk.Bundle
		return json.Unmarshal(owner.Data["disk"].Value, &b) == nil && b.UsedPct != nil && *b.UsedPct == 25 && *b.FreeGB == 3
	})
	d0 := disks.Load()
	if s := h.API("POST", "/api/agents/"+id+"/data/disk/refresh", nil, nil); s != http.StatusAccepted {
		t.Fatalf("disk refresh: %d", s)
	}
	waitUntil(t, "disk refresh", func() bool { return disks.Load() > d0 })

	before := netwatchSamples(t, h, id)
	if s := h.API("POST", "/api/agents/"+id+"/data/netwatch/refresh", nil, nil); s != http.StatusAccepted {
		t.Fatalf("refresh: %d", s)
	}
	waitUntil(t, "refresh button measures again and uploads", func() bool { return netwatchSamples(t, h, id) > before })
	if s := h.API("POST", "/api/agents/"+id+"/data/Bad!/refresh", nil, nil); s != http.StatusBadRequest {
		t.Fatalf("bad bundle name: %d", s)
	}
}

func TestBridgeWithoutSensorDoesNotDeclareIt(t *testing.T) {
	h := decktest.New(t)
	c := h.ConnectClient()
	_, hermesURL := newFakeHermes(t)
	agent := startBridgeWith(t, h, hermesbridge.Config{HermesURL: hermesURL, HermesKey: hermesKey})
	h.WaitFor(c, "bridge idle", decktest.AgentState(agent.Agent.ID, "idle"))
	if s := h.API("POST", "/api/agents/"+agent.Agent.ID+"/data/netwatch/refresh", nil, nil); s != http.StatusConflict {
		t.Fatalf("an agent without sensors gets 409: %d", s)
	}
}
