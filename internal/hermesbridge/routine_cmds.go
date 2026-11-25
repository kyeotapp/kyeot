package hermesbridge

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

// watchRoutines sends routine results and reports while connected.
// Recent results are resent on every connect; the server filters duplicate runs.
func (b *Bridge) watchRoutines(ctx context.Context) {
	runsEvery := cmp.Or(b.cfg.RunsEvery, 30*time.Second)
	reportEvery := cmp.Or(b.cfg.ReportEvery, 5*time.Minute)
	sendRuns := func() {
		now := time.Now()
		b.mu.Lock()
		for key := range b.sent {
			if at, err := time.ParseInLocation("2006-01-02_15-04-05", key[strings.LastIndex(key, "/")+1:], time.Local); err != nil || now.Sub(at) > runWindow+24*time.Hour {
				delete(b.sent, key)
			}
		}
		b.mu.Unlock()
		seen := func(key string) bool {
			b.mu.Lock()
			defer b.mu.Unlock()
			return b.sent[key]
		}
		for _, m := range routineRuns(b.cfg.HermesHome, now, seen) {
			if b.send(m) {
				b.mu.Lock()
				b.sent[m.Routine+"/"+m.Run] = true
				b.mu.Unlock()
			}
		}
	}
	jobs := filepath.Join(b.cfg.HermesHome, "cron", "jobs.json")
	var jobsAt time.Time
	sendReport := func() {
		if fi, err := os.Stat(jobs); err == nil {
			jobsAt = fi.ModTime()
		}
		b.sendReport()
	}
	sendReport()
	sendRuns()
	runs, report := time.NewTicker(runsEvery), time.NewTicker(reportEvery)
	defer runs.Stop()
	defer report.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-runs.C:
			sendRuns()
			if fi, err := os.Stat(jobs); err == nil && !fi.ModTime().Equal(jobsAt) { // report right away when a conversation creates or changes a routine
				sendReport()
			}
		case <-report.C:
			sendReport()
		}
	}
}

// sendReport sends the report. The bridge survives a panic while building it.
func (b *Bridge) sendReport() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("could not build report: %v", r)
		}
	}()
	if raw, err := json.Marshal(BuildReport(b.cfg.HermesHome, time.Now())); err == nil {
		b.send(protocol.AgentMessage{Type: "report", Report: raw})
	}
}

var routineID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// toggleRoutine toggles only routines in jobs.json with hermes cron pause/resume, then resends the report.
func (b *Bridge) toggleRoutine(ctx context.Context, id string, on bool) {
	b.routineCommand(ctx, id, map[bool]string{true: "resume", false: "pause"}[on])
}

// createRoutine creates an app-made routine with hermes cron create. Results accumulate inside the bot and the bridge moves them to the room.
func (b *Bridge) createRoutine(ctx context.Context, name, schedule, prompt string) {
	if b.cfg.HermesHome == "" || !protocol.ValidSchedule(schedule) || strings.TrimSpace(prompt) == "" || len([]rune(name)) > 60 {
		log.Printf("routine creation rejected: %q %q", name, schedule)
		return
	}
	args := []string{"cron", "create", "--name=" + name, "--deliver", "local", "--", schedule, prompt} // names starting with "-" are still taken as values
	if out, err := b.hermesCmd(ctx, args...); err != nil {
		log.Printf("hermes cron create: %v %s", err, strings.TrimSpace(string(out)))
	}
	b.sendReport()
}

func (b *Bridge) hermesCmd(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return exec.CommandContext(ctx, cmp.Or(b.cfg.HermesBin, "hermes"), args...).CombinedOutput()
}

func (b *Bridge) knownRoutine(id string) bool {
	if b.cfg.HermesHome == "" || !routineID.MatchString(id) || !slices.ContainsFunc(readJobs(b.cfg.HermesHome), func(j rawJob) bool { return j.ID == id }) {
		log.Printf("unknown routine %q", id)
		return false
	}
	return true
}

// routineCommand runs hermes cron <verb> <id> only for routines in jobs.json and sends a new report.
func (b *Bridge) routineCommand(ctx context.Context, id, verb string) {
	if !b.knownRoutine(id) {
		return
	}
	if out, err := b.hermesCmd(ctx, "cron", verb, id); err != nil {
		log.Printf("hermes cron %s %s: %v %s", verb, id, err, strings.TrimSpace(string(out)))
	}
	b.sendReport()
}

// runRoutine runs hermes cron run separately (it runs to completion in place, so it can take long).
// routineRuns sends successful results. Failures, timeouts and duplicate runs are reported as routine.run ok=false.
func (b *Bridge) runRoutine(ctx context.Context, id string) {
	if !b.knownRoutine(id) {
		return
	}
	start := time.Now()
	b.mu.Lock()
	busy := b.running[id]
	b.running[id] = true
	b.mu.Unlock()
	if busy {
		b.routineFailed(id, start, "busy", "이미 돌고 있어요. 끝나면 결과가 올라와요")
		return
	}
	go func() {
		defer func() {
			b.mu.Lock()
			delete(b.running, id)
			b.mu.Unlock()
		}()
		limit := cmp.Or(b.cfg.RoutineRunTimeout, 30*time.Minute)
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limit) // a deck reconnect does not interrupt it
		defer cancel()
		before := b.outputs(id)
		cmd := exec.CommandContext(rctx, cmp.Or(b.cfg.HermesBin, "hermes"), "cron", "run", id)
		cmd.WaitDelay = 5 * time.Second // stop waiting even if a child keeps the output open
		out, err := cmd.CombinedOutput()
		wrote := false
		for name := range b.outputs(id) {
			wrote = wrote || !before[name]
		}
		if err != nil && !wrote { // if a result file exists, routineRuns sends it
			if rctx.Err() == context.DeadlineExceeded {
				err = fmt.Errorf("%s 안에 끝나지 않아 멈췄어요", limit)
			}
			log.Printf("hermes cron run %s: %v", id, err)
			b.routineFailed(id, start, "failed", tail(strings.TrimSpace(string(out))+"\n"+err.Error(), 800))
		}
		b.sendReport()
	}()
}

// outputs lists a routine's result file names; comparing before and after tells whether Hermes left a result for this run.
func (b *Bridge) outputs(id string) map[string]bool {
	files, _ := os.ReadDir(filepath.Join(b.cfg.HermesHome, "cron", "output", id))
	names := map[string]bool{}
	for _, f := range files {
		names[f.Name()] = true
	}
	return names
}

func (b *Bridge) routineFailed(id string, at time.Time, kind, text string) {
	name := id
	for _, j := range readJobs(b.cfg.HermesHome) {
		if j.ID == id {
			name = cmp.Or(j.Name, id)
		}
	}
	ok := false
	b.send(protocol.AgentMessage{Type: "routine.run", Routine: id, RoutineName: name, Run: at.Format("2006-01-02_15-04-05.000") + "-" + kind,
		Text: cmp.Or(strings.TrimSpace(text), "실패했어요"), OK: &ok, At: at.UnixMilli()})
}

// tail cuts the last n bytes at a character boundary.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return "…" + s
}
