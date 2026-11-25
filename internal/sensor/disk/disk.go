// Package disk measures this computer's internal disk free space and usage and builds the disk bundle for the kyeot disk tab (sensor template).
// The original is the Hermes script disk_deck.py (based on df -k). On a Mac it looks at the system data volume, elsewhere at /.
package disk

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kyeotapp/kyeot/internal/sensor/text"
)

const BundleName = "disk"

// Usage is the byte counts on the same basis as df. used = total - free blocks, avail = blocks an ordinary user can use.
type Usage struct{ Used, Avail uint64 }

// Bundle is the shape of the disk bundle. If measurement fails, free_gb and used_pct are null and status gives the reason.
type Bundle struct {
	FreeGB  *float64 `json:"free_gb"`
	UsedPct *float64 `json:"used_pct"`
	UsedGB  *float64 `json:"used_gb"`
	TotalGB *float64 `json:"total_gb"`
	Usage   string   `json:"usage"` // "used / total GB"
	Checked string   `json:"checked"`
	Status  string   `json:"status"`
	Text    text.Map `json:"$text"`
}

func volume() string {
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat("/System/Volumes/Data"); err == nil {
			return "/System/Volumes/Data" // same basis as the available space Finder shows
		}
	}
	return "/"
}

// measureDisk runs df -k on a Mac. APFS volumes share one container, so statfs's total minus free includes other volumes' shares.
func measureDisk() (Usage, error) {
	if runtime.GOOS != "darwin" {
		return statfs(volume())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/bin/df", "-k", volume()).Output()
	if err != nil {
		return Usage{}, fmt.Errorf("df 실패: %w", err)
	}
	return parseDf(string(out))
}

func parseDf(out string) (Usage, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	f := strings.Fields(lines[len(lines)-1])
	if len(f) < 4 {
		return Usage{}, fmt.Errorf("df 출력을 못 읽었다")
	}
	used, err1 := strconv.ParseUint(f[2], 10, 64)
	avail, err2 := strconv.ParseUint(f[3], 10, 64)
	if err1 != nil || err2 != nil {
		return Usage{}, fmt.Errorf("df 출력을 못 읽었다")
	}
	return Usage{Used: used * 1024, Avail: avail * 1024}, nil
}

func round1(v float64) *float64 { r := math.Round(v*10) / 10; return &r }

// Build makes the bundle from the measured values. Capacity is in units of 10^9 bytes (GB); usage is used / (used + avail).
func Build(u Usage, err error, now time.Time) Bundle {
	b := Bundle{Checked: now.Format("2006-01-02 15:04"), Status: "정상", Text: text.Map{"status": text.M("disk.status.ok")}}
	if err == nil && u.Used+u.Avail == 0 {
		err = errEmpty
	}
	if err != nil {
		b.Status = "측정 못 함: " + err.Error()
		b.Text["status"] = text.M("sensor.measure_failed_reason", "reason", err.Error())
		return b
	}
	b.FreeGB = round1(float64(u.Avail) / 1e9)
	b.UsedPct = round1(float64(u.Used) / float64(u.Used+u.Avail) * 100)
	b.UsedGB = round1(float64(u.Used) / 1e9)
	b.TotalGB = round1(float64(u.Used+u.Avail) / 1e9)
	b.Usage = strconv.FormatFloat(*b.UsedGB, 'f', -1, 64) + " / " + strconv.FormatFloat(*b.TotalGB, 'f', -1, 64) + " GB"
	b.Text["usage"] = text.M("disk.usage", "used", *b.UsedGB, "total", *b.TotalGB)
	return b
}

type emptyErr struct{}

func (emptyErr) Error() string { return "디스크 크기가 0 이다" }

var errEmpty = emptyErr{}

type Sensor struct {
	Every   time.Duration // default 10 minutes
	Measure func() (Usage, error)
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

func (s *Sensor) Publish() {
	measure, now := s.Measure, s.Now
	if measure == nil {
		measure = measureDisk
	}
	if now == nil {
		now = time.Now
	}
	u, err := measure()
	if raw, jerr := json.Marshal(Build(u, err, now())); jerr == nil && s.Push != nil {
		s.Push(raw)
	}
}

// Run measures and uploads at start, every Every, and on a refresh request.
func (s *Sensor) Run(ctx context.Context) {
	every := s.Every
	if every <= 0 {
		every = 10 * time.Minute
	}
	refresh := s.ch()
	t := time.NewTicker(every)
	defer t.Stop()
	s.Publish()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-refresh:
		}
		s.Publish()
	}
}
