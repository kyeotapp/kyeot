// Package hermesbridge attaches Hermes as a kyeot agent.
// The deck side is a websocket agent; the Hermes side uses api_server /v1/runs.
package hermesbridge

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/sensor/disk"
	"github.com/kyeotapp/kyeot/internal/sensor/netwatch"
	"github.com/kyeotapp/kyeot/internal/sensor/system"
)

type Config struct {
	DeckURL           string
	AgentToken        string
	HermesURL         string
	HermesKey         string
	HermesHome        string                              // where routines and costs are read; empty means none are sent
	HermesBin         string                              // hermes command for toggling routines (default \"hermes\")
	RoutineRunTimeout time.Duration                       // "run now" timeout (default 30m)
	RunsEvery         time.Duration                       // routine result check interval (default 30s)
	ReportEvery       time.Duration                       // report interval (default 5m)
	UIAddr            string                              // local address (loopback only); empty means not opened
	UIRouter          *UIRouter                           // if set, attaches here under UIKey instead of UIAddr (several profiles on one computer)
	UIKey             string                              // screen address key (/h/<key>) of a non-default profile
	UIProfile         bool                                // non-default profile; does not attach without a key
	UITimeout         time.Duration                       // how long the local address waits for the deck reply (default 5s)
	TitleDelays       []time.Duration                     // when to re-read the title after a conversation ends (default 3s, 15s, 60s)
	PingEvery         time.Duration                       // deck connection check interval (default 30s)
	SensorDir         string                              // sensor record folder (sensors in the data folder); empty means sensors off
	SensorProbe       netwatch.Prober                     // for tests; empty measures with this computer's commands
	DiskMeasure       func() (disk.Usage, error)          // for tests
	SystemMeasure     func(context.Context) system.Sample // for tests
}

// ErrAgentRevoked means the deck rejected the agent token (401).
var ErrAgentRevoked = errors.New("deck 이 에이전트 토큰을 거절했다. 토큰이 폐기됐으니 앱에서 새 코드로 다시 연결한다 (kyeot connect)")

const (
	maxRoutineText = 64 << 10
	maxMessageText = 128 << 10 // stays under the server 1MB limit even if JSON escaping grows it 6x
	maxPending     = 200
	workQueue      = 64 // per-task work queue
	endedKeep      = time.Minute
	maxTitles      = 2000
)

// run is the Hermes run for one deck task. A task is one conversation, so the run changes many times.
type run struct {
	id         string
	approvals  map[string]approval
	superseded bool // taken over by a new run after 409
}

type Bridge struct {
	cfg    Config
	hermes *hermes

	mu       sync.Mutex
	conn     *websocket.Conn
	runs     map[string]*run // task id → active run
	approval int
	titles   map[string]string                      // task id → last title sent
	asks     map[string]string                      // task id → user's last message
	pending  []protocol.AgentMessage                // replies and task.end queued while disconnected
	sent     map[string]bool                        // routine results already sent
	workers  map[string]chan protocol.ServerMessage // per-task processing order
	running  map[string]bool                        // routines running now
	ended    map[string]time.Time                   // tasks whose run just ended, so an approval answer arriving in that gap is not mistaken for a restart

	uiMu        sync.Mutex                  // one local-address request at a time
	uiWait      chan protocol.ServerMessage // the request awaiting a reply now; guarded by b.mu
	uiListening bool

	sensors       []sensor
	sensorRunning bool
}

// sensor is one bundle kyeot measures itself; the agent is not involved (sensor template).
type sensor struct {
	bundle, feature string
	run             func(context.Context)
	refresh         func()
}

// sensorReq tracks data.put sent by a sensor; its ui.ack is not routed to the local-address queue
const sensorReq = "sensor"

func New(cfg Config) *Bridge {
	b := &Bridge{
		cfg:     cfg,
		hermes:  &hermes{base: strings.TrimRight(cfg.HermesURL, "/"), key: cfg.HermesKey, http: &http.Client{}},
		runs:    map[string]*run{},
		titles:  map[string]string{},
		asks:    map[string]string{},
		sent:    map[string]bool{},
		workers: map[string]chan protocol.ServerMessage{},
		running: map[string]bool{},
	}
	if cfg.SensorDir != "" {
		nw := netwatch.New(filepath.Join(cfg.SensorDir, netwatch.BundleName), b.pushSensor(netwatch.BundleName))
		if cfg.SensorProbe != nil {
			nw.Probe = cfg.SensorProbe
		}
		dk := disk.New(b.pushSensor(disk.BundleName))
		if cfg.DiskMeasure != nil {
			dk.Measure = cfg.DiskMeasure
		}
		sy := system.New(b.pushSensor(system.BundleName))
		if cfg.SystemMeasure != nil {
			sy.Measure = cfg.SystemMeasure
		}
		b.sensors = []sensor{
			{netwatch.BundleName, protocol.FeatureSensorNetwork, nw.Run, nw.Refresh},
			{disk.BundleName, protocol.FeatureSensorDisk, dk.Run, dk.Refresh},
			{system.BundleName, protocol.FeatureSensorSystem, sy.Run, sy.Refresh},
		}
	}
	return b
}

// pushSensor uploads a sensor bundle the same way as an agent bundle (data.put). Dropped while disconnected; measured again on reconnect.
func (b *Bridge) pushSensor(name string) func(json.RawMessage) {
	return func(data json.RawMessage) {
		b.send(protocol.AgentMessage{Type: "data.put", Name: name, Data: data, Req: sensorReq})
	}
}

func sessionID(task string) string { return "deck-" + task }

// Run stays attached to the deck until ctx ends, reconnecting if dropped.
func (b *Bridge) Run(ctx context.Context) error {
	b.mu.Lock()
	listen := b.cfg.UIAddr != "" && !b.uiListening && b.cfg.UIRouter == nil
	b.mu.Unlock()
	if b.cfg.UIRouter != nil {
		if detach, err := b.cfg.UIRouter.attach(ctx, b.cfg.UIKey, b.cfg.UIProfile, b); err != nil {
			log.Printf("cannot open local address, screen upload is off: %v", err)
		} else {
			defer detach()
			b.mu.Lock()
			b.uiListening = true
			b.mu.Unlock()
		}
	}
	if listen {
		if _, err := b.ListenUI(ctx); err != nil { // conversations continue even if the screen endpoint cannot open
			log.Printf("cannot open local address, screen upload is off: %v", err)
		}
	}
	go b.cleanUploads(ctx)
	b.mu.Lock()
	startSensors := !b.sensorRunning
	b.sensorRunning = true
	b.mu.Unlock()
	if startSensors {
		for _, s := range b.sensors {
			go s.run(ctx)
		}
	}
	backoff := time.Second
	for {
		started := time.Now()
		err := b.session(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		limit := 30 * time.Second
		if errors.Is(err, ErrAgentRevoked) {
			limit, backoff = 10*time.Minute, max(backoff, time.Minute)
		}
		backoff = min(backoff, limit)
		log.Printf("deck connection lost (%v); reconnecting in %s", err, backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, limit)
	}
}

func (b *Bridge) session(ctx context.Context) error {
	url := "ws" + strings.TrimPrefix(strings.TrimRight(b.cfg.DeckURL, "/"), "http") + "/agent"
	conn, res, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + b.cfg.AgentToken}},
	})
	if err != nil {
		if res != nil && res.StatusCode == http.StatusUnauthorized {
			return ErrAgentRevoked
		}
		return err
	}
	conn.SetReadLimit(1 << 20)
	defer conn.CloseNow()

	if err := b.attach(conn); err != nil {
		return err
	}
	log.Print("deck connected")
	pctx, stopPing := context.WithCancel(ctx)
	defer stopPing()
	go b.pingLoop(pctx, conn)
	b.publishStatus()
	if b.cfg.HermesHome != "" || len(b.sensors) > 0 {
		b.sendHello(ctx)
	}
	for _, s := range b.sensors {
		s.refresh() // push current values on the new connection right away
	}
	if b.cfg.HermesHome != "" {
		b.sendRecentTitles()
		watch, stop := context.WithCancel(ctx)
		defer stop()
		go b.watchRoutines(watch)
	}

	for {
		var msg protocol.ServerMessage
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			b.mu.Lock()
			if b.conn == conn {
				b.conn = nil
			}
			b.mu.Unlock()
			return err
		}
		b.dispatch(ctx, msg)
	}
}

// attach first sends messages queued while disconnected, in order, then opens the connection. Unsent approvals are resent too.
func (b *Bridge) attach(conn *websocket.Conn) error {
	for {
		b.mu.Lock()
		queued := b.pending
		b.pending = nil
		if len(queued) == 0 {
			b.conn = conn
			b.mu.Unlock()
			break
		}
		b.mu.Unlock()
		for i, m := range queued {
			if err := writeMsg(conn, m); err != nil {
				b.mu.Lock()
				b.pending = append(queued[i:], b.pending...)
				b.mu.Unlock()
				return err
			}
		}
	}
	b.mu.Lock()
	var again []protocol.AgentMessage
	for _, r := range b.runs {
		for id, ap := range r.approvals {
			if !ap.delivered {
				again = append(again, ap.msg)
				ap.delivered = true
				r.approvals[id] = ap
			}
		}
	}
	b.mu.Unlock()
	slices.SortFunc(again, func(x, y protocol.AgentMessage) int {
		return cmp.Or(cmp.Compare(len(x.ID), len(y.ID)), cmp.Compare(x.ID, y.ID))
	})
	for _, m := range again {
		b.send(m)
	}
	return nil
}

// pingLoop detects half-dead connections early and closes them; the read loop then reconnects.
func (b *Bridge) pingLoop(ctx context.Context, conn *websocket.Conn) {
	every := cmp.Or(b.cfg.PingEvery, 30*time.Second)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, every)
			err := conn.Ping(pctx)
			cancel()
			if err != nil && ctx.Err() == nil {
				log.Printf("deck ping failed: %v", err)
				conn.CloseNow()
				return
			}
		}
	}
}

// dispatch hands slow work to a per-task goroutine, keeping order within one task.
func (b *Bridge) dispatch(ctx context.Context, msg protocol.ServerMessage) {
	switch msg.Type {
	case "ui.ack", "ui.state", "error", protocol.TypeDataRefresh:
		b.handle(ctx, msg)
		return
	}
	key := "task:" + msg.Task
	if strings.HasPrefix(msg.Type, "routine.") {
		key = "routine"
	}
	b.mu.Lock()
	ch := b.workers[key]
	if ch == nil {
		ch = make(chan protocol.ServerMessage, workQueue)
		b.workers[key] = ch
		go b.work(ctx, key, ch)
	}
	full := false
	select {
	case ch <- msg:
	default:
		full = true
	}
	b.mu.Unlock()
	if !full {
		return
	}
	log.Printf("work queue full, dropping %s", msg.Type)
	const dropped = "앞선 요청이 너무 많이 밀려 이 메시지를 받지 못했어요. 잠시 뒤에 다시 보내 주세요."
	switch {
	case msg.Task == "":
	case msg.Type == "task.start":
		b.fail(msg.Task, dropped)
	case msg.Type == "user_message": // earlier work keeps running, so the task is not closed
		b.send(protocol.AgentMessage{Type: "message", Task: msg.Task, Text: dropped})
	}
}

func (b *Bridge) work(ctx context.Context, key string, ch chan protocol.ServerMessage) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-ch:
			b.handle(ctx, m)
		case <-time.After(time.Minute):
			b.mu.Lock()
			if len(ch) == 0 {
				delete(b.workers, key)
				b.mu.Unlock()
				return
			}
			b.mu.Unlock()
		}
	}
}

// send writes on the current connection, returning true if sent. A failed write drops that connection.
// Unsent replies (message) and task.end are queued and sent right after reconnect; the rest are dropped.
func (b *Bridge) send(msg protocol.AgentMessage) bool {
	msg.Text = clipText(msg.Text, maxMessageText)
	b.mu.Lock()
	conn := b.conn
	b.mu.Unlock()
	if conn != nil {
		err := writeMsg(conn, msg)
		if err == nil {
			return true
		}
		log.Printf("deck send failed: %v", err)
	}
	b.mu.Lock()
	if conn != nil && b.conn == conn {
		b.conn = nil // later messages queue too; reconnect (attach) sends them in order
	}
	if msg.Type == "message" || msg.Type == "task.end" {
		b.pending = trimPending(append(b.pending, msg))
	}
	b.mu.Unlock()
	if conn != nil {
		conn.CloseNow()
	}
	return false
}

// trimPending drops the oldest replies past maxPending. task.end is kept to the end, since dropping it leaves the task running.
func trimPending(q []protocol.AgentMessage) []protocol.AgentMessage {
	over := len(q) - maxPending
	if over <= 0 {
		return q
	}
	log.Printf("too many messages queued while disconnected, dropping the oldest %d", over)
	out := q[:0]
	for _, m := range q {
		if over > 0 && m.Type != "task.end" {
			over--
			continue
		}
		out = append(out, m)
	}
	return slices.Delete(out, 0, over) // only when all were task.end
}

func writeMsg(conn *websocket.Conn, msg protocol.AgentMessage) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return wsjson.Write(ctx, conn, msg)
}

// clipText cuts at a character boundary past n bytes and appends a marker.
func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n\n…(잘림)"
}

func (b *Bridge) publishStatus() {
	b.mu.Lock()
	state := "idle"
	for _, r := range b.runs {
		state = "working"
		if len(r.approvals) > 0 {
			state = "blocked"
			break
		}
	}
	b.mu.Unlock()
	b.send(protocol.AgentMessage{Type: "status", State: state})
}

func (b *Bridge) handle(ctx context.Context, msg protocol.ServerMessage) {
	switch msg.Type {
	case "task.start", "user_message":
		b.mu.Lock()
		b.asks[msg.Task] = askOf(msg.Text)
		b.mu.Unlock()
		body := msg.Text
		if msg.Edit != nil { // edited message: quote the original to Hermes and add a note line
			body = protocol.EditNote + body
		}
		text := withContext(withQuote(body, msg.Quote), msg.Context, msg.Type == "task.start") + b.attachNote(ctx, msg.Attachments)
		if msg.Type == "task.start" || msg.Context != "" { // first input of this conversation
			text += deckGuide
			b.mu.Lock()
			ui := b.uiListening
			b.mu.Unlock()
			if ui {
				text += deckUIGuide
			}
		}
		b.userInput(ctx, msg.Task, text, runInstructions(msg.Lang))
	case "approval_result":
		b.answer(ctx, msg.Task, msg.ID, msg.Choice)
	case "routine.toggle":
		if msg.On != nil {
			b.toggleRoutine(ctx, msg.Routine, *msg.On)
		}
	case "routine.remove":
		b.routineCommand(ctx, msg.Routine, "remove")
	case protocol.TypeRoutineTrigger:
		b.runRoutine(ctx, msg.Routine)
	case "routine.create":
		b.createRoutine(ctx, msg.Name, msg.Schedule, msg.Text)
	case "cancel":
		b.mu.Lock()
		r := b.runs[msg.Task]
		b.mu.Unlock()
		if r != nil {
			if err := b.hermes.stop(ctx, r.id); err != nil {
				log.Printf("stop %s: %v", r.id, err)
			}
		}
	case protocol.TypeDataRefresh:
		for _, s := range b.sensors {
			if s.bundle == msg.Name {
				s.refresh()
			}
		}
	case "ui.ack", "ui.state":
		if msg.Req == sensorReq {
			if msg.OK != nil && !*msg.OK {
				logOnce("sensor-ack", "sensor bundle %s was rejected: %s", msg.ID, msg.Reason)
			}
			return
		}
		b.uiReply(msg)
	case "error":
		if !b.titleOnly(msg.Reason) {
			log.Printf("deck error: %s", msg.Reason)
		}
	}
}

// userInput steers the active run if there is one, else opens a new run in the same session.
func (b *Bridge) userInput(ctx context.Context, task, text, instructions string) {
	b.mu.Lock()
	r := b.runs[task]
	b.mu.Unlock()
	if r != nil {
		err := b.hermes.steer(ctx, r.id, text)
		var se *statusError
		if err == nil {
			return
		}
		if !errors.As(err, &se) || se.code != http.StatusConflict {
			b.fail(task, "방향 수정 실패: "+err.Error())
			return
		}
		b.mu.Lock()
		r.superseded = true
		b.mu.Unlock()
	}
	runID, err := b.hermes.startRun(ctx, text, sessionID(task), instructions)
	if err != nil {
		b.fail(task, "Hermes 실행 실패: "+err.Error())
		return
	}
	nr := &run{id: runID, approvals: map[string]approval{}}
	b.mu.Lock()
	b.runs[task] = nr
	b.mu.Unlock()
	b.publishStatus()
	go b.stream(ctx, task, nr)
}

func (b *Bridge) fail(task, text string) {
	log.Print(text)
	b.send(protocol.AgentMessage{Type: "message", Task: task, Text: text})
	b.send(protocol.AgentMessage{Type: "task.end", Task: task, Result: protocol.TaskFailed})
}

// stream translates Hermes events into deck messages until the run ends.
func (b *Bridge) stream(ctx context.Context, task string, self *run) {
	runID := self.id
	var reply strings.Builder
	var lastDraft time.Time
	result, finalText := "", ""
	calls := 0
	began := map[string]time.Time{} // start time per tool; Hermes duration sometimes arrives as 0
	unexplained := map[string]failedCall{}

	err := b.hermes.events(ctx, runID, func(ev hermesEvent) bool {
		switch ev.Event {
		case "tool.started":
			calls++
			began[callID(ev, calls)] = time.Now()
			b.send(protocol.AgentMessage{Type: "tool", Task: task, Call: callID(ev, calls), Phase: "started",
				Name: ev.Tool, Summary: firstNonEmpty(ev.Preview, ev.Tool)})
		case "tool.completed":
			failed := ev.Error == true
			success := !failed
			took := ev.Duration
			if t0, ok := began[callID(ev, calls)]; ok {
				took = max(took, time.Since(t0).Seconds())
				delete(began, callID(ev, calls))
			}
			summary := fmt.Sprintf("%.1f초", took)
			if failed {
				summary += " · 실패"
				if reason := failReason(ev.Preview); reason != "" {
					summary = failSummary(summary, reason)
				} else {
					unexplained[callID(ev, calls)] = failedCall{ev.Tool, summary}
				}
			}
			b.send(protocol.AgentMessage{Type: "tool", Task: task, Call: callID(ev, calls), Phase: "completed",
				Name: ev.Tool, Summary: summary, OK: &success})
		case "subagent.start":
			calls++
			b.send(protocol.AgentMessage{Type: "tool", Task: task, Call: subCall(ev, calls), Phase: "started",
				Name: "subagent", Summary: firstNonEmpty(ev.Goal, "하위 에이전트")})
		case "subagent.complete":
			ok := !slices.Contains(subagentFailed, ev.Status)
			summary := firstNonEmpty(ev.Summary, "완료")
			if !ok {
				summary = firstNonEmpty(ev.Summary, ev.Status) + " · 실패"
			}
			b.send(protocol.AgentMessage{Type: "tool", Task: task, Call: subCall(ev, calls), Phase: "completed",
				Name: "subagent", Summary: summary, OK: &ok})
		case "approval.request":
			b.requestApproval(task, ev, reply.String())
		case "clarify.request":
			b.requestChoice(task, ev)
		case "message.delta":
			reply.WriteString(ev.Delta)
			if time.Since(lastDraft) > draftEvery {
				lastDraft = time.Now()
				if text := draftText(reply.String()); text != "" {
					b.send(protocol.AgentMessage{Type: "draft", Task: task, Text: text})
				}
			}
		case "run.completed":
			result, finalText = protocol.TaskDone, firstNonEmpty(ev.Output, reply.String())
		case "run.failed", "run.interrupted":
			result, finalText = protocol.TaskFailed, firstNonEmpty(errText(ev.Error), ev.ExitReason, "실행 실패")
		case "run.cancelled":
			result, finalText = protocol.TaskCancelled, draftText(reply.String()) // keep the reply received until the stop
		}
		return result == ""
	})

	b.mu.Lock()
	cur := b.runs[task]
	replaced := self.superseded || (cur != nil && cur != self)
	if cur == self {
		delete(b.runs, task)
		delete(b.asks, task)
		now := time.Now()
		for k, at := range b.ended {
			if now.Sub(at) > endedKeep {
				delete(b.ended, k)
			}
		}
		if b.ended == nil {
			b.ended = map[string]time.Time{}
		}
		b.ended[task] = now
	}
	b.mu.Unlock()

	if result == "" {
		result, finalText = protocol.TaskFailed, fmt.Sprintf("Hermes 이벤트 스트림이 끊겼다: %v", err)
	}
	finalText, extra := takeDirectives(task, finalText)
	if finalText != "" {
		b.send(protocol.AgentMessage{Type: "message", Task: task, Text: finalText})
	}
	for _, m := range extra {
		b.send(m)
	}
	if !replaced { // the run that took over reports the end
		b.send(protocol.AgentMessage{Type: "task.end", Task: task, Result: result})
	}
	b.publishStatus()
	if b.cfg.HermesHome != "" {
		go b.followTitle(ctx, task)
		if len(unexplained) > 0 {
			go b.explainFailures(ctx, task, unexplained)
		}
	}
}

// On the first reply to a routine result, withContext prepends that result to the question; Hermes has never seen it.
// withContext prepends background to the question: for the first question (task.start) it is the room posts, for a follow-up it is the routine result.
func withContext(text, context string, start bool) string {
	if context == "" {
		return text
	}
	head := "아래는 네가 루틴으로 보낸 결과다."
	if start {
		head = "아래는 이 질문이 나온 방의 배경이다. 질문이 이 글들을 가리킬 수 있다."
	}
	return head + "\n\n" + context + "\n\n---\n\n" + text
}

// withQuote prepends the quoted original of a reply as a markdown quote.
func withQuote(text, quote string) string {
	if quote == "" {
		return text
	}
	return "> " + strings.ReplaceAll(quote, "\n", "\n> ") + "\n\n" + text
}

var subagentFailed = []string{"failed", "error", "timeout"}

func subCall(ev hermesEvent, n int) string {
	if ev.SubagentID != "" {
		return "sub-" + ev.SubagentID
	}
	return fmt.Sprintf("sub-%d", n)
}

var (
	logMu   sync.Mutex
	logLast = map[string]string{}
)

// logOnce records a repeated error at the same place only the first time.
func logOnce(where, format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	logMu.Lock()
	defer logMu.Unlock()
	if logLast[where] == text {
		return
	}
	logLast[where] = text
	log.Print(text)
}

func callID(ev hermesEvent, n int) string {
	if ev.ToolCallID != "" {
		return ev.ToolCallID
	}
	return fmt.Sprintf("%s-%d", ev.Tool, n)
}

func errText(v any) string {
	s, _ := v.(string)
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
