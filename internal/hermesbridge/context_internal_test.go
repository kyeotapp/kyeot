package hermesbridge

import (
	"strings"
	"testing"
)

func TestWithContextHeadByKind(t *testing.T) {
	if got := withContext("question", "A question from room Lounge.", true); !strings.HasPrefix(got, "아래는 이 질문이 나온 방의 배경이다.") || !strings.HasSuffix(got, "question") {
		t.Fatalf("the background of a first question is introduced as room background: %q", got)
	}
	if got := withContext("reply", "routine result", false); !strings.HasPrefix(got, "아래는 네가 루틴으로 보낸 결과다.") {
		t.Fatalf("the background of a follow-up is the routine result: %q", got)
	}
	if got := withContext("question", "", true); got != "question" {
		t.Fatalf("without background it is unchanged: %q", got)
	}
}
