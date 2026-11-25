package hermesbridge

import (
	"context"
	"database/sql"
	"github.com/kyeotapp/kyeot/internal/cron"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCronText(t *testing.T) {
	for expr, want := range map[string]string{
		"0 9 * * 1,4":         "매주 월, 목 09:00",
		"50 23 * * *":         "매일 23:50",
		"0 18 * * 1-5":        "평일 18:00",
		"0 10 13 11 *":        "11월 13일 10:00",
		"*/5 * * * *":         "5분마다",
		"*/15 0,7-23 * * *":   "15분마다 (0시, 7~23시)",
		"*/10 9 * * *":        "10분마다 (9시)",
		"*/15 */2 * * *":      "*/15 */2 * * *",
		"*/15 0,7-23 * * 1-5": "*/15 0,7-23 * * 1-5",
	} {
		if got := cron.Text(expr); got != want {
			t.Errorf("%s: got %q want %q", expr, got, want)
		}
	}
}

func TestCronRunsCountsWeekdays(t *testing.T) {
	start := time.Date(2026, 11, 2, 0, 0, 0, 0, time.Local) // Monday
	runs := cron.Runs("0 9 * * 1,4", start, start.AddDate(0, 0, 14))
	if len(runs) != 4 || runs[0].Weekday() != time.Monday || runs[1].Weekday() != time.Thursday {
		t.Fatalf("runs: %v", runs)
	}
	if cron.Runs("not a cron", start, start.AddDate(0, 0, 1)) != nil {
		t.Fatal("bad expr should give nil")
	}
}

func TestRunBodyDropsHeader(t *testing.T) {
	md := "# Cron Job: Contest\n\n**Job ID:** x\n**Mode:** no_agent\n\n---\n\n**This week**\n- one\n"
	if got := runBody(md); got != "**This week**\n- one" {
		t.Fatalf("body: %q", got)
	}
	if got := runBody("plain text"); got != "plain text" {
		t.Fatalf("plain: %q", got)
	}
	silent := "# Cron Job: chat-log\n\n**Job ID:** x\n**Mode:** no_agent (script)\n**Status:** silent (empty output)"
	if got := runBody(silent); got != "" {
		t.Fatalf("silent run should be dropped: %q", got)
	}
}

// fakeHome builds a Hermes folder with jobs.json, two result files (one recent, one old) and state.db.
func fakeHome(t *testing.T, now time.Time) string {
	t.Helper()
	home := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(home, "cron", "output", "job1"), 0o755))
	must(os.WriteFile(filepath.Join(home, "cron", "jobs.json"), []byte(`{"jobs":[
	  {"id":"job1","name":"contest-alert","prompt":"Collect contests and notify. Details below.","schedule":{"kind":"cron","expr":"0 9 * * 1,4"},"deliver":"discord:1","last_status":"ok"},
	  {"id":"job2","name":"day-summary","schedule":{"kind":"cron","expr":"50 23 * * *"},"deliver":"local","enabled":false},
	  {"id":"job3","name":"new-routine","schedule":{"kind":"interval","minutes":120},"deliver":"local"}
	]}`), 0o644))
	recent := now.Add(-time.Hour).Format("2006-01-02_15-04-05")
	old := now.AddDate(0, 0, -20).Format("2006-01-02_15-04-05")
	must(os.WriteFile(filepath.Join(home, "cron", "output", "job1", recent+".md"), []byte("# Cron Job: contest-alert\n\n---\n\nThis week contests"), 0o644))
	must(os.WriteFile(filepath.Join(home, "cron", "output", "job1", old+".md"), []byte("# Cron Job: contest-alert\n\n---\n\nOld contest"), 0o644))

	db, err := sql.Open("sqlite", filepath.Join(home, "state.db")+"?_pragma=busy_timeout(5000)")
	must(err)
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE sessions (started_at REAL, source TEXT, title TEXT, input_tokens INTEGER, output_tokens INTEGER,
		cache_read_tokens INTEGER, cache_write_tokens INTEGER, actual_cost_usd REAL, estimated_cost_usd REAL)`)
	must(err)
	day := float64(now.Add(-2 * time.Hour).Unix())
	for _, row := range []struct {
		source, title string
		cost          any
	}{
		{"cron", "contest-alert · 11/09", 0.02},
		{"cron", "contest-alert · 11/06", 0.04},
		{"discord", "chatter", 0.10},
		{"discord", "no value", nil},
	} {
		_, err = db.Exec(`INSERT INTO sessions VALUES (?, ?, ?, 100, 10, 0, 0, NULL, ?)`, day, row.source, row.title, row.cost)
		must(err)
	}
	return home
}

func TestBuildReport(t *testing.T) {
	now := time.Now()
	r := BuildReport(fakeHome(t, now), now)

	if len(r.Routines) != 3 {
		t.Fatalf("routines: %d", len(r.Routines))
	}
	job1, job2, job3 := r.Routines[0], r.Routines[1], r.Routines[2]
	if job1.Schedule != "매주 월, 목 09:00" || job1.State != "ok" || job1.Deliver != "디스코드" || job1.About != "Collect contests and notify." {
		t.Fatalf("job1: %+v", job1)
	}
	if job1.Runs != 2 || job1.AvgCost == nil || *job1.AvgCost < 0.0299 || *job1.AvgCost > 0.0301 {
		t.Fatalf("job1 cost: runs=%d avg=%v", job1.Runs, job1.AvgCost)
	}
	if job2.State != "off" || job2.Deliver != "봇 안에만 저장" {
		t.Fatalf("job2: %+v", job2)
	}
	if job1.Cron != "0 9 * * 1,4" || job1.Every != 0 {
		t.Fatalf("job1 raw schedule should go out for the app: %q %d", job1.Cron, job1.Every)
	}
	if job3.Every != 120 || job3.Cron != "" {
		t.Fatalf("job3 interval: %d %q", job3.Every, job3.Cron)
	}
	if job3.Schedule != "2시간마다" {
		t.Fatalf("job3 schedule: %q", job3.Schedule)
	}

	week := r.Periods[0]
	if week.Sessions != 4 || week.Unpriced != 1 || week.Cost < 0.159 || week.Cost > 0.161 {
		t.Fatalf("week: %+v", week)
	}
	if week.BySource[0].Label != "디스코드" || week.BySource[1].Label != "루틴" {
		t.Fatalf("by source: %+v", week.BySource)
	}

	if len(r.Daily) != dailyDays {
		t.Fatalf("daily length: %d", len(r.Daily))
	}
	last := r.Daily[len(r.Daily)-1]
	if last.Date != now.Format("2006-01-02") && last.Date != now.Add(-2*time.Hour).Format("2006-01-02") {
		t.Fatalf("last day: %s", last.Date)
	}
	var total float64
	var sessions int
	for _, d := range r.Daily {
		total += d.Cost
		sessions += d.Sessions
	}
	if sessions != 4 || total < 0.159 || total > 0.161 || r.Daily[len(r.Daily)-1].BySource == nil {
		t.Fatalf("daily sum: sessions=%d total=%v", sessions, total)
	}

	f := r.Forecast
	if len(f.Rows) != 1 || f.Rows[0].Name != "contest-alert" || f.Rows[0].Runs < 8 || f.Rows[0].Runs > 10 {
		t.Fatalf("forecast rows: %+v", f.Rows)
	}
	if strings.Join(f.Unknown, ",") != "new-routine" {
		t.Fatalf("unknown: %v (a disabled routine must be left out)", f.Unknown)
	}
	if f.Other < 0.42 || f.Other > 0.43 {
		t.Fatalf("other: %v (discord 0.10 × 30/7)", f.Other)
	}
}

func TestRoutineRunsSkipsOldFiles(t *testing.T) {
	now := time.Now()
	runs := routineRuns(fakeHome(t, now), now, nil)
	if len(runs) != 1 || runs[0].Text != "This week contests" || runs[0].RoutineName != "contest-alert" || runs[0].Routine != "job1" {
		t.Fatalf("runs: %+v", runs)
	}
}

func TestBuildReportWithoutHermes(t *testing.T) {
	r := BuildReport(t.TempDir(), time.Now())
	if len(r.Routines) != 0 || len(r.Periods) != 0 || r.Forecast == nil {
		t.Fatalf("empty home: %+v", r)
	}
}

func TestScriptInfo(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "scripts")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "digest.py"), []byte(`#!/usr/bin/env python3
"""digest.py: Collects job postings and notifies.

Picks postings and sends them to Discord.
"""
import json
from sources import fetch
URL = "https://career.example.com/jobs?page=1"
HOOK = "https://hooks.example.com/webhook/abc"
STATE = "digest-state.json"
`), 0o644)
	os.WriteFile(filepath.Join(dir, "sources.py"), []byte(`"""sources.py: posting collector module"""
A = "https://recruit.example.org/list/"
`), 0o644)

	info := scriptInfo(home, "digest.py")
	if info == nil || info.Summary != "Collects job postings and notifies." || info.Body != "Picks postings and sends them to Discord." {
		t.Fatalf("doc: %+v", info)
	}
	if _, body := docParts("x.py: summary\n\n    ## Policy\n    - one\n\n\n    - two\n", "x.py"); body != "## Policy\n- one\n\n- two" {
		t.Fatalf("body keeps lines: %q", body)
	}
	if len(info.Modules) != 1 || info.Modules[0].Name != "sources.py" || info.Modules[0].Summary != "posting collector module" {
		t.Fatalf("modules: %+v", info.Modules)
	}
	if strings.Join(info.Sources, " ") != "https://career.example.com/jobs https://recruit.example.org/list" {
		t.Fatalf("sources: %v (the webhook address must be left out)", info.Sources)
	}
	if strings.Join(info.Files, ",") != "digest-state.json" {
		t.Fatalf("files: %v", info.Files)
	}
}

func TestToggleRoutineRunsHermesOnlyForKnownJobs(t *testing.T) {
	now := time.Now()
	home := fakeHome(t, now)
	log := filepath.Join(t.TempDir(), "calls")
	bin := filepath.Join(t.TempDir(), "hermes")
	os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\" >> "+log+"\n"), 0o755)

	b := New(Config{HermesHome: home, HermesBin: bin})
	b.toggleRoutine(context.Background(), "job1", false)
	b.toggleRoutine(context.Background(), "job2", true)
	b.toggleRoutine(context.Background(), "nope", true)
	b.toggleRoutine(context.Background(), "job1; rm -rf /", true)
	b.routineCommand(context.Background(), "job3", "remove")
	b.routineCommand(context.Background(), "../job1", "remove")
	b.createRoutine(context.Background(), "Morning news", "0 9 * * 1-5", "-- Summarize today news in three lines")
	b.createRoutine(context.Background(), "bad schedule", "0 9 * * *; rm -rf /", "x")
	b.createRoutine(context.Background(), "--help", "0 8 * * *", "y")

	got := readText(log)
	if got != "cron pause job1\ncron resume job2\ncron remove job3\ncron create --name=Morning news --deliver local -- 0 9 * * 1-5 -- Summarize today news in three lines\ncron create --name=--help --deliver local -- 0 8 * * * y\n" {
		t.Fatalf("hermes calls:\n%s", got)
	}
}

func TestScriptRoutineSessionsCountAsRoutine(t *testing.T) {
	rows := []session{
		{source: "oneshot", title: "contest-alert · Nov 12 09:01"},
		{source: "oneshot", title: "Find domestic AI contests"},
		{source: "oneshot", title: "unknown-routine · Nov 12"},
	}
	claimRoutineSessions(rows, jobNames([]rawJob{{ID: "job1", Name: "contest-alert"}}))
	if rows[0].source != "cron" || rows[1].source != "oneshot" || rows[2].source != "oneshot" {
		t.Fatalf("sources: %q %q %q", rows[0].source, rows[1].source, rows[2].source)
	}
}

func TestByRoutine(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.Local)
	home := fakeHome(t, now)
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		at            time.Time
		source, title string
		cost          any
	}{
		{now.Add(-time.Hour), "cron", "AI-contest-alert · 11/10", 0.03},
		{now.Add(-time.Hour), "cron", "", 0.01},
		{now.Add(-time.Hour), "discord", "contest-alert · conversation called inside", 0.05},
		{now.Add(-time.Hour), "cron", "day-summary · no value", nil},
		{time.Date(2026, 10, 5, 9, 0, 0, 0, time.Local), "cron", "AI-contest-alert · 10/05", 0.07},
	} {
		if _, err := db.Exec(`INSERT INTO sessions VALUES (?, ?, ?, 1, 1, 0, 0, NULL, ?)`, float64(row.at.Unix()), row.source, row.title, row.cost); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	r := BuildReport(home, now)
	for _, p := range r.Periods {
		var routine, sum float64
		for _, sc := range p.BySource {
			if sc.Label == "루틴" {
				routine = sc.Cost
			}
		}
		for _, rc := range p.ByRoutine {
			sum += rc.Cost
		}
		if math.Abs(routine-sum) > 1e-9 {
			t.Fatalf("%s: byRoutine sum %v != routine source %v (%+v)", p.Key, sum, routine, p.ByRoutine)
		}
	}
	var dayRoutines float64
	for _, d := range r.Daily {
		var sum float64
		for _, rc := range d.ByRoutine {
			sum += rc.Cost
			dayRoutines += rc.Cost
		}
		if math.Abs(sum-d.BySource["루틴"]) > 1e-9 {
			t.Fatalf("%s: daily byRoutine %v != routine %v", d.Date, sum, d.BySource["루틴"])
		}
	}
	if math.Abs(dayRoutines-(0.02+0.04+0.03+0.01+0.05+0.07)) > 1e-9 {
		t.Fatalf("daily byRoutine total: %v", dayRoutines)
	}
	month := r.Periods[1]
	if month.Key != "month" || len(month.ByRoutine) != 3 {
		t.Fatalf("month byRoutine: %+v", month.ByRoutine)
	}
	top, gone, unnamed := month.ByRoutine[0], month.ByRoutine[1], month.ByRoutine[2]
	if top.Name != "contest-alert" || top.Runs != 3 || math.Abs(top.Cost-0.11) > 1e-9 || top.Gone {
		t.Fatalf("top: %+v", top)
	}
	if gone.Name != "AI-contest-alert" || gone.Runs != 1 || !gone.Gone {
		t.Fatalf("gone: %+v", gone)
	}
	if unnamed.Name != "이름 없는 루틴" || unnamed.Gone {
		t.Fatalf("unnamed: %+v", unnamed)
	}
	last := r.Periods[2]
	if len(last.ByRoutine) != 1 || last.ByRoutine[0].Name != "AI-contest-alert" || math.Abs(last.ByRoutine[0].Cost-0.07) > 1e-9 {
		t.Fatalf("last byRoutine: %+v", last.ByRoutine)
	}
	job1, job2 := r.Routines[0], r.Routines[1]
	if job1.MonthCost == nil || math.Abs(*job1.MonthCost-0.11) > 1e-9 || job1.MonthRuns != 3 {
		t.Fatalf("job1 month: %v %d", job1.MonthCost, job1.MonthRuns)
	}
	if job2.MonthCost != nil || job2.MonthRuns != 0 {
		t.Fatalf("job2 has no priced run this month: %v", job2.MonthCost)
	}
}
