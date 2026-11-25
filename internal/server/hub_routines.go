package server

import (
	"cmp"
	"encoding/json"
	"net/http"
	"time"

	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/store"
)

// routineRun stores one routine run as a finished task. A run already received is ignored.
func (h *hub) routineRun(a store.AgentAuth, msg protocol.AgentMessage) error {
	if h.store.RoutineRunExists(a.ID, msg.Routine, msg.Run) {
		return nil
	}
	at := msg.At
	if at == 0 {
		at = time.Now().UnixMilli()
	}
	if msg.Quiet {
		status := "ok"
		if msg.OK != nil && !*msg.OK {
			status = "error"
		}
		return h.store.RecordQuietRun(a.ID, msg.Routine, msg.Run, at, status)
	}
	name := cmp.Or(msg.RoutineName, msg.Routine)
	title := name + " · " + time.UnixMilli(at).Format("01/02 15:04")
	payload, _ := json.Marshal(map[string]string{"text": msg.Text})
	state := protocol.TaskDone
	if msg.OK != nil && !*msg.OK {
		state = protocol.TaskFailed
	}
	t, e, err := h.store.CreateRoutineRun(a.OwnerID, a.ID, title, store.NewTask{RoutineID: msg.Routine, RunID: msg.Run, State: state, At: at}, payload)
	if err != nil {
		return err
	}
	h.publishTask(a.OwnerID, t.Task)
	if h.sign != nil {
		e = h.sign(e)
	}
	h.toTask(a.OwnerID, t.ID, protocol.ClientMessage{Type: "event", Event: &e})
	h.notify(a, t.ID, msg.Text, true)
	return nil
}

// why routine commands are not sent to an agent that declared features in hello without routines.manage
const cannotManageRoutines = "이 에이전트는 앱에서 루틴을 관리할 수 없어요"

// ToggleRoutine tells the agent to turn a routine on or off. The agent reports the result in its next report.
func (h *hub) ToggleRoutine(ownerID, agentID, routineID string, on bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.store.AgentOwnedBy(agentID, ownerID) {
		return reject(http.StatusNotFound, "agent not found")
	}
	if err := h.requireOnline(agentID); err != nil {
		return err
	}
	if err := h.requireFeature(agentID, protocol.FeatureManageRoutines, false, cannotManageRoutines); err != nil {
		return err
	}
	h.toAgent(agentID, protocol.ServerMessage{Type: "routine.toggle", Routine: routineID, On: &on})
	return nil
}

// CreateRoutine tells the agent to create a new routine. The result comes in the next report.
func (h *hub) CreateRoutine(ownerID, agentID, name, schedule, prompt string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.store.AgentOwnedBy(agentID, ownerID) {
		return reject(http.StatusNotFound, "agent not found")
	}
	if err := h.requireOnline(agentID); err != nil {
		return err
	}
	if err := h.requireFeature(agentID, protocol.FeatureManageRoutines, false, cannotManageRoutines); err != nil {
		return err
	}
	if !protocol.ValidSchedule(schedule) {
		return reject(http.StatusBadRequest, "schedule must be 5 cron fields or like 'every 2h'")
	}
	if len([]rune(name)) > 60 || len([]rune(prompt)) > 4000 {
		return reject(http.StatusBadRequest, "name 60, prompt 4000 characters max")
	}
	h.toAgent(agentID, protocol.ServerMessage{Type: "routine.create", Name: name, Schedule: schedule, Text: prompt})
	return nil
}

// RemoveRoutine tells the agent to delete a routine. The result comes in the next report.
func (h *hub) RemoveRoutine(ownerID, agentID, routineID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.store.AgentOwnedBy(agentID, ownerID) {
		return reject(http.StatusNotFound, "agent not found")
	}
	if err := h.requireOnline(agentID); err != nil {
		return err
	}
	if err := h.requireFeature(agentID, protocol.FeatureManageRoutines, false, cannotManageRoutines); err != nil {
		return err
	}
	h.toAgent(agentID, protocol.ServerMessage{Type: "routine.remove", Routine: routineID})
	return nil
}
