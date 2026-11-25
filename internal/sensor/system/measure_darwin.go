package system

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func run(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return "", fmt.Errorf("%s 실패: %w", name[strings.LastIndex(name, "/")+1:], err)
	}
	return string(out), nil
}

// Measure measures on a Mac with top, vm_stat, sysctl, and pmset. top waits 1 second between samples.
func Measure(ctx context.Context) Sample {
	var s Sample
	if out, err := run(ctx, "/usr/bin/top", "-l", "2", "-n", "0", "-s", "1"); err != nil {
		s.CPUErr = err
	} else if v, err := parseTop(out); err != nil {
		s.CPUErr = err
	} else {
		s.CPU = &v
	}
	if out, err := run(ctx, "/usr/sbin/sysctl", "-n", "hw.memsize"); err != nil {
		s.MemErr = err
	} else if total, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64); err != nil {
		s.MemErr = fmt.Errorf("hw.memsize 를 못 읽었다")
	} else if out, err := run(ctx, "/usr/bin/vm_stat"); err != nil {
		s.MemErr = err
	} else if used, err := parseVMStat(out); err != nil {
		s.MemErr = err
	} else {
		s.MemUsed, s.MemTotal = min(used, total), total
	}
	if out, err := run(ctx, "/usr/sbin/sysctl", "-n", "vm.swapusage"); err == nil {
		if v, err := parseSwapUsage(out); err == nil {
			s.Swap = &v
		}
	}
	if out, err := run(ctx, "/usr/bin/pmset", "-g", "batt"); err != nil {
		s.BatteryErr = err
	} else if pct, charging, ok := parsePmset(out); ok {
		s.Battery, s.Charging = &pct, &charging
	}
	return s
}
