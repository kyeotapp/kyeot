package hermesbridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

// The preview of tool.completed is the first 500 chars of the result JSON, so exit_code at the end is cut off for long output; then read it from state.db
var failDelays = []time.Duration{0, 3 * time.Second}

var (
	exitCodeRe = regexp.MustCompile(`"exit_code":\s*(-?\d+)`)
	meaningRe  = regexp.MustCompile(`"exit_code_meaning":\s*"((?:[^"\\]|\\.)*)"`)
	timedOutRe = regexp.MustCompile(`\[Command timed out after (\d+)s\]`)
)

type toolOutcome struct {
	ExitCode    *int   `json:"exit_code"`
	Meaning     string `json:"exit_code_meaning"`
	Error       any    `json:"error"`
	Message     string `json:"message"`
	UserSummary string `json:"user_summary"`
	Output      string `json:"output"`
}

// parseOutcome parses the result JSON; for truncated JSON it looks only for the exit code in the remaining text.
func parseOutcome(result string) toolOutcome {
	var r toolOutcome
	if json.Unmarshal([]byte(result), &r) == nil {
		return r
	}
	r = toolOutcome{Output: result}
	if m := exitCodeRe.FindStringSubmatch(result); m != nil {
		code, _ := strconv.Atoi(m[1])
		r.ExitCode = &code
	}
	if m := meaningRe.FindStringSubmatch(result); m != nil {
		r.Meaning, _ = strconv.Unquote(`"` + m[1] + `"`)
	}
	return r
}

// blockedReason is why Hermes blocked at the approval step; only fixed wording is returned, not the original text
func blockedReason(r toolOutcome) string {
	s, _ := r.Error.(string)
	switch {
	case !strings.HasPrefix(s, "BLOCKED:"):
		return ""
	case strings.Contains(s, "timed out without user response"):
		return "승인 응답 없음 (시간 초과)"
	case strings.Contains(s, "User denied"):
		return "사용자가 거절"
	}
	return "승인 단계에서 막힘"
}

// exitReason looks only at approval blocks, timeouts and exit codes, so it is safe on raw state.db text that has not been through secret redaction.
func exitReason(r toolOutcome) string {
	if s := blockedReason(r); s != "" {
		return s
	}
	if m := timedOutRe.FindStringSubmatch(r.Output); m != nil {
		return m[1] + "초 시간 초과"
	}
	if r.ExitCode == nil || *r.ExitCode == 0 {
		return ""
	}
	if *r.ExitCode == 124 {
		return "시간 초과 (종료 코드 124)"
	}
	if r.Meaning != "" {
		return fmt.Sprintf("종료 코드 %d: %s", *r.ExitCode, oneLine(r.Meaning, 60))
	}
	return fmt.Sprintf("종료 코드 %d", *r.ExitCode)
}

// failReason extracts a short failure reason from the preview Hermes redacted; "" if none is found.
func failReason(preview string) string {
	r := parseOutcome(preview)
	if r.UserSummary != "" {
		return oneLine(r.UserSummary, 80)
	}
	if s := exitReason(r); s != "" {
		return s
	}
	if s, _ := r.Error.(string); s != "" {
		return oneLine(s, 80)
	}
	return oneLine(r.Message, 80)
}

// toolResult is the raw tool result stored in state.db; "" if none.
func toolResult(home, task, call string) string {
	db, err := openStateDB(home)
	if err != nil {
		return ""
	}
	defer db.Close()
	var content string
	err = db.QueryRow(`SELECT coalesce(content, '') FROM messages WHERE session_id = ? AND role = 'tool' AND tool_call_id = ? ORDER BY id DESC LIMIT 1`,
		sessionID(task), call).Scan(&content)
	if err != nil && err != sql.ErrNoRows {
		logOnce("toolfail", "could not read state.db tool results: %v", err)
	}
	return content
}

type failedCall struct {
	name, summary string
}

// explainFailures finds failed tools that got no reason in state.db and resends them under the same call; the app overwrites the completed of the same call.
func (b *Bridge) explainFailures(ctx context.Context, task string, calls map[string]failedCall) {
	var waited time.Duration
	for _, d := range failDelays {
		select {
		case <-ctx.Done():
			return
		case <-time.After(d - waited):
		}
		waited = d
		for call, f := range calls {
			if reason := exitReason(parseOutcome(toolResult(b.cfg.HermesHome, task, call))); reason != "" {
				ok := false
				b.send(protocol.AgentMessage{Type: "tool", Task: task, Call: call, Phase: "completed",
					Name: f.name, Summary: failSummary(f.summary, reason), OK: &ok})
				delete(calls, call)
			}
		}
		if len(calls) == 0 {
			return
		}
	}
}

func oneLine(s string, n int) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// failSummary appends the reason after a summary like "61.3s · failed".
func failSummary(base, reason string) string {
	if reason = strings.TrimSpace(reason); reason == "" {
		return base
	}
	return base + ": " + reason
}
