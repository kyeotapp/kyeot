package hermesbridge

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A one-time routine, a fractional-minute interval and one broken routine do not hide the rest.
func TestRoutineSchedules(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "cron"), 0o755); err != nil {
		t.Fatal(err)
	}
	runAt := time.Date(2026, 11, 20, 10, 0, 0, 0, time.Local)
	jobs := `{"jobs":[
	  {"id":"once","name":"alert","schedule":{"kind":"once","run_at":"` + runAt.Format(time.RFC3339) + `"},"repeat":{"times":1}},
	  {"id":"half","name":"half","schedule":{"kind":"interval","minutes":1.5}},
	  {"id":"broken","name":"broken","schedule":"0 9 * * *"},
	  {"id":"cron1","name":"once-cron","schedule":{"kind":"cron","expr":"0 10 13 11 *"},"repeat":{"times":1}},
	  {"id":"bare","name":"no-time","schedule":{"kind":"once"}}
	]}`
	if err := os.WriteFile(filepath.Join(home, "cron", "jobs.json"), []byte(jobs), 0o644); err != nil {
		t.Fatal(err)
	}
	r := BuildReport(home, time.Now())
	got := map[string]Routine{}
	for _, x := range r.Routines {
		got[x.ID] = x
	}
	if len(got) != 4 || got["broken"].ID != "" {
		t.Fatalf("broken job should be skipped alone: %v", r.Routines)
	}
	if o := got["once"]; o.Schedule != "11월 20일 10:00, 한 번" || !o.Once || o.At == nil || *o.At != runAt.UnixMilli() || o.Cron != "" {
		t.Fatalf("once: %+v", o)
	}
	if h := got["half"]; h.Every != 2 || h.Schedule != "2분마다" {
		t.Fatalf("fractional minutes: %+v", h)
	}
	if c := got["cron1"]; c.Cron != "0 10 13 11 *" || !c.Once || c.At != nil {
		t.Fatalf("one-shot cron: %+v", c)
	}
	if b := got["bare"]; b.Schedule != "한 번" || !b.Once {
		t.Fatalf("once without time: %+v", b)
	}
}

func TestDeliverLabel(t *testing.T) {
	for in, want := range map[string]string{
		"": "봇 안에만 저장", "local": "봇 안에만 저장", "origin": "만든 대화로",
		"discord:1567917142651969598": "디스코드", "telegram:-100123": "텔레그램",
		"slack:C0123": "Slack", "Email:a": "이메일", "mastodon:x": "Mastodon",
		"discord:1,discord:2,telegram:3": "디스코드, 텔레그램",
	} {
		if got := deliverLabel(in); got != want {
			t.Errorf("deliverLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFirstSentence(t *testing.T) {
	for in, want := range map[string]string{
		"대회를 모아 알린다. 자세한 규칙은 아래.":      "대회를 모아 알린다.",
		"빈자리를 확인해 주세요. 있으면 알려요.":       "빈자리를 확인해 주세요.",
		"Check the prices! Then post.": "Check the prices!",
		"## 목표\n가격이 3.5배 오르면 알린다. 끝.":  "목표 가격이 3.5배 오르면 알린다.",
		"- 새 공고를 모은다":                  "새 공고를 모은다",
		"마침표 없는 지시문":                   "마침표 없는 지시문",
	} {
		if got := firstSentence(in); got != want {
			t.Errorf("firstSentence(%q) = %q, want %q", in, got, want)
		}
	}
}
