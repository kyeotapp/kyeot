// Package system measures this computer's CPU, memory, swap, and battery every 30 seconds and builds the system bundle for the kyeot "내 컴퓨터" (My computer) tab (sensor template).
package system

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/kyeotapp/kyeot/internal/sensor/text"
)

const BundleName = "system"

// thresholds
const (
	cpuWarn       = 85.0
	memWarn       = 90.0
	batteryWarn   = 20.0
	batteryDanger = 10.0
)

// ErrUnsupported means there is no way to measure on this operating system yet.
var ErrUnsupported = errors.New("이 운영체제에서는 아직 못 잰다")

// Sample is one measurement. A field that couldn't be measured is nil plus its reason (…Err). For the battery, if Battery and BatteryErr are both nil it means "none".
type Sample struct {
	CPU        *float64 // 0~100
	CPUErr     error
	MemUsed    uint64 // bytes
	MemTotal   uint64 // 0 means unmeasurable
	MemErr     error
	Swap       *uint64 // bytes
	Battery    *float64
	Charging   *bool
	BatteryErr error
}

type statusItem struct {
	Label string `json:"label"`
	State string `json:"state"`
	Note  string `json:"note"`
}

// Bundle is the shape of the system bundle. Values that couldn't be measured are null and status gives the reason.
type Bundle struct {
	CPUPct     *float64     `json:"cpu_pct"`
	MemPct     *float64     `json:"mem_pct"`
	MemUsedGB  *float64     `json:"mem_used_gb"`
	MemTotalGB *float64     `json:"mem_total_gb"`
	SwapGB     *float64     `json:"swap_gb"`
	BatteryPct *float64     `json:"battery_pct"`
	Charging   *bool        `json:"charging"`
	Checked    string       `json:"checked"`
	Status     []statusItem `json:"status"`
	Text       text.Map     `json:"$text"`
}

func round1(v float64) *float64 { r := math.Round(v*10) / 10; return &r }

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

const gib = 1 << 30

// failed is the status line for a field that couldn't be measured.
func failed(b *Bundle, i int, label string, err error) statusItem {
	if errors.Is(err, ErrUnsupported) {
		b.Text.Put(text.Path("status", i, "note"), text.M("system.unsupported"))
		return statusItem{label, "off", ErrUnsupported.Error()}
	}
	b.Text.Put(text.Path("status", i, "note"), text.M("sensor.measure_failed_reason", "reason", err.Error()))
	return statusItem{label, "off", "측정 못 함: " + err.Error()}
}

// Build makes the bundle from the measured values. Memory GB is 2^30 bytes (same unit as Activity Monitor).
func Build(s Sample, now time.Time) Bundle {
	b := Bundle{Checked: now.Format("2006-01-02 15:04:05"), Text: text.Map{}}
	b.Text.Put("status.0.label", text.M("system.label.cpu"))
	b.Text.Put("status.1.label", text.M("system.label.memory"))
	b.Text.Put("status.2.label", text.M("system.label.battery"))

	cpu := statusItem{"CPU", "ok", ""}
	switch {
	case s.CPU != nil:
		b.CPUPct = round1(*s.CPU)
		if *b.CPUPct >= cpuWarn {
			cpu.State = "warn"
		}
		cpu.Note = num(*b.CPUPct) + "%"
		b.Text.Put("status.0.note", text.M("system.pct", "n", *b.CPUPct))
	default:
		cpu = failed(&b, 0, "CPU", cmpErr(s.CPUErr))
	}

	mem := statusItem{"메모리", "ok", ""}
	switch {
	case s.MemTotal > 0:
		b.MemUsedGB, b.MemTotalGB = round1(float64(s.MemUsed)/gib), round1(float64(s.MemTotal)/gib)
		b.MemPct = round1(float64(s.MemUsed) / float64(s.MemTotal) * 100)
		if *b.MemPct >= memWarn {
			mem.State = "warn"
		}
		mem.Note = num(*b.MemUsedGB) + " / " + num(*b.MemTotalGB) + "GB · " + num(*b.MemPct) + "%"
		b.Text.Put("status.1.note", text.M("system.memory_note", "used", *b.MemUsedGB, "total", *b.MemTotalGB, "pct", *b.MemPct))
	default:
		mem = failed(&b, 1, "메모리", cmpErr(s.MemErr))
	}
	if s.Swap != nil {
		b.SwapGB = round1(float64(*s.Swap) / gib)
	}

	bat := statusItem{"배터리", "ok", ""}
	switch {
	case s.Battery != nil:
		b.BatteryPct, b.Charging = round1(*s.Battery), s.Charging
		switch {
		case *b.BatteryPct <= batteryDanger:
			bat.State = "danger"
		case *b.BatteryPct <= batteryWarn:
			bat.State = "warn"
		}
		bat.Note = num(*b.BatteryPct) + "%"
		key := "system.pct"
		if s.Charging != nil && *s.Charging {
			bat.Note += " · 충전 중"
			key = "system.battery.charging"
		}
		b.Text.Put("status.2.note", text.M(key, "n", *b.BatteryPct))
	case s.BatteryErr != nil:
		bat = failed(&b, 2, "배터리", s.BatteryErr)
	default:
		bat = statusItem{"배터리", "off", "배터리 없음"}
		b.Text.Put("status.2.note", text.M("system.battery.none"))
	}
	b.Status = []statusItem{cpu, mem, bat}
	return b
}

func cmpErr(err error) error {
	if err == nil {
		return errors.New("값이 없다")
	}
	return err
}

type Sensor struct {
	Every   time.Duration // default 30 seconds
	Measure func(context.Context) Sample
	Push    func(json.RawMessage)
	Now     func() time.Time

	once    sync.Once
	refresh chan struct{}
}

func New(push func(json.RawMessage)) *Sensor { return &Sensor{Push: push} }

func (s *Sensor) ch() chan struct{} {
	s.once.Do(func() { s.refresh = make(chan struct{}, 1) })
	return s.refresh
}

// Refresh tells Run to measure and upload once now. It doesn't wait.
func (s *Sensor) Refresh() {
	select {
	case s.ch() <- struct{}{}:
	default:
	}
}

func (s *Sensor) Publish(ctx context.Context) {
	measure, now := s.Measure, s.Now
	if measure == nil {
		measure = Measure
	}
	if now == nil {
		now = time.Now
	}
	sample := measure(ctx)
	if raw, err := json.Marshal(Build(sample, now())); err == nil && s.Push != nil {
		s.Push(raw)
	}
}

// Run measures and uploads at start, every Every, and on a refresh request.
func (s *Sensor) Run(ctx context.Context) {
	every := s.Every
	if every <= 0 {
		every = 30 * time.Second
	}
	refresh := s.ch()
	t := time.NewTicker(every)
	defer t.Stop()
	s.Publish(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-refresh:
		}
		s.Publish(ctx)
	}
}
