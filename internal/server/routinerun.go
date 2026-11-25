package server

import (
	"context"
	"net/http"
	"slices"

	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/store"
	"github.com/kyeotapp/kyeot/internal/uicatalog"
)

// RunRoutine tells the agent to run a routine once now. The result comes back as usual via routine.run.
// Optional feature: sent only to agents that announced FeatureRunRoutine in hello. 409 for others.
func (h *hub) RunRoutine(ownerID, agentID, routineID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.store.AgentOwnedBy(agentID, ownerID) {
		return reject(http.StatusNotFound, "agent not found")
	}
	if err := h.requireOnline(agentID); err != nil {
		return err
	}
	if err := h.requireFeature(agentID, protocol.FeatureRunRoutine, true, "this agent cannot run routines on demand"); err != nil {
		return err
	}
	h.toAgent(agentID, protocol.ServerMessage{Type: protocol.TypeRoutineTrigger, Routine: routineID})
	return nil
}

// requireFeature returns 409 if the agent lacks the feature. Unless optIn, older agents that declare no features pass.
func (h *hub) requireFeature(agentID, feature string, optIn bool, reason string) error {
	ag, err := h.store.Agent(agentID)
	if err != nil {
		return err
	}
	if slices.Contains(ag.Features, feature) || (!optIn && !slices.Contains(ag.Features, protocol.FeatureDeclared)) {
		return nil
	}
	return reject(http.StatusConflict, reason)
}

// RefreshData makes a bundle be rebuilt now. For a server sensor it returns the new bundle; if delegated to the agent it returns nil (completion shows up as a bundle update).
// If the screen shows a server sensor bundle, the server re-measures at once (agent not involved). Otherwise it goes to agents that announced FeatureDataRefresh in hello, and 409 for others.
func (h *hub) RefreshData(ctx context.Context, ownerID, agentID, name string) (*store.DataRow, error) {
	if !uicatalog.ValidBundleName(name) {
		return nil, reject(http.StatusBadRequest, "묶음 이름은 소문자, 숫자, - 로 32자 이하")
	}
	h.mu.Lock()
	wait, err := h.refreshData(ownerID, agentID, name)
	h.mu.Unlock()
	if err != nil || wait == nil {
		return nil, err
	}
	row, err := wait(ctx)
	return &row, err
}

func (h *hub) refreshData(ownerID, agentID, name string) (func(context.Context) (store.DataRow, error), error) {
	if !h.store.AgentOwnedBy(agentID, ownerID) {
		return nil, reject(http.StatusNotFound, "agent not found")
	}
	ag, err := h.store.Agent(agentID)
	if err != nil {
		return nil, err
	}
	if _, ok := h.sensorRefresh[name]; ok && h.store.IsAdmin(ownerID) {
		have, err := h.store.Data(agentID)
		if err != nil {
			return nil, err
		}
		if !ownSensor(ag.Features, name, have) {
			return h.refreshSensor(name), nil
		}
	}
	if err := h.requireOnline(agentID); err != nil {
		return nil, err
	}
	if !slices.Contains(ag.Features, protocol.FeatureDataRefresh) {
		return nil, reject(http.StatusConflict, "this agent cannot refresh data on demand")
	}
	h.toAgent(agentID, protocol.ServerMessage{Type: protocol.TypeDataRefresh, Name: name})
	return nil, nil
}
