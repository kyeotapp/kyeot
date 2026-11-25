package hermesbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

var choiceLabels = map[string]string{
	"once":    "한 번 허용",
	"session": "이번 세션 허용",
	"always":  "항상 허용",
	"deny":    "거절",
}

type approval struct {
	requestID  string
	choices    map[string]string // button label → Hermes choice
	clarify    bool              // clarify question (choices); answered via /clarify
	generation json.RawMessage
	msg        protocol.AgentMessage // resent on reconnect if it could not be sent while disconnected
	delivered  bool
}

const restartedApproval = "연결이 다시 시작되면서 이 확인 요청이 끊겼어요. 같은 일을 다시 맡겨 주세요. / The connection restarted, so this request was lost. Please ask again."

func (b *Bridge) answer(ctx context.Context, task, id, label string) {
	b.mu.Lock()
	r := b.runs[task]
	var ap approval
	ok := false
	if r != nil {
		ap, ok = r.approvals[id]
	}
	_, justEnded := b.ended[task]
	b.mu.Unlock()
	if !ok {
		log.Printf("unknown approval %s/%s", task, id)
		if r == nil && !justEnded { // restart made us forget that run; the answer cannot reach Hermes, so close the task rather than leave it running
			b.fail(task, restartedApproval)
		}
		return
	}
	var err error
	if ap.clarify {
		err = b.hermes.clarify(ctx, r.id, ap.requestID, ap.generation, label)
	} else {
		choice := ap.choices[label]
		if choice == "" {
			choice = "deny"
		}
		err = b.hermes.approve(ctx, r.id, choice, ap.requestID)
	}
	if se := (*statusError)(nil); errors.As(err, &se) && se.code == http.StatusConflict && strings.Contains(se.body, "approval_not_pending") {
		b.mu.Lock()
		delete(r.approvals, id)
		b.mu.Unlock()
		b.send(protocol.AgentMessage{Type: "message", Task: task, Text: "승인 시간이 지나 이미 거절됐어요. 다시 하려면 말로 요청해 주세요."})
		return
	}
	if err != nil {
		b.send(protocol.AgentMessage{Type: "message", Task: task, Text: "승인 전달 실패: " + err.Error()})
		return
	}
	b.mu.Lock()
	delete(r.approvals, id)
	b.mu.Unlock()
	b.publishStatus()
}

func (b *Bridge) requestApproval(task string, ev hermesEvent, said string) {
	choices := ev.Choices
	if len(choices) == 0 {
		choices = []string{"once", "deny"}
	}
	ap := approval{requestID: ev.RequestID, choices: map[string]string{}}
	var labels []string
	scopes := map[string]string{}
	for _, c := range choices {
		label := firstNonEmpty(choiceLabels[c], c)
		ap.choices[label] = c
		labels = append(labels, label)
		if n := scopeNotes[c]; n != "" {
			scopes[label] = n
		}
	}
	kinds := ev.PatternKeys
	if len(kinds) == 0 && ev.PatternKey != "" {
		kinds = []string{ev.PatternKey}
	}

	question := firstNonEmpty(ev.Desc, "승인이 필요합니다")
	if ev.Command != "" {
		question += "\n" + ev.Command
	}
	b.mu.Lock()
	ask := b.asks[task]
	b.mu.Unlock()
	b.offerApproval(task, ap, protocol.AgentMessage{Type: "approval", Task: task, Question: question, Options: labels,
		Context: approvalContext(said, ask), Request: clip(firstLine(ask), 200), Impact: approvalImpact(kinds, ev.Desc, ev.Command), Scopes: scopes})
}

// offerApproval attaches the approval to the run and sends it; if disconnected, attach resends it on reconnect.
func (b *Bridge) offerApproval(task string, ap approval, msg protocol.AgentMessage) {
	b.mu.Lock()
	b.approval++
	msg.ID = fmt.Sprintf("ap-%d", b.approval)
	ap.msg = msg
	r := b.runs[task]
	if r != nil {
		r.approvals[msg.ID] = ap
	}
	b.mu.Unlock()
	if b.send(msg) && r != nil {
		b.mu.Lock()
		if cur, ok := r.approvals[msg.ID]; ok {
			cur.delivered = true
			r.approvals[msg.ID] = cur
		}
		b.mu.Unlock()
	}
	b.publishStatus()
}

// requestChoice sends a Hermes clarify question as choices (kind "choice"); with no choices it sends only the question, to be answered in text.
func (b *Bridge) requestChoice(task string, ev hermesEvent) {
	question := firstNonEmpty(ev.Question, ev.Desc, "어떻게 할까요?")
	if len(ev.Choices) == 0 {
		b.send(protocol.AgentMessage{Type: "message", Task: task, Text: question})
		return
	}
	ap := approval{requestID: ev.RequestID, clarify: true, generation: ev.Generation, choices: map[string]string{}}
	for _, c := range ev.Choices {
		ap.choices[c] = c
	}
	b.offerApproval(task, ap, protocol.AgentMessage{Type: "approval", Kind: "choice", Task: task, Question: question, Options: ev.Choices})
}
