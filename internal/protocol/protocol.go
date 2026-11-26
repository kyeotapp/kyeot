// Package protocol defines the messages exchanged between agents, the server, and the screen.
// Field names must match the app's TypeScript definitions (app/src/lib/protocol.ts).
package protocol

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

// agent → server
type AgentMessage struct {
	Type     string   `json:"type"`
	Task     string   `json:"task,omitempty"`
	State    string   `json:"state,omitempty"`
	Title    string   `json:"title,omitempty"`
	Text     string   `json:"text,omitempty"`
	Call     string   `json:"call,omitempty"`
	Phase    string   `json:"phase,omitempty"`
	Name     string   `json:"name,omitempty"`
	Summary  string   `json:"summary,omitempty"`
	Detail   string   `json:"detail,omitempty"`
	OK       *bool    `json:"ok,omitempty"`
	ID       string   `json:"id,omitempty"`
	Question string   `json:"question,omitempty"`
	Options  []string `json:"options,omitempty"`
	URL      string   `json:"url,omitempty"`
	Kind     string   `json:"kind,omitempty"`
	Result   string   `json:"result,omitempty"`
	// approval — one-line summary, the raw request, what changes, scope per button. Without them the screen uses only question
	Context string            `json:"context,omitempty"`
	Request string            `json:"request,omitempty"`
	Impact  []string          `json:"impact,omitempty"`
	Scopes  map[string]string `json:"scopes,omitempty"`
	// reaction — emoji attached to the user's last message
	Emoji string `json:"emoji,omitempty"`
	// hello — the agent's own info. Kind is the type. Features is optional (if omitted, the server infers from received messages)
	Version  string       `json:"version,omitempty"`
	Models   []AgentModel `json:"models,omitempty"`
	Features []string     `json:"features,omitempty"`

	// routine.run — result of one routine run. Stored once per (agent, routine, run)
	Routine     string `json:"routine,omitempty"`
	RoutineName string `json:"routineName,omitempty"`
	Run         string `json:"run,omitempty"`
	At          int64  `json:"at,omitempty"`
	Quiet       bool   `json:"quiet,omitempty"` // run that ended with nothing to send; it stays in run history only and doesn't become a conversation (text may be empty)
	// room.create — routines that send results to the room (merged with the old routine)
	Routines []string `json:"routines,omitempty"`

	// report — routine list and costs. The server normalizes it with internal/report, stores it, and passes it to the screen (format: Report in app/src/lib/protocol.ts)
	Report json.RawMessage `json:"report,omitempty"`

	// ui.*, data.* — optional. If given, it is echoed back in ui.ack (tells concurrent requests apart)
	Req string `json:"req,omitempty"`
	// ui.put — screen blueprint
	Screen json.RawMessage `json:"screen,omitempty"`
	// ui.put — screen slot: tab, off, hidden. If empty, the existing placement rule applies (a new screen goes at the end of the tabs if there is room)
	Place string `json:"place,omitempty"`
	// ui.part.put — composed part blueprint
	Part json.RawMessage `json:"part,omitempty"`
	// ui.layout — order of screen ids shown in the tab bar
	Tabs []string `json:"tabs,omitempty"`
	// ui.revert — version number to restore (from ui.versions)
	To int `json:"to,omitempty"`
	// data.put — bundle value (JSON object); the bundle name is Name
	Data json.RawMessage `json:"data,omitempty"`
}

// server → agent
type ServerMessage struct {
	Type   string `json:"type"`
	Task   string `json:"task,omitempty"`
	Text   string `json:"text,omitempty"`
	ID     string `json:"id,omitempty"`
	Choice string `json:"choice,omitempty"`
	Reason string `json:"reason,omitempty"`
	// raw routine result, sent with the first reply to it; the agent is seeing this conversation for the first time
	Context string `json:"context,omitempty"`
	// original text the user quoted in a reply
	Quote string `json:"quote,omitempty"`
	// task.start — app language (ko, en); asks for the conversation title in this language
	Lang string `json:"lang,omitempty"`
	// task.start, user_message — the member, if a shared-room member called the owner's agent; empty for the owner
	AskedBy     string `json:"askedBy,omitempty"`
	AskedByName string `json:"askedByName,omitempty"`
	// user_message — the user edited an earlier message. Text is the edited text, Quote the original. The agent may ignore it
	Edit *EditInfo `json:"edit,omitempty"`
	// ui.ack — echoes the req the agent attached to ui.*, data.*
	Req string `json:"req,omitempty"`
	// routine.toggle — turns a routine on or off
	Routine string `json:"routine,omitempty"`
	On      *bool  `json:"on,omitempty"`
	// routine.create — name, schedule (five cron fields or "every 2h"); the task is Text
	Name     string `json:"name,omitempty"`
	Schedule string `json:"schedule,omitempty"`
	// files the user attached. URL is a path relative to the deck address, so the agent downloads them
	Attachments []Attachment `json:"attachments,omitempty"`
	// whether ui.ack succeeded; the failure reason is Reason
	OK *bool `json:"ok,omitempty"`
	// ui.state — {screens, tabs, data}. ui.ack for ui.versions — {versions}
	UI json.RawMessage `json:"ui,omitempty"`
}

// DataWarning is a place where the bundle doesn't match the blueprint's $data slot. Path is the path the blueprint reads.
type DataWarning struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// EditInfo holds the original event number and text of an edited message.
type EditInfo struct {
	ID       int64  `json:"id"`
	Original string `json:"original"`
}

// Attachment is one uploaded file. Only a party that knows its number can fetch it.
type Attachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
	URL  string `json:"url,omitempty"`
}

// server → screen
type ClientMessage struct {
	Type  string `json:"type"`
	Agent *Agent `json:"agent,omitempty"`
	Task  *Task  `json:"task,omitempty"`
	Event *Event `json:"event,omitempty"`
	// for report
	AgentID string          `json:"agentId,omitempty"`
	Report  json.RawMessage `json:"report,omitempty"`
	Room    *Room           `json:"room,omitempty"`
	// for draft: the reply the agent is writing (not stored)
	TaskID string `json:"taskId,omitempty"`
	Text   string `json:"text,omitempty"`
	// for room.message: messages between room members (the agent doesn't act)
	RoomMessage *RoomMessage `json:"roomMessage,omitempty"`
	// for join.request, join.done: sent to the admin only
	Join *JoinRequest `json:"join,omitempty"`
	// for room.report: sent only to the person who handles it
	RoomReport *RoomReport `json:"roomReport,omitempty"`
}

// JoinRequest is a request from someone without an account to join with a non-admin's room code. The admin approves it.
type JoinRequest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	RoomID      string `json:"roomId"`
	RoomName    string `json:"roomName"`
	InviterName string `json:"inviterName"`
	CreatedAt   int64  `json:"createdAt"`
}

type RoomMessage struct {
	ID        int64  `json:"id"`
	RoomID    string `json:"roomId"`
	OwnerID   string `json:"ownerId"`
	Name      string `json:"name"`
	Text      string `json:"text"`
	CreatedAt int64  `json:"createdAt"`
	// the fields below can be ignored by old apps(rooms.reply, rooms.edit, rooms.react, rooms.files)
	Quote       *RoomQuote     `json:"quote,omitempty"`
	Edited      bool           `json:"edited,omitempty"`
	Reactions   []RoomReaction `json:"reactions,omitempty"`
	Attachments []Attachment   `json:"attachments,omitempty"`
	// rooms.delete, rooms.report, rooms.block, rooms.filter. the first three and Filtered differ per viewer
	Removed  string `json:"removed,omitempty"`  // owner: the room owner deleted it, leaving a placeholder; self: the author deleted it (websocket only; the app removes the message)
	Blocked  bool   `json:"blocked,omitempty"`  // the viewer blocked the author
	Reported bool   `json:"reported,omitempty"` // the viewer reported this message
	Filtered bool   `json:"filtered,omitempty"` // hit the room's banned words (not applied to the author)
}

// RoomReport is a report on a room message or a person. It goes only to the handler (the room owner, or the server owner if the target is the room owner).
type RoomReport struct {
	ID           int64    `json:"id"`
	RoomID       string   `json:"roomId"`
	RoomName     string   `json:"roomName"`
	TargetID     string   `json:"targetId"`
	TargetName   string   `json:"targetName"`
	ReporterName string   `json:"reporterName"`          // empty if the handler is the target (the server owner was reported)
	TargetOwner  bool     `json:"targetOwner,omitempty"` // the target is the room owner and can't be removed
	MessageID    int64    `json:"messageId,omitempty"`
	MessageText  string   `json:"messageText,omitempty"`
	Reason       string   `json:"reason"` // spam, harassment, inappropriate, other
	Note         string   `json:"note,omitempty"`
	CreatedAt    int64    `json:"createdAt"`
	Actions      []string `json:"actions"` // empty means an open report; otherwise delete, remove, dismiss
	HandledAt    int64    `json:"handledAt,omitempty"`
}

// RoomQuote is a quoted message from someone in the same room. Text is the original cut at 300 characters.
type RoomQuote struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
}

// RoomReaction is the tally of one reaction. Mine says whether the recipient pressed it.
type RoomReaction struct {
	Emoji string `json:"emoji"`
	Count int    `json:"count"`
	Mine  bool   `json:"mine"`
}

type Room struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	AgentID string `json:"agentId"`
	// old-app compatibility: first value of RoutineIDs
	RoutineID  string   `json:"routineId"`
	RoutineIDs []string `json:"routineIds"`
	CreatedAt  int64    `json:"createdAt"`
	LastAt     int64    `json:"lastAt"`
	Shared     bool     `json:"shared,omitempty"`    // joined another's room as a subscriber; read-only
	OwnerName  string   `json:"ownerName,omitempty"` // room owner's name when Shared
	CanAsk     bool     `json:"canAsk,omitempty"`    // when Shared, whether one can ask the owner's agent (agent member)
	Pinned     *int     `json:"pinned,omitempty"`    // only in GET /api/rooms; the viewer's pin order
	GroupID    string   `json:"groupId,omitempty"`   // only in GET /api/rooms; the viewer's group
	FilterOff  bool     `json:"filterOff,omitempty"` // rooms.filter: the room owner turned off banned-word masking (default on)
}

type Agent struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	State    string `json:"state"`
	LastSeen *int64 `json:"lastSeen"`
	// "preset:robot" is a default character, or "data:image/jpeg;base64,…" is a photo
	Avatar string `json:"avatar"`
	// the agent's own info announced via hello; nil if not announced
	Info *AgentInfo `json:"info,omitempty"`
	// Features inferred by the server from received messages: report → routines, costs / ui.put, data.put → ui / routine.run → routines.
	// the agent may also announce them upfront via hello's features. The app hides screens for missing features
	Features []string `json:"features,omitempty"`
}

// Agent feature names. report and declared are the basis for judging a feature absent — it counts as absent only once a report was received or features were declared in hello
const (
	FeatureRoutines = "routines"
	FeatureCosts    = "costs"
	FeatureUI       = "ui"
	FeatureReport   = "report"   // a report was received at least once
	FeatureDeclared = "declared" // declared via hello's features
)

// AgentInfo holds the agent kind and the models it uses. There can be several models, one per role (chat, delegate, title, etc.).
type AgentInfo struct {
	Kind    string       `json:"kind,omitempty"`
	Version string       `json:"version,omitempty"`
	Models  []AgentModel `json:"models,omitempty"`
}

type AgentModel struct {
	Role string `json:"role"`
	Name string `json:"name"`
}

type Task struct {
	ID        string `json:"id"`
	AgentID   string `json:"agentId"`
	Title     string `json:"title"`
	State     string `json:"state"`
	RoomID    string `json:"roomId,omitempty"`
	RoutineID string `json:"routineId,omitempty"`
	AskedBy   string `json:"askedBy,omitempty"`  // the member, if a room member called the owner's agent
	Question  string `json:"question,omitempty"` // only in room message lists; first user message (first 400 characters)
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	Pinned    *int   `json:"pinned,omitempty"`  // only in GET /api/tasks; the viewer's pin order
	GroupID   string `json:"groupId,omitempty"` // only in GET /api/tasks; the viewer's group
}

// RoutineRun is one history line of a quiet run (routine.run quiet)(GET /api/agents/{id}/routines/{routine}/history)
type RoutineRun struct {
	At     int64  `json:"at"`
	Status string `json:"status"`
}

type Event struct {
	ID        int64           `json:"id"`
	TaskID    string          `json:"taskId"`
	Source    string          `json:"source"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt int64           `json:"createdAt"`
}

const (
	TaskRunning   = "running"
	TaskWaiting   = "waiting"
	TaskDone      = "done"
	TaskFailed    = "failed"
	TaskCancelled = "cancelled"

	AgentOffline = "offline"
	AgentIdle    = "idle"
)

func TaskFinished(state string) bool {
	return state == TaskDone || state == TaskFailed || state == TaskCancelled
}

var required = map[string][]string{
	"status":         {"state"},
	"hello":          {},
	"task.open":      {"task", "title"},
	"task.title":     {"task", "title"},
	"message":        {"task", "text"},
	"tool":           {"task", "call", "phase", "name", "summary"},
	"approval":       {"task", "id", "question"},
	"artifact":       {"task", "name", "url"},
	"task.end":       {"task", "result"},
	"routine.run":    {"routine", "run"}, // text is required unless quiet (below)
	"report":         {},
	"reaction":       {"task", "emoji"},
	"draft":          {"task", "text"},
	"room.create":    {"name"},
	"room.rename":    {"id", "name"},
	"ui.put":         {},
	"ui.remove":      {"id"},
	"ui.layout":      {},
	"ui.list":        {},
	"ui.part.put":    {},
	"ui.part.remove": {"id"},
	"ui.versions":    {"id"},
	"ui.revert":      {"id"},
	"data.put":       {"name"},
	"data.remove":    {"name"},
}

// Places are the place values of ui.put.
var Places = []any{"tab", "off", "hidden"}

var enums = map[string][]string{
	"state":  {"idle", "working", "blocked"},
	"phase":  {"started", "completed"},
	"result": {TaskDone, TaskFailed, TaskCancelled},
}

// ParseAgentMessage returns the message and the payload to store (raw fields minus type and task) if the format is valid.
func ParseAgentMessage(raw []byte) (AgentMessage, json.RawMessage, bool) {
	var msg AgentMessage
	var fields map[string]any
	if json.Unmarshal(raw, &msg) != nil || json.Unmarshal(raw, &fields) != nil {
		return msg, nil, false
	}
	need, known := required[msg.Type]
	if !known {
		return msg, nil, false
	}
	for _, f := range need {
		v, ok := fields[f].(string)
		if !ok || v == "" {
			return msg, nil, false
		}
		if allowed, has := enums[f]; has && !slices.Contains(allowed, v) {
			return msg, nil, false
		}
	}
	if msg.Type == "routine.run" && msg.Text == "" && !msg.Quiet {
		return msg, nil, false
	}
	if msg.Type == "approval" && len(msg.Options) == 0 {
		return msg, nil, false
	}
	if msg.Type == "report" {
		if _, ok := fields["report"].(map[string]any); !ok {
			return msg, nil, false
		}
	}
	if msg.Type == "ui.put" {
		if _, ok := fields["screen"].(map[string]any); !ok {
			return msg, nil, false
		}
		if p, has := fields["place"]; has && !slices.Contains(Places, p) {
			return msg, nil, false
		}
	}
	if msg.Type == "ui.part.put" {
		if _, ok := fields["part"].(map[string]any); !ok {
			return msg, nil, false
		}
	}
	if msg.Type == "data.put" {
		if _, ok := fields["data"].(map[string]any); !ok {
			return msg, nil, false
		}
	}
	if msg.Type == "ui.revert" && msg.To < 1 {
		return msg, nil, false
	}
	if msg.Type == "ui.layout" {
		if _, ok := fields["tabs"].([]any); !ok {
			return msg, nil, false
		}
	}
	delete(fields, "type")
	delete(fields, "task")
	delete(fields, "attachments") // only people attach files
	payload, _ := json.Marshal(fields)
	return msg, payload, true
}

var (
	cronField = `[0-9*,/\-]{1,20}`
	schedule  = regexp.MustCompile(`^(` + cronField + `( ` + cronField + `){4}|(every )?[1-9][0-9]{0,2}[mh])$`)
)

// CleanTitle collapses line breaks and runs of whitespace into one space and trims the ends. ok is false unless it is 1-80 characters.
func CleanTitle(s string) (string, bool) {
	s = strings.Join(strings.Fields(s), " ")
	n := len([]rune(s))
	return s, n >= 1 && n <= 80
}

// ValidSchedule checks that a routine schedule is five cron fields ("0 9 * * 1-5") or an interval ("every 2h", "30m").
func ValidSchedule(s string) bool { return schedule.MatchString(s) }

// EditNote is the notice line the Hermes bridge prefixes to an edited message. The server sends the edit field and leaves the text unchanged.
const EditNote = "(바로 위 인용한 내 말을 이렇게 고칠게)\n"

// BlockedPerson is one line of the block list (rooms.block). Blocks are per server.
type BlockedPerson struct {
	OwnerID   string `json:"ownerId"`
	Name      string `json:"name"`
	Avatar    string `json:"avatar,omitempty"`
	CreatedAt int64  `json:"createdAt"`
}
