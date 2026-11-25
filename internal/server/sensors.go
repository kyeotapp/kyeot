package server

import (
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"time"

	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/sensor/disk"
	"github.com/kyeotapp/kyeot/internal/sensor/netwatch"
	"github.com/kyeotapp/kyeot/internal/sensor/system"
	"github.com/kyeotapp/kyeot/internal/store"
)

// FeatureSensorServer is the label saying the server measures this computer itself. Listed in /version only while sensors run.
const FeatureSensorServer = "sensor.server"

// FeatureSensorServerOff is the label saying this server can measure but sensors are off (KYEOT_SENSORS=off). Listed only when off.
const FeatureSensorServerOff = "sensor.server.off"

// An agent sensor bundle uploaded within ownSensorFresh wins (kyeot connect on another computer)
const ownSensorFresh = 15 * time.Minute

var sensorFeature = map[string]string{
	netwatch.BundleName: protocol.FeatureSensorNetwork,
	disk.BundleName:     protocol.FeatureSensorDisk,
	system.BundleName:   protocol.FeatureSensorSystem,
}

// startSensors runs the network, disk, and system sensors for the server's lifetime if Options.SensorDir is set.
func (s *Server) startSensors() {
	if s.opts.SensorDir == "" {
		return
	}
	nw := netwatch.New(filepath.Join(s.opts.SensorDir, netwatch.BundleName), s.hub.sensorSink(netwatch.BundleName))
	if s.opts.SensorProbe != nil {
		nw.Probe = s.opts.SensorProbe
	}
	dk := disk.New(s.hub.sensorSink(disk.BundleName))
	if s.opts.DiskMeasure != nil {
		dk.Measure = s.opts.DiskMeasure
	}
	sy := system.New(s.hub.sensorSink(system.BundleName))
	if s.opts.SystemMeasure != nil {
		sy.Measure = s.opts.SystemMeasure
	}
	s.hub.sensorRefresh = map[string]func(){netwatch.BundleName: nw.Refresh, disk.BundleName: dk.Refresh, system.BundleName: sy.Refresh}
	s.hub.sensorRebuild = map[string]func() json.RawMessage{netwatch.BundleName: func() json.RawMessage {
		raw, _ := json.Marshal(nw.Build())
		return raw
	}}
	s.hub.sensorStale = cmp.Or(s.opts.SensorStale, time.Minute)
	s.hub.sensorWait = cmp.Or(s.opts.SensorWait, s.hub.sensorWait)
	s.hub.sensorStop = s.ctx.Done()
	for _, run := range []func(context.Context){nw.Run, dk.Run, sy.Run} {
		s.bg.Add(1)
		go func() {
			defer s.bg.Done()
			run(s.ctx)
		}()
	}
}

func (s *Server) features() []string {
	if s.hub.sensorRefresh == nil {
		return append(slices.Clone(Features), FeatureSensorServerOff)
	}
	return append(slices.Clone(Features), FeatureSensorServer)
}

// freshenSensors re-aggregates server bundles older than sensorStale from the records (measuring already runs every 20s). Call outside h.mu.
func (h *hub) freshenSensors() {
	if h.sensorRebuild == nil {
		return
	}
	h.mu.Lock()
	stale := map[string]int64{} // name -> time when seen
	for name := range h.sensorRebuild {
		if row, ok := h.sensorData[name]; ok && time.Since(time.UnixMilli(row.UpdatedAt)) > h.sensorStale {
			stale[name] = row.UpdatedAt
		}
	}
	h.mu.Unlock()
	for name, seen := range stale {
		raw := h.sensorRebuild[name]()
		if raw == nil {
			continue
		}
		h.mu.Lock()
		if h.sensorData[name].UpdatedAt == seen { // keep a new measurement that arrived during re-aggregation
			h.sensorData[name] = store.DataRow{Value: raw, UpdatedAt: h.sensorStamp(name)}
		}
		h.mu.Unlock()
	}
}

// sensorStamp always gives a bundle time greater than the previous one, so two measurements in the same millisecond still look new. Caller holds h.mu.
func (h *hub) sensorStamp(name string) int64 {
	return max(time.Now().UnixMilli(), h.sensorData[name].UpdatedAt+1)
}

// sensorSink keeps server sensor bundles in memory and notifies screens watching them.
func (h *hub) sensorSink(name string) func(json.RawMessage) {
	return func(v json.RawMessage) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.sensorData[name] = store.DataRow{Value: v, UpdatedAt: h.sensorStamp(name)}
		h.sensorSeq[name]++
		close(h.sensorPub)
		h.sensorPub = make(chan struct{})
		owners := map[string]bool{}
		for _, o := range h.clients {
			owners[o] = true
		}
		for o := range owners {
			agents, err := h.store.Agents(o)
			if err != nil {
				continue
			}
			for _, a := range agents {
				h.toClients(o, protocol.ClientMessage{Type: "ui", AgentID: a.ID})
			}
		}
	}
}

// ownSensor checks whether this agent measures and uploads the name bundle itself.
func ownSensor(features []string, name string, have map[string]store.DataRow) bool {
	f, ok := sensorFeature[name]
	row, has := have[name]
	return ok && has && slices.Contains(features, f) && time.Since(time.UnixMilli(row.UpdatedAt)) < ownSensorFresh
}

// withSensors adds server sensor bundles to an agent's bundles. Caller holds h.mu.
func (h *hub) withSensors(agentID string, data map[string]store.DataRow) {
	if len(h.sensorData) == 0 {
		return
	}
	ag, err := h.store.Agent(agentID)
	if err != nil {
		return
	}
	if owner, err := h.store.AgentOwner(agentID); err != nil || !h.store.IsAdmin(owner) {
		return // server computer state shows only on the owner's and admins' agent screens
	}
	for name, row := range h.sensorData {
		if !ownSensor(ag.Features, name, data) {
			data[name] = row
		}
	}
}

// refreshSensor, for a server sensor, signals an immediate re-measure and returns a function that waits for the new bundle; nil otherwise. Caller holds h.mu and calls the returned function outside it.
func (h *hub) refreshSensor(name string) func(context.Context) (store.DataRow, error) {
	f, ok := h.sensorRefresh[name]
	if !ok {
		return nil
	}
	seq := h.sensorSeq[name]
	f()
	return func(ctx context.Context) (store.DataRow, error) {
		deadline := time.After(h.sensorWait)
		for {
			h.mu.Lock()
			row, done, pub := h.sensorData[name], h.sensorSeq[name] != seq, h.sensorPub
			h.mu.Unlock()
			if done {
				return row, nil
			}
			select {
			case <-pub:
			case <-deadline:
				return store.DataRow{}, reject(http.StatusGatewayTimeout, "센서가 아직 다 재지 못했어요: "+name)
			case <-ctx.Done():
				return store.DataRow{}, ctx.Err()
			case <-h.sensorStop:
				return store.DataRow{}, reject(http.StatusServiceUnavailable, "서버가 닫히는 중이에요")
			}
		}
	}
}

func (h *hub) ServerData() map[string]store.DataRow {
	h.freshenSensors()
	h.mu.Lock()
	defer h.mu.Unlock()
	out := maps.Clone(h.sensorData)
	if out == nil {
		out = map[string]store.DataRow{}
	}
	return out
}

// RefreshServerData re-measures the server sensors and returns the new bundle.
func (h *hub) RefreshServerData(ctx context.Context, name string) (store.DataRow, error) {
	h.mu.Lock()
	wait := h.refreshSensor(name)
	h.mu.Unlock()
	if wait == nil {
		return store.DataRow{}, reject(http.StatusNotFound, "서버가 재는 묶음이 아니에요: "+name)
	}
	return wait(ctx)
}

func (s *Server) sensorRoutes() {
	// server computer state: owner and admins only
	s.mux.HandleFunc("GET /api/data", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if !s.store.IsAdmin(owner) {
			return reject(http.StatusForbidden, "only admins can read server data")
		}
		writeJSON(w, http.StatusOK, s.hub.ServerData())
		return nil
	}))
	s.mux.HandleFunc("POST /api/data/{name}/refresh", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if !s.store.IsAdmin(owner) {
			return reject(http.StatusForbidden, "only admins can refresh server data")
		}
		row, err := s.hub.RefreshServerData(r.Context(), r.PathValue("name"))
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, row)
		return nil
	}))
}
