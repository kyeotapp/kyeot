package server

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/store"
)

// checkAgent restores the last state if the stored state of an attached agent was overwritten with offline.
// Another process on the same DB (a second server that dies right away on a blocked port) has done this.
func (h *hub) checkAgent(p *peer, a store.AgentAuth) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.agents[a.ID] != p || h.closing {
		return
	}
	cur, err := h.store.Agent(a.ID)
	if err != nil || cur.State != protocol.AgentOffline {
		return
	}
	state := cmp.Or(h.live[a.ID], protocol.AgentIdle)
	log.Printf("agent %s: attached but stored state is offline, restoring %s", a.ID, state)
	if err := h.publishAgent(a.OwnerID, a.ID, state); err != nil {
		log.Printf("agent %s check: %v", a.ID, err)
	}
}

// attachAgent rechecks the token under the lock. False if a release or token rotation slipped in between check and registration.
func (h *hub) attachAgent(p *peer, a store.AgentAuth, token string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cur, err := h.store.AgentByToken(token); err != nil || cur.ID != a.ID {
		switch removed, err := h.store.AgentRemoved(a.ID); {
		case removed:
			p.close(4001, "agent unlinked")
		case errors.Is(err, store.ErrNotFound): // the owner's account was deleted with its agents
			p.close(4003, "account deleted")
		default:
			p.close(4002, "token rotated")
		}
		return false
	}
	if old := h.agents[a.ID]; old != nil {
		old.close(4000, "replaced by a new connection")
	}
	h.agents[a.ID] = p
	if t := h.gone[a.ID]; t != nil { // came back within the grace period
		t.Stop()
		delete(h.gone, a.ID)
	}
	if err := h.publishAgent(a.OwnerID, a.ID, protocol.AgentIdle); err != nil {
		log.Printf("agent %s attach: %v", a.ID, err)
	}
	return true
}

func (h *hub) detachAgent(p *peer, a store.AgentAuth) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.agents[a.ID] != p {
		return
	}
	delete(h.agents, a.ID)
	if h.closing {
		return
	}
	if err := h.publishAgent(a.OwnerID, a.ID, protocol.AgentOffline); err != nil {
		log.Printf("agent %s detach: %v", a.ID, err)
	}
	// A disconnected agent cannot answer. If it does not return within the grace period, close in-flight tasks so the screen is not stuck on "typing" forever
	if t := h.gone[a.ID]; t != nil {
		t.Stop()
	}
	h.gone[a.ID] = time.AfterFunc(h.grace, func() { h.agentGone(a.ID) })
}

// closeLeftovers closes open tasks left by the previous process after grace. Tasks of a reattached idle agent are closed too
// (the in-server adapter loses its memory on restart); tasks of working or blocked agents wait until the agent goes idle.
// Pending approvals stay while the agent is attached. Only pending approvals of the in-server adapter (forgets) are closed as before.
func (h *hub) closeLeftovers() {
	open, err := h.store.AllOpenTasks()
	if err != nil || len(open) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.left = make(map[string]int64, len(open))
	for _, t := range open {
		h.left[t.ID] = h.store.LastEventID(t.ID)
	}
	h.leftover = time.AfterFunc(h.grace, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.closing {
			return
		}
		h.leftDue = true
		h.settleLeftovers("")
	})
}

// agentForgets marks that the agent restarted with the server and does not know the approvals it left behind.
func (h *hub) agentForgets(agentID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.forgets == nil {
		h.forgets = map[string]bool{}
	}
	h.forgets[agentID] = true
}

// settleLeftovers closes leftover tasks with no news after the grace period. With agentID, only that agent's tasks are checked.
func (h *hub) settleLeftovers(agentID string) {
	if !h.leftDue || len(h.left) == 0 {
		return
	}
	now, err := h.store.AllOpenTasks()
	if err != nil {
		return
	}
	stillOpen := make(map[string]bool, len(now))
	for _, t := range now {
		last, ok := h.left[t.ID]
		if !ok {
			continue
		}
		stillOpen[t.ID] = true
		if agentID != "" && t.AgentID != agentID {
			continue
		}
		if h.store.LastEventID(t.ID) != last {
			delete(h.left, t.ID)
			continue
		}
		if h.agents[t.AgentID] != nil && (h.live[t.AgentID] != protocol.AgentIdle || (t.State == protocol.TaskWaiting && !h.forgets[t.AgentID])) {
			continue // a reattached agent can still answer a pending approval. If it disconnects again, agentGone closes it
		}
		delete(h.left, t.ID)
		if err := h.moveTask(t.OwnerID, t.ID, protocol.TaskFailed); err != nil {
			log.Printf("leftover task %s: %v", t.ID, err)
		}
	}
	for id := range h.left {
		if !stillOpen[id] {
			delete(h.left, id)
		}
	}
}

func (h *hub) agentGone(agentID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.gone, agentID)
	if h.agents[agentID] != nil || h.closing {
		return
	}
	open, err := h.store.OpenTasks(agentID)
	if err != nil {
		return
	}
	for _, t := range open {
		if err := h.moveTask(t.OwnerID, t.ID, protocol.TaskFailed); err != nil {
			log.Printf("agent %s gone: task %s: %v", agentID, t.ID, err)
		}
	}
}

// agentIdle is when the agent reports it is "idle". Closes tasks as done where the agent answered but sent no task.end.
// Tasks that have not answered the user's message yet are left alone (the agent may be working, not disconnected).
func (h *hub) agentIdle(a store.AgentAuth) {
	open, err := h.store.OpenTasks(a.ID)
	if err != nil {
		return
	}
	for _, t := range open {
		if t.State == protocol.TaskRunning && h.store.LastEventSource(t.ID) == "agent" {
			if err := h.moveTask(t.OwnerID, t.ID, protocol.TaskDone); err != nil {
				log.Printf("agent %s idle: task %s: %v", a.ID, t.ID, err)
			}
		}
	}
}

// SetAvatar stores a default character ("preset:<name>") or a photo (data:image/…;base64, up to 200KB) and notifies the screens.
func (h *hub) SetAvatar(ownerID, agentID, avatar string) (protocol.Agent, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.store.AgentOwnedBy(agentID, ownerID) {
		return protocol.Agent{}, reject(http.StatusNotFound, "agent not found")
	}
	name, isPreset := strings.CutPrefix(avatar, "preset:")
	if !(isPreset && store.Pickable(name)) && !store.IsPhoto(avatar) {
		return protocol.Agent{}, reject(http.StatusBadRequest, "avatar must be a known preset or a jpeg/png data URL under 200KB")
	}
	a, err := h.store.SetAvatar(agentID, avatar)
	if err == nil {
		h.toClients(ownerID, protocol.ClientMessage{Type: "agent", Agent: &a})
	}
	return a, err
}

// maxAgents is the safety cap on unreleased agents per person. Separate from pricing tiers.
const maxAgents = 20

// CreateAgent registers an agent and notifies open screens. The token is shown only once, now.
func (h *hub) CreateAgent(ownerID, name string) (protocol.Agent, string, error) {
	if name = agentName(name); name == "" {
		return protocol.Agent{}, "", reject(http.StatusBadRequest, "name is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if n, err := h.store.AgentCount(ownerID); err != nil {
		return protocol.Agent{}, "", err
	} else if n >= maxAgents {
		return protocol.Agent{}, "", reject(http.StatusConflict, fmt.Sprintf("에이전트는 %d개까지 만들 수 있어요", maxAgents))
	}
	a, token, err := h.store.CreateAgent(ownerID, name)
	if err == nil {
		h.toClients(ownerID, protocol.ClientMessage{Type: "agent", Agent: &a})
	}
	return a, token, err
}

// RotateAgentToken issues a new agent token and drops connections made with the old one.
func (h *hub) RotateAgentToken(ownerID, agentID string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.store.AgentOwnedBy(agentID, ownerID) {
		return "", reject(http.StatusNotFound, "agent not found")
	}
	token, err := h.store.RotateToken(agentID)
	if err != nil {
		return "", err
	}
	if p := h.agents[agentID]; p != nil {
		p.close(4002, "token rotated")
	}
	return token, nil
}

// UnlinkAgent unlinks the connection. The agent itself (Hermes etc.) is left alone.
func (h *hub) UnlinkAgent(ownerID, agentID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.store.AgentOwnedBy(agentID, ownerID) {
		return reject(http.StatusNotFound, "agent not found")
	}
	rooms, err := h.store.Rooms(ownerID)
	if err != nil {
		return err
	}
	if err := h.store.UnlinkAgent(agentID); err != nil {
		return err
	}
	for _, r := range rooms {
		if r.AgentID != agentID || r.Shared {
			continue
		}
		members, _ := h.store.RoomMembers(r.ID)
		r.Shared = true
		for _, m := range members {
			h.toClients(m.OwnerID, protocol.ClientMessage{Type: "room.removed", Room: &r})
		}
	}
	if p := h.agents[agentID]; p != nil {
		delete(h.agents, agentID)
		p.close(4001, "agent unlinked")
	}
	h.toClients(ownerID, protocol.ClientMessage{Type: "agent.removed", AgentID: agentID})
	return nil
}

// closeOwner drops the screens and agent connections of a revoked user.
func (h *hub) closeOwner(ownerID string, agentIDs []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for p, o := range h.clients {
		if o == ownerID {
			delete(h.clients, p)
			p.close(4003, "owner revoked")
		}
	}
	for _, id := range agentIDs {
		if p := h.agents[id]; p != nil {
			delete(h.agents, id)
			p.close(4003, "owner revoked")
		}
		if t := h.gone[id]; t != nil {
			t.Stop()
			delete(h.gone, id)
		}
		open, err := h.store.OpenTasks(id) // cannot reattach, so close right away
		if err != nil {
			log.Printf("owner revoked: agent %s tasks: %v", id, err)
			continue
		}
		for _, t := range open {
			if err := h.moveTask(t.OwnerID, t.ID, protocol.TaskFailed); err != nil {
				log.Printf("owner revoked: task %s: %v", t.ID, err)
			}
		}
	}
}

// DeleteOwner deletes an invited user's account and drops connections. Sends room.removed to members of that person's rooms,
// and the participant list to rooms where that person was a member. The caller deletes attachments.
func (h *hub) DeleteOwner(ownerID string) (store.DeletedOwner, error) {
	return h.dropOwner(ownerID, h.store.DeleteOwner)
}

// HandOver gives the owner seat to heir and deletes the old owner like DeleteOwner.
// old is the current owner, confirmed by the caller under Server.leaveMu.
func (h *hub) HandOver(old, heir, hostToken string) (store.DeletedOwner, error) {
	return h.dropOwner(old, func(string) (store.DeletedOwner, error) { return h.store.HandOver(heir, hostToken) })
}

// CloseServer tells everyone about the closing, drops connections, then deletes the records.
func (h *hub) CloseServer() ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	uploads, err := h.store.WipeAll()
	if err != nil {
		return nil, err
	}
	data, _ := json.Marshal(protocol.ClientMessage{Type: "server.closed"}) // even if undelivered, the app reads close code 4010 the same way
	for p := range h.clients {
		p.sendRaw(data)
		delete(h.clients, p)
		p.close(closeServerClosed, "server closed")
	}
	for id, p := range h.agents {
		delete(h.agents, id)
		p.close(closeServerClosed, "server closed")
	}
	for id, t := range h.gone {
		t.Stop()
		delete(h.gone, id)
	}
	clear(h.live)
	clear(h.left)
	clear(h.featureWarned)
	return uploads, nil
}

func (h *hub) dropOwner(ownerID string, del func(string) (store.DeletedOwner, error)) (store.DeletedOwner, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	owned, err := ownedRoomMembers(h.store, ownerID)
	if err != nil {
		return store.DeletedOwner{}, err
	}
	d, err := del(ownerID)
	if err != nil {
		return d, err
	}
	for p, o := range h.clients {
		if o == ownerID {
			delete(h.clients, p)
			p.close(4003, "account deleted")
		}
	}
	for _, id := range d.Agents {
		if p := h.agents[id]; p != nil {
			delete(h.agents, id)
			p.close(4003, "account deleted")
		}
		if t := h.gone[id]; t != nil {
			t.Stop()
			delete(h.gone, id)
		}
		delete(h.live, id)
		delete(h.featureWarned, id)
	}
	for _, o := range owned {
		o.room.Shared = true
		for _, m := range o.members {
			if m.OwnerID != ownerID {
				h.toClients(m.OwnerID, protocol.ClientMessage{Type: "room.removed", Room: &o.room})
			}
		}
	}
	for _, roomID := range d.Left {
		if room, roomOwner, err := h.store.Room(roomID); err == nil {
			h.toRoom(roomOwner, room)
		}
	}
	return d, nil
}
