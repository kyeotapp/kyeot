package server

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/push"
	"github.com/kyeotapp/kyeot/internal/report"
	"github.com/kyeotapp/kyeot/internal/store"
)

// ActionError is the reason a user request was rejected, with an HTTP status code.
type ActionError struct {
	Status int
	Msg    string
}

func (e *ActionError) Error() string { return e.Msg }

func reject(status int, msg string) error { return &ActionError{status, msg} }

// hub holds connected agents and screens and relays messages.
// Storing and delivering share one lock (mu) so every screen sees events in the same order.
type hub struct {
	mu       sync.Mutex
	store    *store.Store
	agents   map[string]*peer
	clients  map[*peer]string
	gone     map[string]*time.Timer // disconnected agents. Tasks are closed if they do not return within grace
	leftover *time.Timer            // closing of tasks left by the previous process
	leftDue  bool                   // grace period for leftover tasks has passed
	left     map[string]int64       // leftover task -> last event id at startup. Removed if news arrives after that
	grace    time.Duration
	closing  bool              // server is shutting down. Disconnects are not stored (so state a new process already wrote is not overwritten)
	forgets  map[string]bool   // agents that restart with the server and lose memory (same-computer Hermes adapter)
	live     map[string]string // last state written for attached agents. Restored if the stored state is overwritten from outside

	push     push.Sender                         // nil turns push off
	sign     func(protocol.Event) protocol.Event // re-signs attachment addresses. nil leaves them as is
	signFile func(id string) string              // signs one attachment's address. nil means no address
	pushCtx  context.Context
	pushes   sync.WaitGroup

	sensorData    map[string]store.DataRow // bundles the server measured on this computer. Not stored (re-measured at startup)
	sensorRefresh map[string]func()        // nil if sensors are off
	sensorRebuild map[string]func() json.RawMessage
	sensorStale   time.Duration
	sensorSeq     map[string]int // upload count per bundle. Basis for a refresh request waiting on a new bundle
	sensorPub     chan struct{}  // closed and recreated each time a server sensor bundle arrives
	sensorWait    time.Duration
	sensorStop    <-chan struct{} // closed when the server closes. Ends refresh waits

	featureWarned map[string]bool // agents whose unknown hello feature names were already logged as dropped
}

func newHub(s *store.Store) *hub {
	return &hub{store: s, agents: map[string]*peer{}, clients: map[*peer]string{}, gone: map[string]*time.Timer{}, grace: time.Minute, live: map[string]string{}, sensorData: map[string]store.DataRow{}, sensorSeq: map[string]int{}, sensorPub: make(chan struct{}), sensorWait: 15 * time.Second}
}

func (h *hub) toClients(ownerID string, msg protocol.ClientMessage) {
	var data []byte
	for p, owner := range h.clients {
		if owner != ownerID {
			continue
		}
		if data == nil {
			data, _ = json.Marshal(msg) // once, even with many screens
		}
		p.sendRaw(data)
	}
}

// audience logs TaskAudience errors and passes them on.
func (h *hub) audience(ownerID, taskID string) []string {
	ids, err := h.store.TaskAudience(ownerID, taskID)
	if err != nil {
		log.Printf("task %s audience: %v", taskID, err)
	}
	return ids
}

// roomMembers logs RoomMembers errors and passes them on.
func (h *hub) roomMembers(roomID string) []store.Member {
	members, err := h.store.RoomMembers(roomID)
	if err != nil {
		log.Printf("room %s members: %v", roomID, err)
	}
	return members
}

// toTask sends to the task owner and to members of shared rooms that hold the task as a post.
func (h *hub) toTask(ownerID, taskID string, msg protocol.ClientMessage) {
	h.toClients(ownerID, msg)
	if !h.othersOnline(ownerID) {
		return // alone, so no need to look up members
	}
	for _, m := range h.audience(ownerID, taskID) {
		if m != ownerID {
			h.toClients(m, msg)
		}
	}
}

func (h *hub) othersOnline(ownerID string) bool {
	for _, o := range h.clients {
		if o != ownerID {
			return true
		}
	}
	return false
}

// toRoom sends to the room owner and members. Shared is turned on for members.
func (h *hub) toRoom(ownerID string, r protocol.Room) {
	h.toClients(ownerID, protocol.ClientMessage{Type: "room", Room: &r})
	members := h.roomMembers(r.ID)
	if len(members) == 0 {
		return
	}
	ownerName := h.store.OwnerName(ownerID)
	for _, m := range members {
		shared := r
		shared.Shared, shared.OwnerName, shared.CanAsk = true, ownerName, m.Role == store.RoleAgent
		h.toClients(m.OwnerID, protocol.ClientMessage{Type: "room", Room: &shared})
	}
}

func (h *hub) toAgent(agentID string, msg protocol.ServerMessage) {
	if p := h.agents[agentID]; p != nil {
		p.send(msg)
	}
}

func (h *hub) publishAgent(ownerID, agentID, state string) error {
	a, err := h.store.SetAgentState(agentID, state)
	if err != nil {
		return err
	}
	if state == protocol.AgentOffline {
		delete(h.live, agentID)
	} else {
		h.live[agentID] = state
	}
	h.toClients(ownerID, protocol.ClientMessage{Type: "agent", Agent: &a})
	return nil
}

// locked stores and delivers under the single hub lock. Inside f, only toRoom and toClients, which do not lock, may be used
func (h *hub) locked(f func() error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return f()
}

// saveRoom stores the room and, on success, delivers it under the same lock
func (h *hub) saveRoom(ownerID string, save func() (protocol.Room, error)) (protocol.Room, error) {
	var r protocol.Room
	err := h.locked(func() error {
		var err error
		if r, err = save(); err == nil {
			h.toRoom(ownerID, r)
		}
		return err
	})
	return r, err
}

func (h *hub) publishTask(ownerID string, t protocol.Task) {
	h.toTask(ownerID, t.ID, protocol.ClientMessage{Type: "task", Task: &t})
}

func (h *hub) moveTask(ownerID, taskID, state string) error {
	t, err := h.store.SetTaskState(taskID, state)
	if err != nil {
		return err
	}
	h.publishTask(ownerID, t)
	return nil
}

func (h *hub) record(ownerID, taskID, source, typ string, payload any) error {
	_, err := h.recordState(ownerID, taskID, source, typ, payload, "")
	return err
}

// recordState stores the event and the task state (if state is set) in one go and sends the event. The caller sends the changed task.
func (h *hub) recordState(ownerID, taskID, source, typ string, payload any, state string) (protocol.Task, error) {
	raw, ok := payload.(json.RawMessage)
	if !ok {
		raw, _ = json.Marshal(payload)
	}
	e, t, err := h.store.AddEventWithState(taskID, source, typ, raw, state)
	if err != nil {
		return t, err
	}
	if h.sign != nil {
		e = h.sign(e)
	}
	h.toTask(ownerID, taskID, protocol.ClientMessage{Type: "event", Event: &e})
	return t, nil
}

func (h *hub) attachClient(p *peer, ownerID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[p] = ownerID
}

func (h *hub) detachClient(p *peer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, p)
}

func (h *hub) onAgentMessage(p *peer, a store.AgentAuth, raw []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.agents[a.ID] != p { // late message from a replaced or released connection
		return
	}
	// store even while shutting down (h.closing): keeps the reply sent just before the disconnect; the DB closes after all connections end
	if err := h.handleAgentMessage(a, raw); err != nil {
		log.Printf("agent %s message: %v", a.ID, err)
	}
}

func (h *hub) handleAgentMessage(a store.AgentAuth, raw []byte) error {
	msg, payload, ok := protocol.ParseAgentMessage(raw)
	if !ok {
		h.toAgent(a.ID, protocol.ServerMessage{Type: "error", Reason: "invalid message"})
		return nil
	}
	switch msg.Type {
	case "status":
		if msg.State == protocol.AgentIdle {
			h.agentIdle(a)
		}
		if err := h.publishAgent(a.OwnerID, a.ID, msg.State); err != nil {
			return err
		}
		if msg.State == protocol.AgentIdle {
			h.settleLeftovers(a.ID)
		}
		return nil
	case "hello":
		info, ok := agentInfo(msg)
		if !ok {
			h.toAgent(a.ID, protocol.ServerMessage{Type: "error", Reason: "invalid hello"})
			return nil
		}
		ag, err := h.store.SetAgentInfo(a.ID, info)
		if err != nil {
			return err
		}
		h.toClients(a.OwnerID, protocol.ClientMessage{Type: "agent", Agent: &ag})
		if len(msg.Features) > 0 {
			return h.learnFeatures(a, append(h.knownFeatures(a.ID, msg.Features), protocol.FeatureDeclared)...)
		}
		return nil
	case "report":
		kind := ""
		if ag, err := h.store.Agent(a.ID); err == nil && ag.Info != nil {
			kind = ag.Info.Kind
		}
		normalized, feat, err := report.Normalize(msg.Report, kind, time.Now())
		if err != nil {
			h.toAgent(a.ID, protocol.ServerMessage{Type: "error", Reason: "invalid report: " + err.Error()})
			return nil
		}
		if err := h.store.SaveReport(a.ID, normalized); err != nil {
			return err
		}
		h.toClients(a.OwnerID, protocol.ClientMessage{Type: "report", AgentID: a.ID, Report: normalized})
		feats := []string{protocol.FeatureReport}
		if feat.Routines {
			feats = append(feats, protocol.FeatureRoutines)
		}
		if feat.Costs {
			feats = append(feats, protocol.FeatureCosts)
		}
		return h.learnFeatures(a, feats...)
	case "routine.run":
		if err := h.store.LearnRoutine(a.ID, msg.Routine, msg.RoutineName, cmp.Or(msg.At, time.Now().UnixMilli())); err != nil {
			return err
		}
		if err := h.learnFeatures(a, protocol.FeatureRoutines); err != nil {
			return err
		}
		return h.routineRun(a, msg)
	case "room.create", "room.rename":
		return h.agentRoom(a, msg)
	case "ui.put", "ui.remove", "ui.layout", "ui.list", "ui.versions", "ui.revert", "ui.part.put", "ui.part.remove", "data.put", "data.remove":
		if msg.Type == "ui.put" || msg.Type == "ui.part.put" || msg.Type == "data.put" {
			if err := h.learnFeatures(a, protocol.FeatureUI); err != nil {
				return err
			}
		}
		return h.agentUI(a, msg)
	}

	task, err := h.store.Task(msg.Task)
	exists := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}

	if msg.Type == "task.open" {
		if exists {
			h.toAgent(a.ID, protocol.ServerMessage{Type: "error", Reason: "task id already exists"})
			return nil
		}
		t, err := h.store.CreateTask(a.OwnerID, a.ID, msg.Title, store.NewTask{ID: msg.Task})
		if err != nil {
			return err
		}
		h.publishTask(a.OwnerID, t.Task)
		return nil
	}

	if !exists || task.AgentID != a.ID {
		h.toAgent(a.ID, protocol.ServerMessage{Type: "error", Reason: "unknown task " + msg.Task})
		return nil
	}
	if msg.Type == "task.title" {
		title, ok := protocol.CleanTitle(msg.Title)
		if !ok {
			h.toAgent(a.ID, protocol.ServerMessage{Type: "error", Reason: "invalid title"})
			return nil
		}
		return h.retitle(a.OwnerID, task.ID, title, store.TitleAgent)
	}
	if msg.Type == "draft" { // a draft being typed is only streamed to screens, not stored
		h.toTask(a.OwnerID, task.ID, protocol.ClientMessage{Type: "draft", TaskID: task.ID, Text: msg.Text})
		return nil
	}
	if msg.Type == "reaction" {
		target := h.store.LastUserEvent(task.ID)
		if target == 0 {
			h.toAgent(a.ID, protocol.ServerMessage{Type: "error", Reason: "reaction: no user message in " + msg.Task})
			return nil
		}
		if len(msg.Emoji) > 16 {
			h.toAgent(a.ID, protocol.ServerMessage{Type: "error", Reason: "reaction: emoji is longer than 16 bytes"})
			return nil
		}
		return h.record(a.OwnerID, task.ID, "agent", "reaction", map[string]any{"target": target, "emoji": msg.Emoji, "on": true})
	}
	if msg.Type == "approval" && msg.ID != "" {
		dup, err := h.store.SameApprovalPending(task.ID, msg.ID, payload)
		if err != nil || dup {
			return err
		}
	}
	next := ""
	if !protocol.TaskFinished(task.State) {
		switch msg.Type {
		case "approval":
			next = protocol.TaskWaiting
		case "task.end":
			next = msg.Result
		}
	}
	moved, err := h.recordState(a.OwnerID, task.ID, "agent", msg.Type, payload, next)
	if err != nil || next == "" {
		return err
	}
	h.publishTask(a.OwnerID, moved)
	switch msg.Type {
	case "approval":
		h.notify(a, task.ID, msg.Question, false)
	case "task.end":
		if msg.Result == protocol.TaskDone || msg.Result == protocol.TaskFailed {
			h.notify(a, task.ID, h.store.LastAgentText(task.ID), false)
		}
	}
	return nil
}

// agentFeatureNames are the agent feature names the server understands. Every name the app reads from agent.features must be here.
var agentFeatureNames = []string{
	protocol.FeatureRoutines, protocol.FeatureCosts, protocol.FeatureUI, protocol.FeatureReport, protocol.FeatureDeclared,
	protocol.FeatureRunRoutine, protocol.FeatureDataRefresh, protocol.FeatureSensorNetwork, protocol.FeatureSensorDisk,
	protocol.FeatureSensorSystem, protocol.FeatureManageRoutines,
}

const (
	maxHelloFeatures   = 64
	maxHelloFeatureLen = 64
)

// knownFeatures keeps only the names the server knows from hello's features. Anything dropped is logged once per agent.
func (h *hub) knownFeatures(agentID string, feats []string) []string {
	var out, dropped []string
	for i, f := range feats {
		if i < maxHelloFeatures && len(f) <= maxHelloFeatureLen && slices.Contains(agentFeatureNames, f) {
			out = append(out, f)
		} else {
			dropped = append(dropped, f)
		}
	}
	if len(dropped) > 0 && !h.featureWarned[agentID] {
		if h.featureWarned == nil {
			h.featureWarned = map[string]bool{}
		}
		h.featureWarned[agentID] = true
		log.Printf("agent %s hello: dropping %d unknown feature names (first name %.64q)", agentID, len(dropped), dropped[0])
	}
	return out
}

// learnFeatures records features inferred from received messages and, if a new one is learned, resends the agent to screens.
// Agents need not declare their features. The app hides screens for missing features.
func (h *hub) learnFeatures(a store.AgentAuth, feats ...string) error {
	if len(feats) == 0 {
		return nil
	}
	changed, ag, err := h.store.AddAgentFeatures(a.ID, feats...)
	if err != nil {
		return err
	}
	if changed {
		h.toClients(a.OwnerID, protocol.ClientMessage{Type: "agent", Agent: &ag})
	}
	return nil
}

// retitle changes the title and, if it changed, notifies the screens.
func (h *hub) retitle(ownerID, taskID, title, source string) error {
	changed, err := h.store.SetTaskTitle(taskID, title, source)
	if err != nil || !changed {
		return err
	}
	t, err := h.store.Task(taskID)
	if err == nil {
		h.publishTask(ownerID, t.Task)
	}
	return err
}

// RenameTask is the user renaming a conversation. Titles the agent sends afterwards are ignored.
func (h *hub) RenameTask(ownerID, taskID, title string) (protocol.Task, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.ownedTask(ownerID, taskID); err != nil {
		return protocol.Task{}, err
	}
	clean, ok := protocol.CleanTitle(title)
	if !ok {
		return protocol.Task{}, reject(http.StatusBadRequest, "title must be 1-80 characters")
	}
	if err := h.retitle(ownerID, taskID, clean, store.TitleUser); err != nil {
		return protocol.Task{}, err
	}
	t, err := h.store.Task(taskID)
	return t.Task, err
}

// agentRoom is an agent creating its own room or renaming its own room. It cannot touch other rooms.
func (h *hub) agentRoom(a store.AgentAuth, msg protocol.AgentMessage) error {
	name := roomName(msg.Name)
	if name == "" {
		return nil
	}
	var room protocol.Room
	var err error
	if msg.Type == "room.create" {
		ids, _ := routineIDs(msg.Routines, msg.Routine)
		room, err = h.store.CreateRoom(a.OwnerID, name, a.ID, ids)
	} else {
		r, owner, gerr := h.store.Room(msg.ID)
		if gerr != nil || owner != a.OwnerID || r.AgentID != a.ID {
			h.toAgent(a.ID, protocol.ServerMessage{Type: "error", Reason: "unknown room " + msg.ID})
			return nil
		}
		room, err = h.store.RenameRoom(r.ID, name)
	}
	if err == nil {
		h.toRoom(a.OwnerID, room)
	}
	return err
}

func (h *hub) ownedTask(ownerID, taskID string) (store.OwnedTask, error) {
	t, err := h.store.Task(taskID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && t.OwnerID != ownerID) {
		return t, reject(http.StatusNotFound, "task not found")
	}
	return t, err
}

func (h *hub) requireOnline(agentID string) error {
	if h.agents[agentID] == nil {
		return reject(http.StatusConflict, "agent is offline")
	}
	return nil
}

func (h *hub) StartTask(ownerID, agentID, text, lang string, atts []protocol.Attachment) (protocol.Task, error) {
	return h.StartTaskIn(ownerID, agentID, "", text, lang, atts, "")
}

// withFiles attaches files to the post record kept for the screen.
func withFiles(payload map[string]any, atts []protocol.Attachment) map[string]any {
	if len(atts) > 0 {
		payload["attachments"] = atts
	}
	return payload
}

// titleOf is the conversation title. If there is no text, the first file name.
func titleOf(text string, atts []protocol.Attachment) string {
	if text == "" && len(atts) > 0 {
		text = atts[0].Name
	}
	r := []rune(text)
	if len(r) > 80 {
		r = r[:80]
	}
	return string(r)
}

// StartTaskIn puts a task in room roomID. With an empty string, no room. lang is the app language (ko, en); unknown values are dropped.
// With by set, a room member called the owner's (ownerID) agent. The task is the owner's and records who called.
func (h *hub) StartTaskIn(ownerID, agentID, roomID, text, lang string, atts []protocol.Attachment, by string) (protocol.Task, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.store.AgentOwnedBy(agentID, ownerID) {
		return protocol.Task{}, reject(http.StatusNotFound, "agent not found")
	}
	if err := h.requireOnline(agentID); err != nil {
		return protocol.Task{}, err
	}
	taskID := uuid.NewString()
	if err := h.attach(taskID, atts); err != nil { // reject before creating the task
		return protocol.Task{}, err
	}
	t, err := h.store.CreateTask(ownerID, agentID, titleOf(text, atts), store.NewTask{ID: taskID, RoomID: roomID, AskedBy: by})
	if err != nil {
		h.detach(taskID, atts)
		return protocol.Task{}, err
	}
	h.publishTask(ownerID, t.Task)
	if err := h.record(ownerID, t.ID, "user", "task.start", h.writtenBy(withFiles(map[string]any{"text": text}, atts), by)); err != nil {
		h.detach(taskID, atts)
		return protocol.Task{}, err
	}
	if lang != "ko" && lang != "en" {
		lang = ""
	}
	var context string
	if roomID != "" {
		context = h.roomContext(ownerID, roomID)
	}
	h.toAgent(agentID, h.askedBy(protocol.ServerMessage{Type: "task.start", Task: t.ID, Text: text, Context: context, Lang: lang, Attachments: atts}, by))
	return t.Task, nil
}

// With a nonzero quoteID, SendMessage quotes that event's post. The server finds the quoted text.
// With a nonzero editID, it records that message of mine as edited and sends the agent the edited text along with the original.
// With by set, a room member (agent role) continued the owner's task. Members cannot edit.
func (h *hub) SendMessage(ownerID, taskID, text string, quoteID, editID int64, atts []protocol.Attachment, by string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, err := h.ownedTask(ownerID, taskID)
	if err != nil {
		return err
	}
	if err := h.requireOnline(t.AgentID); err != nil {
		return err
	}
	var context string
	if t.RoutineID != "" {
		if first, spoke := h.store.FirstAgentText(taskID); !spoke {
			context = first
		}
	}
	payload := map[string]any{"text": text}
	var quote string
	if quoteID != 0 {
		_, quoted, ok := h.store.EventText(taskID, quoteID)
		if !ok || quoted == "" {
			return reject(http.StatusBadRequest, "quoted message not found in this task")
		}
		quoted = clipQuote(quoted)
		quote = quoted
		payload["quote"] = map[string]any{"id": quoteID, "text": quoted}
	}
	var edit *protocol.EditInfo
	if editID != 0 && by != "" {
		return reject(http.StatusForbidden, "members cannot edit messages")
	}
	if editID != 0 {
		typ, original, ok := h.store.EventText(taskID, editID)
		if !ok || (typ != "user_message" && typ != "task.start") {
			return reject(http.StatusBadRequest, "edited message not found in this task")
		}
		if text == "" {
			return reject(http.StatusBadRequest, "edited text is empty")
		}
		payload["edit"] = map[string]any{"id": editID}
		quote = original
		edit = &protocol.EditInfo{ID: editID, Original: original}
	}
	if err := h.attach(taskID, atts); err != nil {
		return err
	}
	if err := h.record(ownerID, taskID, "user", "user_message", h.writtenBy(withFiles(payload, atts), by)); err != nil {
		h.detach(taskID, atts)
		return err
	}
	if protocol.TaskFinished(t.State) { // talking to a finished task reopens it
		if err := h.moveTask(ownerID, taskID, protocol.TaskRunning); err != nil {
			return err
		}
	}
	h.toAgent(t.AgentID, h.askedBy(protocol.ServerMessage{Type: "user_message", Task: taskID, Text: text, Context: context, Quote: quote, Edit: edit, Attachments: atts}, by))
	return nil
}

// attach binds a human post's attachments to the task. 400, same as room posts, if a file is already attached to another post.
func (h *hub) attach(taskID string, atts []protocol.Attachment) error {
	err := h.store.AttachUploads(taskID, atts)
	if errors.Is(err, store.ErrUploadUsed) {
		return reject(http.StatusBadRequest, "attachment already used in another message")
	}
	return err
}

// detach undoes attach when the post could not be saved, so the same file can be sent again.
func (h *hub) detach(taskID string, atts []protocol.Attachment) {
	if err := h.store.DetachUploads(taskID, atts); err != nil {
		log.Printf("detach uploads %s: %v", taskID, err)
	}
}

// askedBy tells the agent who called when a member did. Empty for the owner.
func (h *hub) askedBy(m protocol.ServerMessage, by string) protocol.ServerMessage {
	if by != "" {
		m.AskedBy, m.AskedByName = by, h.store.OwnerName(by)
	}
	return m
}

// writtenBy adds the author to a message written by a member. The app shows the name above the bubble.
func (h *hub) writtenBy(payload map[string]any, by string) map[string]any {
	if by != "" {
		payload["by"] = by
		payload["byName"] = h.store.OwnerName(by)
	}
	return payload
}

// HideTask removes a finished conversation from the list. If in progress, it must be stopped first.
func (h *hub) HideTask(ownerID, taskID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, err := h.ownedTask(ownerID, taskID)
	if err != nil {
		return err
	}
	if !protocol.TaskFinished(t.State) {
		return reject(http.StatusConflict, "진행 중인 대화는 멈춘 뒤에 지울 수 있어요")
	}
	audience := h.audience(ownerID, taskID) // after deletion it leaves the room posts and cannot be found
	if err := h.store.HideTask(taskID); err != nil {
		return err
	}
	msg := protocol.ClientMessage{Type: "task.removed", Task: &t.Task}
	h.toClients(ownerID, msg)
	for _, m := range audience {
		h.toClients(m, msg)
	}
	return nil
}

var reactable = map[string]bool{"task.start": true, "user_message": true, "message": true}

// React turns a message reaction on or off. Off if already on. Returns the state after the change.
func (h *hub) React(ownerID, taskID string, target int64, emoji string) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.ownedTask(ownerID, taskID); err != nil {
		return false, err
	}
	if err := validEmoji(emoji); err != nil {
		return false, err
	}
	typ, _, ok := h.store.EventText(taskID, target)
	if !ok || !reactable[typ] {
		return false, reject(http.StatusNotFound, "message not found in this task")
	}
	on := !h.store.ReactionOn(taskID, target, emoji)
	return on, h.record(ownerID, taskID, "user", "reaction", map[string]any{"target": target, "emoji": emoji, "on": on})
}

func (h *hub) AnswerApproval(ownerID, taskID, approvalID, choice string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, err := h.ownedTask(ownerID, taskID)
	if err != nil {
		return err
	}
	pending, err := h.store.PendingApprovals(ownerID)
	if err != nil {
		return err
	}
	var options []string
	found := false
	for _, e := range pending {
		var p struct {
			ID      string   `json:"id"`
			Options []string `json:"options"`
		}
		if e.TaskID == taskID && json.Unmarshal(e.Payload, &p) == nil && p.ID == approvalID {
			options, found = p.Options, true
			break
		}
	}
	if !found {
		return reject(http.StatusNotFound, "no pending approval with that id")
	}
	if !slices.Contains(options, choice) {
		return reject(http.StatusBadRequest, fmt.Sprintf("choice must be one of %v", options))
	}
	if err := h.requireOnline(t.AgentID); err != nil {
		return err
	}
	if err := h.record(ownerID, taskID, "user", "approval_result", map[string]string{"id": approvalID, "choice": choice}); err != nil {
		return err
	}
	if h.store.UnansweredApprovals(taskID) == 0 { // if approvals still await an answer, keep waiting so it does not vanish from the list
		if err := h.moveTask(ownerID, taskID, protocol.TaskRunning); err != nil {
			return err
		}
	}
	h.toAgent(t.AgentID, protocol.ServerMessage{Type: "approval_result", Task: taskID, ID: approvalID, Choice: choice})
	return nil
}

func (h *hub) CancelTask(ownerID, taskID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, err := h.ownedTask(ownerID, taskID)
	if err != nil {
		return err
	}
	if protocol.TaskFinished(t.State) {
		return reject(http.StatusConflict, "task is finished")
	}
	if err := h.record(ownerID, taskID, "user", "cancel", struct{}{}); err != nil {
		return err
	}
	if err := h.moveTask(ownerID, taskID, protocol.TaskCancelled); err != nil {
		return err
	}
	h.toAgent(t.AgentID, protocol.ServerMessage{Type: "cancel", Task: taskID})
	return nil
}

// closeAll drops all connections on shutdown.
func (h *hub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closing = true
	if h.leftover != nil {
		h.leftover.Stop()
	}
	for id, t := range h.gone {
		t.Stop()
		delete(h.gone, id)
	}
	for _, p := range h.agents {
		p.close(websocket.StatusGoingAway, "server shutdown")
	}
	for p := range h.clients {
		p.close(websocket.StatusGoingAway, "server shutdown")
	}
}

// agentInfo extracts kind and model from hello. Rejects if length or count overflows.
func agentInfo(m protocol.AgentMessage) (protocol.AgentInfo, bool) {
	if len(m.Kind) > 40 || len(m.Version) > 40 || len(m.Models) > 10 {
		return protocol.AgentInfo{}, false
	}
	for _, x := range m.Models {
		if x.Role == "" || x.Name == "" || len(x.Role) > 30 || len(x.Name) > 80 {
			return protocol.AgentInfo{}, false
		}
	}
	return protocol.AgentInfo{Kind: m.Kind, Version: m.Version, Models: m.Models}, true
}
