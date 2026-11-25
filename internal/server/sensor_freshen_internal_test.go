package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/store"
)

func TestFreshenSensorsKeepsNewerMeasurement(t *testing.T) {
	h := &hub{sensorData: map[string]store.DataRow{}, sensorStale: time.Millisecond}
	old := time.Now().Add(-time.Hour).UnixMilli()
	h.sensorData["net"] = store.DataRow{Value: json.RawMessage(`{"v":"old"}`), UpdatedAt: old}
	h.sensorRebuild = map[string]func() json.RawMessage{"net": func() json.RawMessage {
		h.mu.Lock() // a new measurement arrives during re-aggregation
		h.sensorData["net"] = store.DataRow{Value: json.RawMessage(`{"v":"new"}`), UpdatedAt: h.sensorStamp("net")}
		h.mu.Unlock()
		return json.RawMessage(`{"v":"rebuilt-from-old"}`)
	}}
	h.freshenSensors()
	if got := string(h.sensorData["net"].Value); got != `{"v":"new"}` {
		t.Fatalf("overwrote the new measurement: %s", got)
	}

	h.sensorData["net"] = store.DataRow{Value: json.RawMessage(`{"v":"old"}`), UpdatedAt: old}
	h.sensorRebuild["net"] = func() json.RawMessage { return json.RawMessage(`{"v":"rebuilt"}`) }
	h.freshenSensors()
	if got := string(h.sensorData["net"].Value); got != `{"v":"rebuilt"}` {
		t.Fatalf("stale bundle was not re-aggregated: %s", got)
	}
}
