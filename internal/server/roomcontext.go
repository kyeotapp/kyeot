package server

import (
	"cmp"
	"encoding/json"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

// size limits of the background attached to the first question in a room
const (
	roomContextPosts = 5
	roomContextTalk  = 5
	roomContextLine  = 400  // one post
	roomContextTotal = 2500 // total; when over, oldest lines are dropped first
	roomContextRule  = 300  // one routine description line
)

// roomContext is the room name, linked routines, and recent posts (agent replies, routine results, person talk); posts without a reply (including just-created tasks) are left out.
// The agent doesn't know the room, so this lets it understand a first question that points at room posts, like "the item not yet scheduled".
func (h *hub) roomContext(ownerID, roomID string) string {
	room, _, err := h.store.Room(roomID)
	if err != nil {
		return ""
	}
	type line struct {
		at   int64
		text string
	}
	var lines []line
	posts, _ := h.store.RoomPosts(room, ownerID, roomContextPosts+1)
	for _, p := range posts {
		if strings.TrimSpace(p.Preview) == "" {
			continue
		}
		q := p.Task.Question
		if strings.TrimSpace(q) == "" {
			q = p.Task.Title
		}
		head := "질문 「" + clipRunes(q, 80) + "」의 답"
		if p.Task.RoutineID != "" {
			head = "루틴 결과 「" + clipRunes(p.Task.Title, 80) + "」"
		}
		lines = append(lines, line{p.Task.CreatedAt, stamp(p.Task.CreatedAt) + " " + head + ": " + clipRunes(p.Preview, roomContextLine)})
	}
	talk, _ := h.store.RoomMessages(roomID, roomContextTalk, "")
	for _, m := range talk {
		if m.Removed != "" {
			continue
		}
		lines = append(lines, line{m.CreatedAt, stamp(m.CreatedAt) + " " + m.Name + ": " + clipRunes(talkLine(m), roomContextLine)})
	}
	slices.SortStableFunc(lines, func(a, b line) int { return cmp.Compare(a.at, b.at) })
	head := "방 「" + room.Name + "」에서 온 질문이다."
	rules := h.roomRoutineLines(room)
	if len(rules) > 0 {
		head = "방 「" + room.Name + "」에서 온 질문이다. 이 방은 아래 루틴의 결과를 받는 곳이다.\n" + strings.Join(rules, "\n")
	}
	if len(lines) == 0 && len(rules) == 0 {
		return head
	}
	var body []string
	n := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if n += len([]rune(lines[i].text)); n > roomContextTotal && len(body) > 0 {
			break
		}
		body = append(body, lines[i].text)
	}
	slices.Reverse(body)
	out := head
	if len(body) > 0 {
		out += "\n최근 방 글:\n" + strings.Join(body, "\n")
	}
	if len(rules) > 0 {
		return out + "\n질문이 무엇을 가리키는지 애매하면 이 방의 루틴과 위 글을 가리키는 것으로 본다."
	}
	return out + "\n질문이 무엇을 가리키는지 애매하면 위 방 글을 가리키는 것으로 본다."
}

// roomRoutineLines is one line per routine linked to the room; info comes from the agent report, else the name and time learned from routine.run.
func (h *hub) roomRoutineLines(room protocol.Room) []string {
	if len(room.RoutineIDs) == 0 {
		return nil
	}
	var rep struct {
		Routines []struct {
			ID, Name, Title, Schedule, State, LastStatus, Deliver, About, Script string
			LastRun                                                              *int64
		}
	}
	if raw, _ := h.store.Report(room.AgentID); raw != nil {
		json.Unmarshal(raw, &rep)
	}
	learned, _ := h.store.LearnedRoutines(room.AgentID)
	var out []string
	for _, id := range room.RoutineIDs {
		var parts []string
		found := false
		for _, r := range rep.Routines {
			if r.ID != id {
				continue
			}
			found = true
			name := cmp.Or(r.Title, r.Name, id)
			parts = append(parts, "「"+name+"」")
			if r.Schedule != "" {
				parts = append(parts, "일정 "+r.Schedule)
			}
			if r.Script != "" {
				parts = append(parts, "스크립트 "+path.Base(r.Script))
			}
			if r.About != "" {
				parts = append(parts, "하는 일 "+r.About)
			}
			if r.LastRun != nil && *r.LastRun > 0 {
				parts = append(parts, "마지막 실행 "+stamp(*r.LastRun)+" "+cmp.Or(r.LastStatus, r.State))
			}
			if r.Deliver != "" {
				parts = append(parts, "결과 행선지 "+r.Deliver)
			}
			break
		}
		if !found {
			name, at := id, int64(0)
			for _, l := range learned {
				if l.ID == id {
					name, at = l.Name, l.LastRunAt
				}
			}
			parts = append(parts, "「"+name+"」")
			if at > 0 {
				parts = append(parts, "마지막 실행 "+stamp(at))
			}
		}
		out = append(out, "- "+clipRunes(strings.Join(parts, ", "), roomContextRule))
	}
	return out
}

func stamp(ms int64) string { return "[" + time.UnixMilli(ms).Format("01/02 15:04") + "]" }

func clipRunes(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// talkLine is one line for a room person post; attachments are listed by name only.
func talkLine(m protocol.RoomMessage) string {
	if len(m.Attachments) == 0 {
		return m.Text
	}
	names := make([]string, len(m.Attachments))
	for i, a := range m.Attachments {
		names[i] = a.Name
	}
	files := "[첨부: " + strings.Join(names, ", ") + "]"
	if strings.TrimSpace(m.Text) == "" {
		return files
	}
	return m.Text + " " + files
}
