// Package netwatch measures this computer's network every 20 seconds, records it, and builds the netwatch bundle for the kyeot network tab.
// Measurement interval, state judgment, snapshots, and excluding sleeping samples are user-independent policy, kept by code and tests (sensor template).
// The originals are netwatch.sh and netwatch_deck.py in integrations/hermes/examples/netwatch.
package netwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	BundleName = "netwatch" // bundle name
	keepDays   = 30
	sleepGap   = 120 // seconds. With a 20-second interval, a gap this large means it was asleep
	tailLines  = 30
)

type Sensor struct {
	Dir          string
	Probe        Prober
	Every        time.Duration // measurement (default 20 seconds)
	PublishEvery time.Duration // aggregation (default 5 minutes)
	Push         func(json.RawMessage)
	Now          func() time.Time

	mu      sync.Mutex
	failed  string // why the last measurement failed
	refresh chan struct{}
}

func New(dir string, push func(json.RawMessage)) *Sensor {
	return &Sensor{Dir: dir, Probe: System{}, Push: push}
}

func (s *Sensor) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Sensor) refreshCh() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refresh == nil {
		s.refresh = make(chan struct{}, 1)
	}
	return s.refresh
}

// Refresh tells Run to measure and upload once now. It doesn't wait.
func (s *Sensor) Refresh() {
	select {
	case s.refreshCh() <- struct{}{}:
	default:
	}
}

// Run measures until ctx ends. It measures and uploads once right at start.
func (s *Sensor) Run(ctx context.Context) {
	every, pubEvery := s.Every, s.PublishEvery
	if every <= 0 {
		every = 20 * time.Second
	}
	if pubEvery <= 0 {
		pubEvery = 5 * time.Minute
	}
	refresh := s.refreshCh()
	measure, publish := time.NewTicker(every), time.NewTicker(pubEvery)
	defer measure.Stop()
	defer publish.Stop()
	s.Tick(ctx)
	s.Publish()
	for {
		select {
		case <-ctx.Done():
			return
		case <-measure.C:
			if changed := s.Tick(ctx); changed {
				s.Publish()
			}
		case <-publish.C:
			s.cleanup()
			s.Publish()
		case <-refresh:
			s.Tick(ctx)
			s.Publish()
		}
	}
}

func (s *Sensor) statePath() string { return filepath.Join(s.Dir, ".state") }

func (s *Sensor) readState() (int64, string) {
	b, err := os.ReadFile(s.statePath())
	if err != nil {
		return 0, "OK"
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0, "OK"
	}
	ts, _ := strconv.ParseInt(f[0], 10, 64)
	return ts, f[1]
}

// Tick measures once, writes one line to today's file, and saves a snapshot if the state turned abnormal. Returns true if the state changed.
func (s *Sensor) Tick(ctx context.Context) bool {
	raw, err := s.Probe.Measure(ctx)
	s.mu.Lock()
	prevFailed := s.failed
	s.failed = ""
	if err != nil {
		s.failed = err.Error()
	}
	s.mu.Unlock()
	if err != nil {
		if prevFailed == "" {
			log.Printf("network sensor: measurement failed: %v", err)
		}
		return prevFailed != err.Error()
	}
	if err := os.MkdirAll(filepath.Join(s.Dir, "snap"), 0o700); err != nil {
		log.Printf("network sensor: could not create record folder: %v", err)
		return false
	}
	now := s.now()
	st := Judge(raw)
	prevTS, prevSt := s.readState()
	gap := now.Unix() - prevTS
	if prevTS == 0 {
		gap = 0
	}
	sleep := 0
	if gap > sleepGap {
		sleep = 1
	}
	dash := func(v string) string { return cmpOr(v, "-") }
	line := fmt.Sprintf("%s\tst=%s\tgw=%s\tgwmac=%s\tip=%s\tgw_ms=%s\twan_ms=%s\tdns=%s\ttcp=%s\tudp=%s\ttop=%s\tpwr=%s\tgap=%d\tasleep=%d\n",
		now.Format(tsLayout), st, dash(raw.GW), dash(raw.GWMAC), dash(raw.IP), dash(raw.GwMs), dash(raw.WanMs), dash(raw.DNS),
		dash(raw.TCP), dash(raw.UDP), raw.Top, dash(raw.Pwr), gap, sleep)
	day := filepath.Join(s.Dir, now.Format("2006-01-02")+".tsv")
	if f, err := os.OpenFile(day, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		f.WriteString(line)
		f.Close()
	}
	if st != "OK" && st != prevSt && sleep == 0 {
		s.snapshot(ctx, raw, st, now, day)
	}
	os.WriteFile(s.statePath(), []byte(fmt.Sprintf("%d %s\n", now.Unix(), st)), 0o600)
	return st != prevSt || prevFailed != ""
}

func (s *Sensor) snapshot(ctx context.Context, raw Raw, st string, now time.Time, day string) {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s %s gw=%s (%s) ip=%s\n", now.Format(tsLayout), st, raw.GW, raw.GWMAC, raw.IP)
	b.WriteString("### 직전 기록\n")
	if data, err := os.ReadFile(day); err == nil {
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		b.WriteString(strings.Join(lines[max(0, len(lines)-tailLines):], "\n") + "\n")
	}
	b.WriteString(s.Probe.Snapshot(ctx, raw))
	name := filepath.Join(s.Dir, "snap", now.Format("20060102-150405")+"-"+st+".txt")
	if err := os.WriteFile(name, []byte(b.String()), 0o600); err != nil {
		log.Printf("network sensor: could not save snapshot: %v", err)
	}
}

// cleanup deletes records and snapshots older than 30 days.
func (s *Sensor) cleanup() {
	limit := s.now().Add(-keepDays * 24 * time.Hour)
	for _, pat := range []string{"*.tsv", filepath.Join("snap", "*.txt")} {
		files, _ := filepath.Glob(filepath.Join(s.Dir, pat))
		for _, f := range files {
			if fi, err := os.Stat(f); err == nil && fi.ModTime().Before(limit) {
				os.Remove(f)
			}
		}
	}
}

// Build makes the bundle from today's records.
func (s *Sensor) Build() Bundle {
	s.mu.Lock()
	failed := s.failed
	s.mu.Unlock()
	return build(readRecords(filepath.Join(s.Dir, s.now().Format("2006-01-02")+".tsv")), failed)
}

// Publish builds the bundle and hands it to Push.
func (s *Sensor) Publish() {
	if s.Push == nil {
		return
	}
	raw, err := json.Marshal(s.Build())
	if err != nil {
		return
	}
	s.Push(raw)
}
