package hermesbridge

import "testing"

func TestFailReason(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`{"output": "", "exit_code": -1, "error": "BLOCKED: Command timed out without user response. The user has NOT consented."}`, "승인 응답 없음 (시간 초과)"},
		{`{"output": "", "exit_code": -1, "error": "BLOCKED: User denied this command."}`, "사용자가 거절"},
		{`{"output": "", "exit_code": -1, "error": "BLOCKED: Failed to send approval request to user"}`, "승인 단계에서 막힘"},
		{`{"output": "x\n[Command timed out after 60s]", "exit_code": 124}`, "60초 시간 초과"},
		{`{"output": "aaaa", "exit_code": 1, "exit_code_meaning": "No matches found (not an error)"`, "종료 코드 1: No matches found (not an error)"},
		{`{"output": "aaaa`, ""},
	} {
		if got := failReason(c.in); got != c.want {
			t.Errorf("%s\n got %q want %q", c.in, got, c.want)
		}
	}
}
