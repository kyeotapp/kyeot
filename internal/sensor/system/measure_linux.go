package system

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Measure reads /proc and /sys on Linux. CPU is the difference between two reads 1 second apart.
func Measure(ctx context.Context) Sample {
	var s Sample
	if t0, i0, err := readProcStat(); err != nil {
		s.CPUErr = err
	} else {
		select {
		case <-ctx.Done():
			s.CPUErr = ctx.Err()
		case <-time.After(time.Second):
			if t1, i1, err := readProcStat(); err != nil {
				s.CPUErr = err
			} else if v, err := cpuBetween(t0, i0, t1, i1); err != nil {
				s.CPUErr = err
			} else {
				s.CPU = &v
			}
		}
	}
	if raw, err := os.ReadFile("/proc/meminfo"); err != nil {
		s.MemErr = err
	} else if used, total, swap, err := parseMeminfo(string(raw)); err != nil {
		s.MemErr = err
	} else {
		s.MemUsed, s.MemTotal, s.Swap = used, total, &swap
	}
	bats, _ := filepath.Glob("/sys/class/power_supply/BAT*")
	if len(bats) > 0 {
		cap, err := os.ReadFile(filepath.Join(bats[0], "capacity"))
		if err != nil {
			s.BatteryErr = err
			return s
		}
		pct, err := strconv.ParseFloat(strings.TrimSpace(string(cap)), 64)
		if err != nil {
			s.BatteryErr = err
			return s
		}
		st, _ := os.ReadFile(filepath.Join(bats[0], "status"))
		charging := strings.TrimSpace(string(st)) == "Charging"
		s.Battery, s.Charging = &pct, &charging
	}
	return s
}

func readProcStat() (uint64, uint64, error) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	return parseProcStat(string(raw))
}
