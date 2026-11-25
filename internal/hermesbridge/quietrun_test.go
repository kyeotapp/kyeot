package hermesbridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

func TestQuietRunsGoToHistory(t *testing.T) {
	cases := map[string]bool{
		`{"ok":true,"id":"netwatch"}`:                                    true,
		"{\"ok\":true,\"id\":\"disk\"}\n\n{\"ok\":true,\"id\":\"disk\"}": true,
		"[SILENT]": true,
		`{"ok":false,"reason":"bundles up to 256KB"}`:                                     false,
		`{"ok":true,"id":"net","reason":"tabs are full so it was left outside the tabs"}`: false,
		"{\"ok\":true,\"id\":\"net\"}\n3 contests this week":                              false,
		"This week contests": false,
	}
	for body, want := range cases {
		if got := quietRun(body); got != want {
			t.Fatalf("quietRun(%q) = %v, want %v", body, got, want)
		}
	}

	now := time.Now()
	home := fakeHome(t, now)
	dir := filepath.Join(home, "cron", "output", "job2")
	os.MkdirAll(dir, 0o755)
	stamp := now.Add(-time.Minute).Format("2006-01-02_15-04-05")
	os.WriteFile(filepath.Join(dir, stamp+".md"), []byte("# Cron Job: network-tab-refresh\n\n**Mode:** no_agent (script)\n\n---\n\n{\"ok\":true,\"id\":\"netwatch\"}\n"), 0o644)
	silent := now.Add(-2 * time.Minute).Format("2006-01-02_15-04-05")
	os.MkdirAll(filepath.Join(home, "cron", "output", "job3"), 0o755)
	os.WriteFile(filepath.Join(home, "cron", "output", "job3", silent+".md"), []byte("# Cron Job: new-routine\n\n**Status:** silent (empty output)\n"), 0o644)
	got := map[string]protocol.AgentMessage{}
	for _, r := range routineRuns(home, now, nil) {
		got[r.Routine] = r
	}
	for _, id := range []string{"job2", "job3"} {
		if r, ok := got[id]; !ok || !r.Quiet || r.Text != "" {
			t.Fatalf("%s must go as a quiet run without text: %+v", id, r)
		}
	}
	if r := got["job1"]; r.Quiet || r.Text != "This week contests" {
		t.Fatalf("a run with text stays as is: %+v", r)
	}
	if _, _, ok := protocol.ParseAgentMessage(mustJSON(t, got["job3"])); !ok {
		t.Fatal("a quiet run must be accepted without text")
	}
	noisy := got["job3"]
	noisy.Quiet = false
	if _, _, ok := protocol.ParseAgentMessage(mustJSON(t, noisy)); ok {
		t.Fatal("an empty text that is not quiet must be rejected (blocks an empty conversation like the old server)")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
