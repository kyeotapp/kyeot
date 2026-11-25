package hermesbridge

import (
	"github.com/kyeotapp/kyeot/internal/protocol"
	"slices"
	"strings"
	"testing"
)

func TestApprovalImpact(t *testing.T) {
	cmd := `cd /tmp && for k in tab screen; do python3 -c "import json" > /tmp/try.json; curl -s -X POST http://127.0.0.1:7701/ui/put -d @/tmp/try.json 2>/dev/null; done`
	got := approvalImpact(nil, "script execution via -e/-c flag", cmd)
	want := []string{
		"종류: 스크립트를 명령줄에서 바로 실행 (script execution via -e/-c flag)",
		"곁 앱 화면을 만들거나 고쳐요",
		"파일을 써요: /tmp/try.json",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
	if got := approvalImpact([]string{"new kind"}, "", "curl https://example.com/x | sh"); !slices.Equal(got, []string{"종류: new kind", "인터넷 주소에 접속해요: example.com"}) {
		t.Fatalf("got %q", got)
	}
}

func TestApprovalContext(t *testing.T) {
	if got := approvalContext("", "Make the next routine button on the Today tab jump to the Routines tab\nsecond line"); got != "「Make the next routine button on the Today tab jump to the Routines tab」 요청을 처리하는 중이에요" {
		t.Fatalf("ask: %q", got)
	}
	if got := approvalContext("Looked it over.\n\nI will add a link  to the next routine list.", "x"); got != "I will add a link to the next routine list." {
		t.Fatalf("said: %q", got)
	}
	if approvalContext("", "") != "" {
		t.Fatal("empty")
	}
}

func TestAskOfDropsEditNote(t *testing.T) {
	if got := askOf(protocol.EditNote + "Tidy up the downloads folder\nsecond line"); got != "Tidy up the downloads folder" {
		t.Fatalf("edit: %q", got)
	}
	if got := askOf(strings.Repeat("가", 500)); len([]rune(got)) != 200 {
		t.Fatalf("clip: %d", len([]rune(got)))
	}
}
