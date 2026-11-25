package hermesbridge

import (
	"context"
	"strings"
	"time"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

// Hermes names the conversation separately after the first reply; re-read after this much time since the conversation ended
var titleDelays = []time.Duration{3 * time.Second, 15 * time.Second, 60 * time.Second}

// hermesTitles reads deck session titles from state.db by conversation id. With an empty task, all sessions started after since.
func hermesTitles(home, task string, since time.Time) map[string]string {
	db, err := openStateDB(home)
	if err != nil {
		return nil
	}
	defer db.Close()
	q, args := `SELECT id, title FROM sessions WHERE id LIKE 'deck-%' AND started_at >= ? AND coalesce(title, '') != ''`, []any{float64(since.Unix())}
	if task != "" {
		q, args = `SELECT id, title FROM sessions WHERE id = ? AND coalesce(title, '') != ''`, []any{sessionID(task)}
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		logOnce("titles", "could not read state.db titles: %v", err)
		return nil
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, title string
		if rows.Scan(&id, &title) == nil {
			out[strings.TrimPrefix(id, "deck-")] = title
		}
	}
	return out
}

// sendTitle sends task.title only when the title differs from the last one sent.
func (b *Bridge) sendTitle(task, title string) {
	title, ok := protocol.CleanTitle(title)
	if r := []rune(title); len(r) > 80 {
		title, ok = string(r[:80]), true
	}
	if !ok {
		return
	}
	b.mu.Lock()
	same := b.titles[task] == title
	b.mu.Unlock()
	if same || !b.send(protocol.AgentMessage{Type: "task.title", Task: task, Title: title}) {
		return
	}
	b.mu.Lock()
	for k := range b.titles {
		if len(b.titles) < maxTitles {
			break
		}
		delete(b.titles, k)
	}
	b.titles[task] = title
	b.mu.Unlock()
}

// sendRecentTitles sends the titles of the last 30 days of deck conversations once on connect.
func (b *Bridge) sendRecentTitles() {
	for task, title := range hermesTitles(b.cfg.HermesHome, "", time.Now().AddDate(0, 0, -30)) {
		b.sendTitle(task, title)
	}
}

// followTitle re-reads the title a fixed few times after a conversation ends.
func (b *Bridge) followTitle(ctx context.Context, task string) {
	delays := b.cfg.TitleDelays
	if len(delays) == 0 {
		delays = titleDelays
	}
	var waited time.Duration
	for _, d := range delays {
		select {
		case <-ctx.Done():
			return
		case <-time.After(d - waited):
		}
		waited = d
		b.sendTitle(task, hermesTitles(b.cfg.HermesHome, task, time.Time{})[task])
	}
}

// titleOnly reports whether this is an unknown-task error for a conversation sent only as a title; the deck does not know that Hermes conversation, so nothing is logged.
func (b *Bridge) titleOnly(reason string) bool {
	task, ok := strings.CutPrefix(reason, "unknown task ")
	if !ok {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_, sent := b.titles[task]
	return sent
}
