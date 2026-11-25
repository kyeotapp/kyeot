package system

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Reads the output of measurement commands and files. Gathered here so it can be tested independently of the OS.

var topIdle = regexp.MustCompile(`CPU usage:.*?([\d.]+)% idle`)

// parseTop derives usage from the last "CPU usage" line of top -l 2. The first sample is cumulative since boot, so it is discarded.
func parseTop(out string) (float64, error) {
	m := topIdle.FindAllStringSubmatch(out, -1)
	if len(m) == 0 {
		return 0, fmt.Errorf("top 출력을 못 읽었다")
	}
	idle, err := strconv.ParseFloat(m[len(m)-1][1], 64)
	if err != nil {
		return 0, fmt.Errorf("top 출력을 못 읽었다")
	}
	return max(0, 100-idle), nil
}

var vmLine = regexp.MustCompile(`^(.+?):\s+(\d+)\.?$`)

// parseVMStat returns Activity Monitor's "Memory Used" (app memory + wired + compressed) in bytes.
// App memory = Anonymous pages − Pages purgeable.
func parseVMStat(out string) (uint64, error) {
	page := uint64(4096)
	if m := regexp.MustCompile(`page size of (\d+) bytes`).FindStringSubmatch(out); m != nil {
		page, _ = strconv.ParseUint(m[1], 10, 64)
	}
	v := map[string]uint64{}
	for _, line := range strings.Split(out, "\n") {
		if m := vmLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			n, _ := strconv.ParseUint(m[2], 10, 64)
			v[strings.Trim(m[1], `"`)] = n
		}
	}
	anon, ok1 := v["Anonymous pages"]
	wired, ok2 := v["Pages wired down"]
	comp, ok3 := v["Pages occupied by compressor"]
	if !ok1 || !ok2 || !ok3 {
		return 0, fmt.Errorf("vm_stat 출력을 못 읽었다")
	}
	app := anon - min(anon, v["Pages purgeable"])
	return (app + wired + comp) * page, nil
}

var swapUsed = regexp.MustCompile(`used = ([\d.]+)([KMG])`)

// parseSwapUsage returns the used value of sysctl vm.swapusage in bytes.
func parseSwapUsage(out string) (uint64, error) {
	m := swapUsed.FindStringSubmatch(out)
	if m == nil {
		return 0, fmt.Errorf("vm.swapusage 를 못 읽었다")
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("vm.swapusage 를 못 읽었다")
	}
	unit := map[string]float64{"K": 1 << 10, "M": 1 << 20, "G": 1 << 30}[m[2]]
	return uint64(n * unit), nil
}

var battLine = regexp.MustCompile(`(\d+)%;\s*([^;]+);`)

// parsePmset reads pmset -g batt. If there is no internal battery line, ok=false (no battery).
func parsePmset(out string) (pct float64, charging bool, ok bool) {
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "InternalBattery") {
			continue
		}
		m := battLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		p, _ := strconv.ParseFloat(m[1], 64)
		st := strings.TrimSpace(m[2])
		return p, st == "charging" || st == "finishing charge", true
	}
	return 0, false, false
}

// cpuTimes is the (total, idle) time from the first line of /proc/stat.
func parseProcStat(out string) (total, idle uint64, err error) {
	line, _, _ := strings.Cut(out, "\n")
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return 0, 0, fmt.Errorf("/proc/stat 을 못 읽었다")
	}
	for i, s := range f[1:] {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("/proc/stat 을 못 읽었다")
		}
		total += n
		if i == 3 || i == 4 { // idle, iowait
			idle += n
		}
	}
	return total, idle, nil
}

// cpuBetween is the usage between two samples.
func cpuBetween(t0, i0, t1, i1 uint64) (float64, error) {
	if t1 <= t0 {
		return 0, fmt.Errorf("CPU 시간이 늘지 않았다")
	}
	busy := float64((t1-t0)-(min(i1-i0, t1-t0))) / float64(t1-t0) * 100
	return busy, nil
}

// parseMeminfo returns (used, total, swap used) bytes from /proc/meminfo. used = MemTotal − MemAvailable.
func parseMeminfo(out string) (used, total, swap uint64, err error) {
	v := map[string]uint64{}
	for _, line := range strings.Split(out, "\n") {
		k, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		n, _ := strconv.ParseUint(f[0], 10, 64)
		v[k] = n * 1024
	}
	total, avail := v["MemTotal"], v["MemAvailable"]
	if total == 0 || avail > total {
		return 0, 0, 0, fmt.Errorf("/proc/meminfo 를 못 읽었다")
	}
	return total - avail, total, v["SwapTotal"] - min(v["SwapFree"], v["SwapTotal"]), nil
}
