package hermesbridge

import (
	"regexp"
	"strings"
	"time"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

// deckGuide is the note appended to the first input of a new conversation. Hermes does not know the app protocol, so it uses app features through markers in its reply.
const deckGuide = "\n\n[곁 앱 안내 — 사용자에게는 보이지 않는다 / Kyeot app note — hidden from the user]\n" +
	"This conversation comes from the Kyeot app. Reply in the user's language. Only when needed, put a marker below at the end of your reply; the app handles it and removes the marker.\n" +
	"이 대화는 곁 앱에서 왔다. 사용자와 같은 언어로 답한다. 필요할 때만 답 끝에 아래 표시를 쓰면 앱이 처리하고 표시는 지운다.\n" +
	"- React to the user's message with an emoji / 이모지로 반응: <deck:react emoji=\"👍\"/>\n" +
	"- Create a room (to collect routine results or talk by topic; name it in the user's language) / 방 만들기: <deck:room name=\"room name\" routine=\"routine id, omit if none\"/>"

// deckUIGuide is added only by a bridge that actually opened the screen address (7701); the skill description alone may not make Hermes pick the skill for a tab request.
const deckUIGuide = "\n- To make or change app tabs, screens, or dashboards, read and follow the deck-ui skill / 앱의 탭, 화면, 대시보드를 만들거나 고칠 때는 deck-ui 스킬을 읽고 따른다"

var (
	directive = regexp.MustCompile(`<deck:(react|room)((?:\s+[a-z]+="[^"]*")*)\s*/>`)
	attr      = regexp.MustCompile(`([a-z]+)="([^"]*)"`)
)

// takeDirectives strips markers from a reply and turns them into messages for the app.
func takeDirectives(task, text string) (string, []protocol.AgentMessage) {
	var out []protocol.AgentMessage
	clean := directive.ReplaceAllStringFunc(text, func(m string) string {
		parts := directive.FindStringSubmatch(m)
		a := map[string]string{}
		for _, kv := range attr.FindAllStringSubmatch(parts[2], -1) {
			a[kv[1]] = kv[2]
		}
		switch parts[1] {
		case "react":
			if a["emoji"] != "" {
				out = append(out, protocol.AgentMessage{Type: "reaction", Task: task, Emoji: a["emoji"]})
			}
		case "room":
			if a["name"] != "" {
				out = append(out, protocol.AgentMessage{Type: "room.create", Name: a["name"], Routine: a["routine"]})
			}
		}
		return ""
	})
	return strings.TrimSpace(clean), out
}

// draftEvery is the interval for sending an in-progress reply to the screen.
const draftEvery = 150 * time.Millisecond

// draftText removes app markers from an in-progress reply, including a marker not fully arrived yet (a trailing "<deck…").
func draftText(text string) string {
	text, _ = takeDirectives("", text)
	if i := strings.LastIndex(text, "<deck"); i >= 0 && !strings.Contains(text[i:], "/>") {
		text = strings.TrimSpace(text[:i])
	}
	return text
}
