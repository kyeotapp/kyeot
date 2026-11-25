// Package fakeagent is a test agent. On a task it imitates one tool call → approval request → result → end.
package fakeagent

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

type Agent struct {
	Conn     *websocket.Conn
	Errors   chan string
	partial  sync.Map    // in-progress reply to keep when stopped
	Contexts chan string // context attached to user_message
	Quotes   chan string // quote attached to user_message
	Toggles  chan string // routine.toggle as "id=on"
	Acks     chan protocol.ServerMessage
	States   chan protocol.ServerMessage // ui.state
	// Demo sends a report and routine results right after connecting, and answers tasks in markdown without approval.
	// Tasks containing "삭제", "결제", "보내" still ask for approval. For screen development.
	Demo bool
	// Bundles are data bundles uploaded via data.put right after connecting. For screen tests
	Bundles map[string]json.RawMessage
}

func Dial(ctx context.Context, serverURL, token string) (*Agent, error) {
	url := "ws" + strings.TrimPrefix(serverURL, "http") + "/agent"
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		return nil, err
	}
	return &Agent{Conn: conn, Errors: make(chan string, 16), Contexts: make(chan string, 16), Quotes: make(chan string, 16), Toggles: make(chan string, 16), Acks: make(chan protocol.ServerMessage, 16), States: make(chan protocol.ServerMessage, 16)}, nil
}

// Send sends the raw message as-is. Tests use it to send rule-breaking messages.
func (a *Agent) Send(ctx context.Context, v any) error { return wsjson.Write(ctx, a.Conn, v) }

func (a *Agent) Close() { a.Conn.Close(websocket.StatusNormalClosure, "") }

// Run reacts to server messages until the connection drops.
func (a *Agent) Run(ctx context.Context) error {
	ok := true
	send := func(m protocol.AgentMessage) { a.Send(ctx, m) }
	if a.Demo {
		for _, m := range demoOpening(time.Now()) {
			send(m)
		}
	}
	for name, data := range a.Bundles {
		send(protocol.AgentMessage{Type: "data.put", Name: name, Data: data})
	}
	for {
		var msg protocol.ServerMessage
		if err := wsjson.Read(ctx, a.Conn, &msg); err != nil {
			return err
		}
		switch msg.Type {
		case "task.start":
			if a.Demo && strings.Contains(msg.Text, "골라") {
				send(protocol.AgentMessage{Type: "message", Task: msg.Task, Text: "좋아요. 하나만 골라 주세요!"})
				send(protocol.AgentMessage{Type: "approval", Kind: "choice", Task: msg.Task, ID: "choice-1", Question: "로그인 방식을 알려 주시면 이어서 할게요",
					Options: []string{"카카오로 로그인", "네이버로 로그인", "이메일로 로그인", "기억 안 남"}})
				send(protocol.AgentMessage{Type: "status", State: "blocked"})
				continue
			}
			if strings.Contains(msg.Text, "긴 답") {
				send(protocol.AgentMessage{Type: "message", Task: msg.Task, Text: longAnswer})
				send(protocol.AgentMessage{Type: "task.end", Task: msg.Task, Result: protocol.TaskDone})
				continue
			}
			if strings.Contains(msg.Text, "제목 바꿔") { // like Hermes, the agent makes up a new conversation title
				send(protocol.AgentMessage{Type: "task.title", Task: msg.Task, Title: "에이전트가 지은 제목"})
				send(protocol.AgentMessage{Type: "message", Task: msg.Task, Text: "제목을 새로 지었어요"})
				send(protocol.AgentMessage{Type: "task.end", Task: msg.Task, Result: protocol.TaskDone})
				continue
			}
			if a.Demo && !needsApproval(msg.Text) {
				go demoAnswer(ctx, send, msg.Task, msg.Text)
				continue
			}
			send(protocol.AgentMessage{Type: "status", State: "working"})
			send(protocol.AgentMessage{Type: "tool", Task: msg.Task, Call: "c1", Phase: "started", Name: "calendar.list", Summary: "내일 일정 조회"})
			send(protocol.AgentMessage{Type: "tool", Task: msg.Task, Call: "c1", Phase: "completed", Name: "calendar.list", Summary: "3건", OK: &ok})
			send(protocol.AgentMessage{Type: "approval", Task: msg.Task, ID: "a1", Question: "19시에 예약할까요?", Options: []string{"승인", "거절"}})
			send(protocol.AgentMessage{Type: "status", State: "blocked"})
		case "approval_result":
			result := protocol.TaskCancelled
			if msg.Choice == "승인" || msg.ID == "choice-1" {
				result = protocol.TaskDone
			}
			send(protocol.AgentMessage{Type: "status", State: "working"})
			send(protocol.AgentMessage{Type: "message", Task: msg.Task, Text: "「" + msg.Choice + "」 받았습니다"})
			send(protocol.AgentMessage{Type: "task.end", Task: msg.Task, Result: result})
			send(protocol.AgentMessage{Type: "status", State: "idle"})
		case "user_message":
			if msg.Edit != nil { // like the bridge, prefix a notice line to the edited message; the server only reports it via the edit field
				msg.Text = protocol.EditNote + msg.Text
			}
			select {
			case a.Contexts <- msg.Context:
			default:
			}
			select {
			case a.Quotes <- msg.Quote:
			default:
			}
			if a.Demo {
				go demoAnswer(ctx, send, msg.Task, msg.Text)
				continue
			}
			if strings.Contains(msg.Text, "멈출") { // simulates stopping mid-write; on cancel, keep what was written so far
				a.partial.Store(msg.Task, "멈추기 전까지 쓴 답")
				send(protocol.AgentMessage{Type: "status", State: "working"})
				send(protocol.AgentMessage{Type: "draft", Task: msg.Task, Text: "멈추기 전까지 쓴 답"})
				continue
			}
			if strings.Contains(msg.Text, "긴 승인") { // like Hermes, first line is the reason, the rest a long command
				cmd := strings.Repeat("for k in tab screen to go id; do python3 -c \"print(1)\" > /tmp/try.json; done\n", 30)
				send(protocol.AgentMessage{Type: "status", State: "working"})
				send(protocol.AgentMessage{Type: "approval", Task: msg.Task, ID: "long-1", Question: "script execution via -e/-c flag\n" + cmd, Options: []string{"한 번 허용", "거절", "이번 세션 허용", "항상 허용"}})
				continue
			}
			if strings.Contains(msg.Text, "설명 승인") { // like the bridge, with context, impact, and per-button scope
				cmd := strings.Repeat("python3 -c \"print(1)\" > /tmp/try.json; curl -s -X POST http://127.0.0.1:7701/ui/put -d @/tmp/try.json\n", 20)
				send(protocol.AgentMessage{Type: "status", State: "working"})
				send(protocol.AgentMessage{Type: "approval", Task: msg.Task, ID: "ctx-1", Question: "script execution via -e/-c flag\n" + cmd,
					Options: []string{"한 번 허용", "거절", "이번 세션 허용", "항상 허용"},
					Context: "오늘 한눈에 탭의 다음 루틴 목록에 루틴 탭으로 가는 링크를 붙일게요.", Request: msg.Text,
					Impact: []string{"종류: 스크립트를 명령줄에서 바로 실행 (script execution via -e/-c flag)", "곁 앱 화면을 만들거나 고쳐요", "파일을 써요: /tmp/try.json"},
					Scopes: map[string]string{"이번 세션 허용": "이번 답이 끝날 때까지 같은 종류의 명령은 다시 묻지 않아요",
						"항상 허용": "앞으로 모든 대화에서 같은 종류의 명령을 묻지 않아요. Hermes 설정에 남아요"}})
				continue
			}
			if strings.Contains(msg.Text, "안 끝나는 도구") { // a tool that never finishes, as if stuck on a Mac confirmation dialog; ended by cancel
				a.partial.Store(msg.Task, "도구를 멈췄어요")
				send(protocol.AgentMessage{Type: "status", State: "working"})
				send(protocol.AgentMessage{Type: "tool", Task: msg.Task, Call: "stuck", Phase: "started", Name: "terminal", Summary: "find ~ -name Composer.tsx"})
				continue
			}
			if strings.Contains(msg.Text, "천천히") { // shows the reply being written bit by bit in screen tests
				go func(task string) {
					for _, part := range []string{"천천히", "천천히 쓰는", "천천히 쓰는 중이에요"} {
						send(protocol.AgentMessage{Type: "draft", Task: task, Text: part})
						time.Sleep(700 * time.Millisecond)
					}
					send(protocol.AgentMessage{Type: "message", Task: task, Text: "다 썼어요: 천천히 쓴 답"})
					send(protocol.AgentMessage{Type: "task.end", Task: task, Result: protocol.TaskDone})
				}(msg.Task)
				continue
			}
			if strings.Contains(msg.Text, "긴 답") {
				send(protocol.AgentMessage{Type: "message", Task: msg.Task, Text: longAnswer})
				send(protocol.AgentMessage{Type: "task.end", Task: msg.Task, Result: protocol.TaskDone})
				continue
			}
			if strings.Contains(msg.Text, "고마워") {
				send(protocol.AgentMessage{Type: "reaction", Task: msg.Task, Emoji: "👏"})
			}
			send(protocol.AgentMessage{Type: "message", Task: msg.Task, Text: "들었습니다: " + msg.Text + attachedNames(msg.Attachments)})
			send(protocol.AgentMessage{Type: "task.end", Task: msg.Task, Result: protocol.TaskDone})
		case "routine.create":
			select {
			case a.Toggles <- msg.Name + "=created@" + msg.Schedule:
			default:
			}
		case "routine.remove":
			select {
			case a.Toggles <- msg.Routine + "=removed":
			default:
			}
		case protocol.TypeRoutineTrigger:
			select {
			case a.Toggles <- msg.Routine + "=run":
			default:
			}
		case protocol.TypeDataRefresh:
			select {
			case a.Toggles <- msg.Name + "=refresh":
			default:
			}
		case "routine.toggle":
			select {
			case a.Toggles <- msg.Routine + "=" + map[bool]string{true: "on", false: "off"}[msg.On != nil && *msg.On]:
			default:
			}
		case "ui.ack":
			select {
			case a.Acks <- msg:
			default:
			}
		case "ui.state":
			select {
			case a.States <- msg:
			default:
			}
		case "cancel":
			if p, ok := a.partial.LoadAndDelete(msg.Task); ok {
				send(protocol.AgentMessage{Type: "message", Task: msg.Task, Text: p.(string)})
				send(protocol.AgentMessage{Type: "task.end", Task: msg.Task, Result: protocol.TaskCancelled})
			}
			send(protocol.AgentMessage{Type: "status", State: "idle"})
		case "error":
			select {
			case a.Errors <- msg.Reason:
			default:
			}
		}
	}
}

// attachedNames appends the received file names to the end of the reply. Screen tests use it to see whether attachments reached the agent.
func attachedNames(atts []protocol.Attachment) string {
	if len(atts) == 0 {
		return ""
	}
	names := make([]string, len(atts))
	for i, a := range atts {
		names[i] = a.Name
	}
	return " (첨부: " + strings.Join(names, ", ") + ")"
}

// A reply long enough to fill the screen. Mixes lists and code, with a marker line at the end
const longAnswer = "검증 결과를 정리해요. 한 단계는 막혀서 못 끝냈어요.\n\n확인된 것\n\n" +
	"- 수동 실행 1회: cron과 같은 전용 venv(`~/.local/share/suraksan-venv`)로 돌렸고 종료 코드 0, 17.6초 걸렸어요.\n" +
	"- 상태 파일: runs 129→130, fails 0, paused false로 정상 갱신됐어요.\n" +
	"- cron 기록: 빈자리 감시 실행 112건이 모두 completed예요.\n" +
	"- 스크립트 문법 컴파일 통과.\n\n아직 확인 못 한 것\n\n" +
	"- 달 목록을 실제로 읽었는지는 로그가 없어서 조용히 넘어갔어요.\n\n" +
	"```\npython3 watch.py --once --verbose\n```\n\n" +
	"다음에 돌 때 로그를 남기게 해 둘게요.\n\n마지막 줄 표식: 접힘 끝"
