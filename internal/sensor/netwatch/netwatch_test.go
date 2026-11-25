package netwatch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/sensor/text"
)

func TestJudge(t *testing.T) {
	ok := Raw{GW: "192.0.2.1", GwMs: "3.1", WanMs: "20.5", DNS: "NXDOMAIN"}
	cases := []struct {
		name string
		edit func(*Raw)
		want string
	}{
		{"normal", func(*Raw) {}, "OK"},
		{"no route", func(r *Raw) { r.GW, r.GwMs = "", "" }, "NO_ROUTE"},
		{"router unresponsive", func(r *Raw) { r.GwMs = "" }, "GW_DOWN"},
		{"WAN down", func(r *Raw) { r.WanMs, r.DNS = "", "" }, "WAN_DOWN"},
		{"DNS only fails", func(r *Raw) { r.DNS = "" }, "DNS_DOWN"},
		{"ping-only blocked is normal", func(r *Raw) { r.WanMs = "" }, "OK"},
	}
	for _, c := range cases {
		r := ok
		c.edit(&r)
		if got := Judge(r); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestBuildFromRealDay(t *testing.T) {
	recs := readRecords("testdata/2026-11-16.tsv")
	b := build(recs, "")
	if b.SamplesToday != 1031 {
		t.Fatalf("samples_today %d", b.SamplesToday)
	}
	if got := b.Status[2].Note; got != "1건" {
		t.Fatalf("today's outages %q, want 1건", got)
	}
	if len(b.Incidents) != 2 {
		t.Fatalf("incidents %+v", b.Incidents)
	}
	blip, outage := b.Incidents[0], b.Incidents[1]
	if blip.Title != "순간 끊김" || blip.Tone != "info" || !strings.HasPrefix(blip.Subtitle, "22:23–22:23 · 43초") {
		t.Errorf("blip %+v", blip)
	}
	if outage.Title != "외부 연결 끊김" || outage.Tone != "danger" ||
		outage.Subtitle != "20:37–22:20 · 1시간 42분 · 외부 연결 끊김 238, 경로 없음 12, 공유기 응답 없음 2" {
		t.Errorf("outage %+v", outage)
	}
	if len(b.Flow.Labels) != flowN || b.Now.State != "정상" || b.Now.TCP == nil || *b.Now.TCP != 109 || b.Now.WanMs != nil {
		t.Errorf("now %+v flow %d", b.Now, len(b.Flow.Labels))
	}
}

func line(ts, st string, extra ...string) record {
	r := record{"ts": "2026-11-16 " + ts, "st": st, "asleep": "0", "pwr": "AC"}
	for _, e := range extra {
		k, v, _ := strings.Cut(e, "=")
		r[k] = v
	}
	return r
}

func TestSleepIsBoundary(t *testing.T) {
	recs := []record{
		line("10:00:00", "OK"),
		line("10:00:20", "WAN_DOWN"),
		line("10:05:00", "WAN_DOWN"), // during a 5-minute outage
		line("11:00:00", "NO_ROUTE", "asleep=1", "pwr=Battery"),
		line("11:00:20", "NO_ROUTE", "pwr=Battery"), // just woke, before Wi-Fi reconnects
		line("11:00:40", "OK"),
	}
	runs := outageRuns(recs)
	if len(runs) != 1 || runs[0].start != "2026-11-16 10:00:20" || runs[0].end != "2026-11-16 10:05:00" || runs[0].recovered != "" {
		t.Fatalf("sleep must split the run: %+v", runs)
	}
	// NO_ROUTE while plugged in is an outage even right after waking
	recs[4]["pwr"] = "AC"
	if runs := outageRuns(recs); len(runs) != 2 {
		t.Fatalf("AC NO_ROUTE is an outage: %d", len(runs))
	}
	// old records (no asleep field) are judged by gap
	old := record{"ts": "x", "gap": "121"}
	if !asleep(old) {
		t.Fatal("gap 121 means asleep")
	}
}

func TestOngoingAndDNSTone(t *testing.T) {
	recs := []record{line("09:00:00", "DNS_DOWN"), line("09:02:00", "DNS_DOWN")}
	b := build(recs, "")
	it := b.Incidents[0]
	if it.Tone != "warn" || it.Title != "DNS 실패" || !strings.HasSuffix(it.Subtitle, " · 진행 중") {
		t.Fatalf("%+v", it)
	}
	if b.Status[0].State != "warn" {
		t.Fatalf("now field %+v", b.Status[0])
	}
}

type fakeProbe struct {
	raw   Raw
	err   error
	snaps int
}

func (f *fakeProbe) Measure(context.Context) (Raw, error) { return f.raw, f.err }
func (f *fakeProbe) Snapshot(context.Context, Raw) string { f.snaps++; return "### fake\n" }

var (
	okRaw   = Raw{GW: "192.0.2.1", GwMs: "3", WanMs: "20", DNS: "NOERROR", TCP: "10", UDP: "2", Pwr: "AC"}
	gwDown  = Raw{GW: "192.0.2.1", DNS: "", TCP: "10", UDP: "2", Pwr: "AC"}
	wanDown = Raw{GW: "192.0.2.1", GwMs: "3", TCP: "10", UDP: "2", Pwr: "AC"}
)

func snaps(t *testing.T, dir string) []string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "snap", "*.txt"))
	for i, f := range files {
		files[i] = filepath.Base(f)
	}
	return files
}

func TestSnapshotOnlyWhenTurningBad(t *testing.T) {
	dir := t.TempDir()
	clock := time.Date(2026, 11, 16, 20, 0, 0, 0, time.Local)
	p := &fakeProbe{raw: okRaw}
	s := &Sensor{Dir: dir, Probe: p, Now: func() time.Time { return clock }}
	step := func(r Raw, d time.Duration) bool {
		clock = clock.Add(d)
		p.raw = r
		return s.Tick(context.Background())
	}
	step(okRaw, 0)
	if !step(gwDown, 20*time.Second) {
		t.Fatal("OK→GW_DOWN is a change")
	}
	if step(gwDown, 20*time.Second) {
		t.Fatal("GW_DOWN→GW_DOWN is not a change")
	}
	step(wanDown, 20*time.Second) // changes to a different abnormal state
	step(okRaw, 20*time.Second)
	step(gwDown, 3*time.Minute) // right after waking is excluded
	if got := snaps(t, dir); len(got) != 2 || !strings.HasSuffix(got[0], "-GW_DOWN.txt") || !strings.HasSuffix(got[1], "-WAN_DOWN.txt") {
		t.Fatalf("snapshot %v", got)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "snap", snaps(t, dir)[0]))
	if !strings.Contains(string(data), "### 직전 기록") || !strings.Contains(string(data), "st=GW_DOWN") {
		t.Fatalf("snapshot body\n%s", data)
	}
	day, _ := os.ReadFile(filepath.Join(dir, "2026-11-16.tsv"))
	lines := strings.Split(strings.TrimSpace(string(day)), "\n")
	if len(lines) != 6 || !strings.HasSuffix(lines[0], "\tgap=0\tasleep=0") || !strings.HasSuffix(lines[5], "\tgap=180\tasleep=1") {
		t.Fatalf("records\n%s", day)
	}
	if !strings.Contains(lines[1], "\tgw_ms=-\twan_ms=-\tdns=-\t") {
		t.Fatalf("empty values are - : %s", lines[1])
	}
}

func TestMeasureFailureIsShown(t *testing.T) {
	var pushed []byte
	p := &fakeProbe{err: errors.New("ping not found")}
	s := &Sensor{Dir: t.TempDir(), Probe: p, Push: func(b json.RawMessage) { pushed = b }}
	s.Tick(context.Background())
	s.Publish()
	var b Bundle
	if err := json.Unmarshal(pushed, &b); err != nil {
		t.Fatal(err)
	}
	if b.Status[0].Note != "측정 못 함: ping not found" || b.Now.State != "측정 못 함" {
		t.Fatalf("%+v %+v", b.Status[0], b.Now)
	}
	if files, _ := filepath.Glob(filepath.Join(s.Dir, "*.tsv")); len(files) != 0 {
		t.Fatal("do not record when measurement failed")
	}
	p.err, p.raw = nil, okRaw
	if !s.Tick(context.Background()) {
		t.Fatal("upload immediately when measurement recovers")
	}
	if b := s.Build(); b.Status[0].State != "ok" {
		t.Fatalf("after recovery %+v", b.Status[0])
	}
}

func TestCleanupKeeps30Days(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "snap"), 0o700)
	now := time.Now()
	old, fresh := filepath.Join(dir, "old.tsv"), filepath.Join(dir, "fresh.tsv")
	oldSnap := filepath.Join(dir, "snap", "old.txt")
	for _, f := range []string{old, fresh, oldSnap} {
		os.WriteFile(f, nil, 0o600)
	}
	past := now.Add(-31 * 24 * time.Hour)
	os.Chtimes(old, past, past)
	os.Chtimes(oldSnap, past, past)
	(&Sensor{Dir: dir}).cleanup()
	for f, want := range map[string]bool{old: false, oldSnap: false, fresh: true} {
		if _, err := os.Stat(f); (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", f, err == nil, want)
		}
	}
}

func TestRunPublishesAndRefreshes(t *testing.T) {
	got := make(chan Bundle, 8)
	p := &fakeProbe{raw: okRaw}
	s := &Sensor{Dir: t.TempDir(), Probe: p, Every: time.Hour, PublishEvery: time.Hour, Push: func(raw json.RawMessage) {
		var b Bundle
		json.Unmarshal(raw, &b)
		got <- b
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	first := <-got
	if first.SamplesToday != 1 {
		t.Fatalf("once right at start: %+v", first)
	}
	s.Refresh()
	select {
	case b := <-got:
		if b.SamplesToday != 2 {
			t.Fatalf("refresh measures again: %d", b.SamplesToday)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh was not uploaded")
	}
}

// Must equal the original netwatch_deck.py's build() output (+ samples_today).
func TestSameAsPythonOriginal(t *testing.T) {
	raw, _ := json.Marshal(build(readRecords("testdata/2026-11-16.tsv"), ""))
	want, err := os.ReadFile("testdata/2026-11-16.want.json")
	if err != nil {
		t.Fatal(err)
	}
	var a map[string]any
	var b any
	json.Unmarshal(raw, &a)
	json.Unmarshal(want, &b)
	delete(a, text.Field) // the Python original (agent example) sends only text, without a key table
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("Go\n%s\nPython\n%s", raw, want)
	}
}
