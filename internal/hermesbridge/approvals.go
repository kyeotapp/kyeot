package hermesbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

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
		err = b.hermes.approve(ctx, r.id, choice, ap.requestID, ap.generation)
	}
	b.mu.Lock()
	delete(r.approvals, id)
	b.mu.Unlock()
	var se *statusError
	switch {
	case err == nil:
		b.publishStatus()
	case errors.As(err, &se) && se.code == http.StatusConflict && strings.Contains(se.body, "approval_not_pending"):
		b.send(protocol.AgentMessage{Type: "message", Task: task, Text: "이 확인 요청은 이미 처리됐거나 승인 시간이 지났어요. 다시 하려면 말로 요청해 주세요."})
		b.publishStatus()
	case se == nil || se.code >= 500: // network or Hermes hiccup: ask again
		log.Printf("approval %s/%s: %v", task, id, err)
		b.send(protocol.AgentMessage{Type: "message", Task: task, Text: "승인을 잠시 전달하지 못했어요. 다시 골라 주세요."})
		ap.delivered = false
		b.offerApproval(ctx, task, r, ap, ap.msg)
	case strings.Contains(se.body, "stale_generation"):
		b.closeRun(ctx, task, r, "이 확인 요청은 이미 지나간 실행의 것이라 전달하지 못했어요. 작업을 멈췄으니 다시 말로 요청해 주세요.")
	default: // Hermes will never take this answer; stop rather than leave the task waiting forever
		b.closeRun(ctx, task, r, "승인을 전달하지 못해 작업을 멈췄어요. 다시 말로 요청해 주세요. ("+err.Error()+")")
	}
}

// closeRun stops a run whose approval cannot be delivered and ends the task as failed.
func (b *Bridge) closeRun(ctx context.Context, task string, r *run, text string) {
	b.mu.Lock()
	if b.runs[task] != r { // the run already ended and reported its own end
		b.mu.Unlock()
		b.send(protocol.AgentMessage{Type: "message", Task: task, Text: "이 확인 요청은 이미 끝난 실행의 것이라 전달하지 못했어요."})
		return
	}
	r.closed = true
	b.mu.Unlock()
	b.fail(task, text) // before the run leaves b.runs: an idle status first would close the task as done
	b.mu.Lock()
	if b.runs[task] == r {
		delete(b.runs, task)
		delete(b.asks, task)
	}
	if b.ended == nil {
		b.ended = map[string]time.Time{}
	}
	b.ended[task] = time.Now()
	b.mu.Unlock()
	b.denyPending(ctx, r)
	if err := b.hermes.stop(ctx, r.id); err != nil {
		log.Printf("stop %s: %v", r.id, err)
	}
	b.publishStatus()
}

// supersedeIfWaiting marks the run as taken over when it waits on an approval, before anything can end it.
func (b *Bridge) supersedeIfWaiting(r *run) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ap := range r.approvals {
		if !ap.clarify {
			r.superseded = true
			return true
		}
	}
	return false
}

// denyPending answers deny to the run's open approvals (not clarify questions) so Hermes is not left waiting; reports whether there were any.
func (b *Bridge) denyPending(ctx context.Context, r *run) bool {
	b.mu.Lock()
	var pending []approval
	for id, ap := range r.approvals {
		if !ap.clarify {
			pending = append(pending, ap)
			delete(r.approvals, id)
		}
	}
	b.mu.Unlock()
	for _, ap := range pending {
		if err := b.hermes.approve(ctx, r.id, "deny", ap.requestID, ap.generation); err != nil {
			log.Printf("deny %s: %v", r.id, err)
		}
	}
	return len(pending) > 0
}

func (b *Bridge) requestApproval(ctx context.Context, task string, r *run, ev hermesEvent, said string) {
	choices := ev.Choices
	if len(choices) == 0 {
		choices = []string{"once", "deny"}
	}
	ap := approval{requestID: ev.RequestID, generation: ev.Generation, choices: map[string]string{}}
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
	b.offerApproval(ctx, task, r, ap, protocol.AgentMessage{Type: "approval", Task: task, Question: question, Options: labels,
		Context: approvalContext(said, ask), Request: clip(firstLine(ask), 200), Impact: approvalImpact(kinds, ev.Desc, ev.Command), Scopes: scopes})
}

// offerApproval attaches the approval to its run and sends it; if disconnected, attach resends it on reconnect.
// A run that was replaced or closed offers nothing: its answer would reach the wrong run.
func (b *Bridge) offerApproval(ctx context.Context, task string, r *run, ap approval, msg protocol.AgentMessage) {
	b.mu.Lock()
	if r.superseded || r.closed || b.runs[task] != r {
		b.mu.Unlock()
		log.Printf("approval from replaced run %s denied", r.id)
		if !ap.clarify { // nobody will answer it; keep Hermes from holding the session
			go b.hermes.approve(ctx, r.id, "deny", ap.requestID, ap.generation)
		}
		return
	}
	b.approval++
	msg.ID = fmt.Sprintf("ap-%s-%d", b.apPrefix, b.approval)
	ap.msg = msg
	r.approvals[msg.ID] = ap
	b.mu.Unlock()
	if b.send(msg) {
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
func (b *Bridge) requestChoice(ctx context.Context, task string, r *run, ev hermesEvent) {
	question := firstNonEmpty(ev.Question, ev.Desc, "어떻게 할까요?")
	if len(ev.Choices) == 0 {
		b.send(protocol.AgentMessage{Type: "message", Task: task, Text: question})
		return
	}
	ap := approval{requestID: ev.RequestID, clarify: true, generation: ev.Generation, choices: map[string]string{}}
	for _, c := range ev.Choices {
		ap.choices[c] = c
	}
	b.offerApproval(ctx, task, r, ap, protocol.AgentMessage{Type: "approval", Kind: "choice", Task: task, Question: question, Options: ev.Choices})
}
