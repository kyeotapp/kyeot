package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/sensor/disk"
	"github.com/kyeotapp/kyeot/internal/sensor/netwatch"
	"github.com/kyeotapp/kyeot/internal/sensor/system"
	"github.com/kyeotapp/kyeot/internal/server"
	"github.com/kyeotapp/kyeot/internal/store"
)

type sensorProbe struct{ n atomic.Int32 }

func (p *sensorProbe) Measure(context.Context) (netwatch.Raw, error) {
	p.n.Add(1)
	return netwatch.Raw{GW: "192.0.2.1", GwMs: "2", WanMs: "15", DNS: "NXDOMAIN", TCP: "5", UDP: "1", Pwr: "AC"}, nil
}
func (*sensorProbe) Snapshot(context.Context, netwatch.Raw) string { return "" }

type sensorServer struct {
	*decktest.Harness
	dir   string
	probe *sensorProbe
	disks *atomic.Int32
	cpus  *atomic.Int32
}

func newSensorServer(t *testing.T, opts ...func(*server.Options)) sensorServer {
	t.Helper()
	s := sensorServer{dir: t.TempDir(), probe: &sensorProbe{}, disks: &atomic.Int32{}, cpus: &atomic.Int32{}}
	s.Harness = decktest.NewWith(t, func(o *server.Options) {
		o.SensorDir, o.SensorProbe, o.Version = s.dir, s.probe, "0.0.1"
		o.DiskMeasure = func() (disk.Usage, error) { s.disks.Add(1); return disk.Usage{Used: 1e9, Avail: 3e9}, nil }
		o.SystemMeasure = func(context.Context) system.Sample {
			cpu := float64(s.cpus.Add(1))
			return system.Sample{CPU: &cpu, MemUsed: 1 << 30, MemTotal: 4 << 30}
		}
		for _, f := range opts {
			f(o)
		}
	})
	return s
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	for range 250 {
		if f() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("waited but did not happen: %s", what)
}

func samples(rows map[string]store.DataRow) int {
	var b netwatch.Bundle
	if json.Unmarshal(rows["netwatch"].Value, &b) != nil {
		return 0
	}
	return b.SamplesToday
}

func (s sensorServer) serverData() map[string]store.DataRow {
	var out map[string]store.DataRow
	s.API("GET", "/api/data", nil, &out)
	return out
}

func (s sensorServer) agentData(id string) map[string]store.DataRow {
	var ui struct {
		Data map[string]store.DataRow `json:"data"`
	}
	s.API("GET", "/api/agents/"+id+"/ui", nil, &ui)
	return ui.Data
}

func TestServerSensorsRunWithoutAgents(t *testing.T) {
	s := newSensorServer(t)
	eventually(t, "netwatch and disk bundles without an agent", func() bool {
		d := s.serverData()
		var b disk.Bundle
		var sy system.Bundle
		return samples(d) >= 1 && json.Unmarshal(d["disk"].Value, &b) == nil && b.UsedPct != nil && *b.UsedPct == 25 &&
			json.Unmarshal(d["system"].Value, &sy) == nil && sy.MemPct != nil && *sy.MemPct == 25
	})
	day := filepath.Join(s.dir, "netwatch", time.Now().Format("2006-01-02")+".tsv")
	if _, err := os.Stat(day); err != nil {
		t.Fatalf("no TSV: %v", err)
	}
	var v struct{ Features []string }
	if code := s.API("GET", "/version", nil, &v); code != http.StatusOK || !slices.Contains(v.Features, server.FeatureSensorServer) || slices.Contains(v.Features, server.FeatureSensorServerOff) {
		t.Fatalf("no sensor.server label: %d %v", code, v.Features)
	}

	before, d0, c0 := samples(s.serverData()), s.disks.Load(), s.cpus.Load()
	if code := s.API("POST", "/api/data/netwatch/refresh", nil, nil); code != http.StatusOK {
		t.Fatalf("netwatch refresh: %d", code)
	}
	if code := s.API("POST", "/api/data/disk/refresh", nil, nil); code != http.StatusOK {
		t.Fatalf("disk refresh: %d", code)
	}
	if code := s.API("POST", "/api/data/system/refresh", nil, nil); code != http.StatusOK {
		t.Fatalf("system refresh: %d", code)
	}
	eventually(t, "re-measured without an agent", func() bool { return samples(s.serverData()) > before && s.disks.Load() > d0 && s.cpus.Load() > c0 })
	if code := s.API("POST", "/api/data/revenue/refresh", nil, nil); code != http.StatusNotFound {
		t.Fatalf("a bundle the server doesn't measure should be 404: %d", code)
	}
}

func TestServerSensorsReachAgentScreens(t *testing.T) {
	s := newSensorServer(t)
	c := s.ConnectClient()
	a := s.CreateAgent("Agent A") // never connected
	eventually(t, "server netwatch in the agent screen bundles", func() bool { return samples(s.agentData(a.Agent.ID)) >= 1 })
	before := samples(s.agentData(a.Agent.ID))
	if code := s.API("POST", "/api/agents/"+a.Agent.ID+"/data/netwatch/refresh", nil, nil); code != http.StatusOK {
		t.Fatalf("server sensors refresh even for a disconnected agent: %d", code)
	}
	s.WaitFor(c, "ui notification after refresh", func(m protocol.ClientMessage) bool { return m.Type == "ui" && m.AgentID == a.Agent.ID })
	eventually(t, "re-measured value", func() bool { return samples(s.agentData(a.Agent.ID)) > before })
}

func TestRemoteAgentSensorKeepsItsOwn(t *testing.T) {
	s := newSensorServer(t)
	a := s.CreateAgent("Agent A")
	agent := s.StartFakeAgent(a.Token)
	ctx := context.Background()
	agent.Send(ctx, protocol.AgentMessage{Type: "hello", Kind: "Test", Features: []string{protocol.FeatureSensorNetwork, protocol.FeatureDataRefresh}})
	agent.Send(ctx, protocol.AgentMessage{Type: "data.put", Name: "netwatch", Data: json.RawMessage(`{"samples_today":777}`)})
	eventually(t, "value measured by that computer", func() bool { return samples(s.agentData(a.Agent.ID)) == 777 })
	var b disk.Bundle
	if json.Unmarshal(s.agentData(a.Agent.ID)["disk"].Value, &b) != nil || b.UsedPct == nil {
		t.Fatal("a disk that agent doesn't measure is the server's")
	}
	n := s.probe.n.Load()
	if code := s.API("POST", "/api/agents/"+a.Agent.ID+"/data/netwatch/refresh", nil, nil); code != http.StatusAccepted {
		t.Fatalf("refresh: %d", code)
	}
	time.Sleep(100 * time.Millisecond)
	if s.probe.n.Load() != n {
		t.Fatal("the server intercepted a refresh for an agent with its own sensor")
	}
}

func TestSensorsOffWithoutDir(t *testing.T) {
	h := decktest.NewWith(t, func(o *server.Options) { o.Version = "0.0.1" })
	var v struct{ Features []string }
	h.API("GET", "/version", nil, &v)
	if slices.Contains(v.Features, server.FeatureSensorServer) || !slices.Contains(v.Features, server.FeatureSensorServerOff) {
		t.Fatalf("with sensors off, expect sensor.server.off instead of sensor.server: %v", v.Features)
	}
	if code := h.API("POST", "/api/data/netwatch/refresh", nil, nil); code != http.StatusNotFound {
		t.Fatalf("sensors off: %d", code)
	}
}

func TestAgentBundleWithoutSensorFeatureYieldsToServer(t *testing.T) {
	s := newSensorServer(t)
	a := s.CreateAgent("Agent A")
	agent := s.StartFakeAgent(a.Token)
	agent.Send(context.Background(), protocol.AgentMessage{Type: "data.put", Name: "netwatch", Data: json.RawMessage(`{"samples_today":777}`)})
	eventually(t, "the server-measured value wins", func() bool { n := samples(s.agentData(a.Agent.ID)); return n >= 1 && n != 777 })
	time.Sleep(100 * time.Millisecond)
	if samples(s.agentData(a.Agent.ID)) == 777 {
		t.Fatal("an agent bundle that did not announce sensor.network overwrote the server sensor")
	}
}

// Bundles upload every 5 minutes, but if one is older than 1 minute (short here) when a screen opens, it is re-aggregated from the records, not re-measured.
func TestStaleServerBundleIsRebuiltOnRequest(t *testing.T) {
	for _, stale := range []time.Duration{time.Millisecond, time.Hour} {
		s := newSensorServer(t, func(o *server.Options) { o.SensorStale = stale })
		a := s.CreateAgent("Agent A")
		eventually(t, "first bundle", func() bool { return samples(s.serverData()) == 1 })
		n := s.probe.n.Load()
		day := filepath.Join(s.dir, "netwatch", time.Now().Format("2006-01-02")+".tsv")
		double := func() { // double today's record line count
			b, err := os.ReadFile(day)
			if err == nil {
				err = os.WriteFile(day, append(b, b...), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(5 * time.Millisecond)
		}
		fresh := stale == time.Millisecond
		double()
		if got, want := samples(s.agentData(a.Agent.ID)), map[bool]int{true: 2, false: 1}[fresh]; got != want {
			t.Fatalf("stale threshold %v: screen %d, want %d", stale, got, want)
		}
		double()
		if got, want := samples(s.serverData()), map[bool]int{true: 4, false: 1}[fresh]; got != want {
			t.Fatalf("stale threshold %v: /api/data %d, want %d", stale, got, want)
		}
		if s.probe.n.Load() != n {
			t.Fatal("should only re-aggregate but re-measured")
		}
	}
}

// The refresh response itself is the new bundle. The screen ends "refreshing" with it even without a websocket notification.
func TestRefreshAnswersWithNewBundle(t *testing.T) {
	s := newSensorServer(t)
	a := s.CreateAgent("Agent A")
	eventually(t, "first measurement", func() bool {
		d := s.agentData(a.Agent.ID)
		return d["netwatch"].UpdatedAt > 0 && d["disk"].UpdatedAt > 0 && d["system"].UpdatedAt > 0
	})
	for _, name := range []string{"netwatch", "disk", "system"} {
		for _, path := range []string{"/api/agents/" + a.Agent.ID + "/data/" + name + "/refresh", "/api/data/" + name + "/refresh"} {
			before := s.agentData(a.Agent.ID)[name].UpdatedAt
			time.Sleep(2 * time.Millisecond) // so a re-measure within the same millisecond still gets a different time
			var got store.DataRow
			if code := s.API("POST", path, nil, &got); code != http.StatusOK {
				t.Fatalf("%s: %d", path, code)
			}
			if got.UpdatedAt <= before || len(got.Value) == 0 {
				t.Fatalf("%s: no new bundle in response %d -> %d", path, before, got.UpdatedAt)
			}
			if now := s.agentData(a.Agent.ID)[name].UpdatedAt; now < got.UpdatedAt {
				t.Fatalf("%s: response time %d is newer than the screen bundle %d", path, got.UpdatedAt, now)
			}
		}
	}
	// Even rapid repeats all end with a new bundle
	before := s.agentData(a.Agent.ID)["disk"].UpdatedAt
	done := make(chan int64, 3)
	for range 3 {
		go func() {
			var got store.DataRow
			s.API("POST", "/api/data/disk/refresh", nil, &got)
			done <- got.UpdatedAt
		}()
	}
	for range 3 {
		if at := <-done; at <= before {
			t.Fatalf("one of the rapid repeats ended without a new bundle: %d (before %d)", at, before)
		}
	}
}

type stuckProbe struct{ sensorProbe }

func (p *stuckProbe) Measure(ctx context.Context) (netwatch.Raw, error) {
	if p.n.Add(1) > 1 {
		<-ctx.Done()
	}
	return netwatch.Raw{GW: "192.0.2.1", GwMs: "2", WanMs: "15", DNS: "NXDOMAIN", TCP: "5", UDP: "1", Pwr: "AC"}, nil
}

// If the sensor can't finish measuring, it answers with a failure instead of waiting forever.
func TestRefreshTimesOutWhenSensorStuck(t *testing.T) {
	probe := &stuckProbe{}
	s := newSensorServer(t, func(o *server.Options) { o.SensorProbe, o.SensorWait = probe, 200*time.Millisecond })
	eventually(t, "first measurement", func() bool { return s.serverData()["netwatch"].UpdatedAt > 0 })
	start := time.Now()
	if code := s.API("POST", "/api/data/netwatch/refresh", nil, nil); code != http.StatusGatewayTimeout {
		t.Fatalf("stalled sensor: %d", code)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("waited longer than the limit")
	}
}
