package system

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/sensor/text/texttest"
)

func f(v float64) *float64 { return &v }
func u(v uint64) *uint64   { return &v }
func yes(v bool) *bool     { return &v }

var now = time.Date(2026, 11, 21, 14, 20, 5, 0, time.Local)

func full() Sample {
	return Sample{CPU: f(23.44), MemUsed: 12 << 30, MemTotal: 16 << 30, Swap: u(3 << 29), Battery: f(64), Charging: yes(true)}
}

func states(b Bundle) [3]string {
	return [3]string{b.Status[0].State, b.Status[1].State, b.Status[2].State}
}

func TestBuild(t *testing.T) {
	b := Build(full(), now)
	if *b.CPUPct != 23.4 || *b.MemPct != 75 || *b.MemUsedGB != 12 || *b.MemTotalGB != 16 || *b.SwapGB != 1.5 || *b.BatteryPct != 64 || !*b.Charging || b.Checked != "2026-11-21 14:20:05" {
		t.Fatalf("%+v", b)
	}
	if states(b) != [3]string{"ok", "ok", "ok"} || b.Status[1].Note != "12 / 16GB · 75%" || b.Status[2].Note != "64% · 충전 중" {
		t.Fatalf("%+v", b.Status)
	}
}

func TestThresholds(t *testing.T) {
	cases := []struct {
		cpu, mem, bat float64
		want          [3]string
	}{
		{84.9, 89.9, 21, [3]string{"ok", "ok", "ok"}},
		{85, 90, 20, [3]string{"warn", "warn", "warn"}},
		{99, 99, 10, [3]string{"warn", "warn", "danger"}},
	}
	for _, c := range cases {
		s := Sample{CPU: f(c.cpu), MemUsed: uint64(c.mem * 1e7), MemTotal: 1e9, Battery: f(c.bat), Charging: yes(false)}
		if got := states(Build(s, now)); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}

func TestMissing(t *testing.T) {
	b := Build(Sample{CPUErr: errors.New("top failed"), MemErr: ErrUnsupported}, now)
	if b.CPUPct != nil || b.MemPct != nil || b.BatteryPct != nil || b.Charging != nil || b.SwapGB != nil {
		t.Fatalf("%+v", b)
	}
	if states(b) != [3]string{"off", "off", "off"} || b.Status[0].Note != "측정 못 함: top failed" || b.Status[1].Note != "이 운영체제에서는 아직 못 잰다" || b.Status[2].Note != "배터리 없음" {
		t.Fatalf("%+v", b.Status)
	}
	raw, _ := json.Marshal(b)
	var m map[string]any
	json.Unmarshal(raw, &m)
	for _, k := range []string{"cpu_pct", "mem_pct", "mem_used_gb", "mem_total_gb", "swap_gb", "battery_pct", "charging"} {
		if v, ok := m[k]; !ok || v != nil {
			t.Errorf("%s must be null: %v", k, v)
		}
	}
}

func TestTextKeysMatchKorean(t *testing.T) {
	for _, s := range []Sample{
		full(),
		{CPU: f(90), MemUsed: 1, MemTotal: 2, Battery: f(5)},
		{CPUErr: errors.New("top failed"), MemErr: ErrUnsupported, BatteryErr: ErrUnsupported},
		{},
	} {
		raw, _ := json.Marshal(Build(s, now))
		texttest.Check(t, raw)
	}
}

func TestParseMac(t *testing.T) {
	top := "Processes: 1\nCPU usage: 22.38% user, 19.70% sys, 57.91% idle \nProcesses: 1\nCPU usage: 13.55% user, 6.83% sys, 79.61% idle \n"
	if v, err := parseTop(top); err != nil || v < 20.38 || v > 20.4 {
		t.Fatalf("top %v %v", v, err)
	}
	vm := "Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free:      8678.\nPages wired down:     149222.\nPages purgeable:   1509.\n\"Translation faults\":    3314609252.\nAnonymous pages:   420270.\nPages occupied by compressor:   250961.\n"
	if v, err := parseVMStat(vm); err != nil || v != (420270-1509+149222+250961)*16384 {
		t.Fatalf("vm_stat %v %v", v, err)
	}
	if v, err := parseSwapUsage("total = 4096.00M  used = 3448.12M  free = 647.88M  (encrypted)"); err != nil || v != 3615615877 {
		t.Fatalf("swap %v %v", v, err)
	}
	cases := map[string][3]any{
		"Now drawing from 'AC Power'\n -InternalBattery-0 (id=1)\t100%; charged; 0:00 remaining present: true":        {100.0, false, true},
		"Now drawing from 'AC Power'\n -InternalBattery-0 (id=1)\t42%; charging; 1:10 remaining present: true":        {42.0, true, true},
		"Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t9%; discharging; 0:20 remaining present: true": {9.0, false, true},
		"Now drawing from 'AC Power'\n": {0.0, false, false},
	}
	for in, want := range cases {
		p, c, ok := parsePmset(in)
		if [3]any{p, c, ok} != want {
			t.Errorf("%q: %v %v %v", in, p, c, ok)
		}
	}
	for _, bad := range []func() error{
		func() error { _, err := parseTop("x"); return err },
		func() error { _, err := parseVMStat("x"); return err },
		func() error { _, err := parseSwapUsage("x"); return err },
	} {
		if bad() == nil {
			t.Error("read failure should error")
		}
	}
}

func TestParseLinux(t *testing.T) {
	t0, i0, err := parseProcStat("cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 1 2 3 4\n")
	t1, i1, _ := parseProcStat("cpu  200 0 200 1300 100 0 0 0 0 0\n")
	if err != nil || t0 != 1000 || i0 != 800 {
		t.Fatalf("%d %d %v", t0, i0, err)
	}
	if v, err := cpuBetween(t0, i0, t1, i1); err != nil || v != 25 {
		t.Fatalf("%v %v", v, err)
	}
	used, total, swap, err := parseMeminfo("MemTotal:       16000000 kB\nMemFree:  1000 kB\nMemAvailable:    4000000 kB\nSwapTotal: 2000 kB\nSwapFree: 500 kB\n")
	if err != nil || used != 12000000*1024 || total != 16000000*1024 || swap != 1500*1024 {
		t.Fatalf("%d %d %d %v", used, total, swap, err)
	}
	if _, _, err := parseProcStat("x"); err == nil {
		t.Fatal("read failure should error")
	}
	if _, _, _, err := parseMeminfo("x"); err == nil {
		t.Fatal("read failure should error")
	}
}

func TestMeasureOnThisComputer(t *testing.T) {
	b := Build(Measure(context.Background()), time.Now())
	if b.CPUPct == nil || b.MemPct == nil || *b.MemTotalGB <= 0 {
		t.Fatalf("%+v", b.Status)
	}
	raw, _ := json.Marshal(b)
	t.Logf("%s", raw)
}

func TestRunPublishesAndRefreshes(t *testing.T) {
	got := make(chan Bundle, 4)
	n := 0.0
	s := &Sensor{Every: time.Hour, Measure: func(context.Context) Sample { n += 10; return Sample{CPU: f(n)} },
		Push: func(raw json.RawMessage) { var b Bundle; json.Unmarshal(raw, &b); got <- b }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	if b := <-got; *b.CPUPct != 10 {
		t.Fatalf("first measurement %+v", b)
	}
	s.Refresh()
	select {
	case b := <-got:
		if *b.CPUPct != 20 {
			t.Fatalf("refresh %v", *b.CPUPct)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh was not uploaded")
	}
}
