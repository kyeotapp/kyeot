package cron

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Counts routine schedules (five cron fields) and describes them in words. Used by the server's report normalization and the Hermes bridge.
// Ported from a personal dashboard script's cron_field, cron_runs, cron_text.

var days = []string{"일", "월", "화", "수", "목", "금", "토", "일"}

func Field(spec string, lo, hi int) (map[int]bool, error) {
	vals := map[int]bool{}
	for _, part := range strings.Split(spec, ",") {
		step := 1
		if base, s, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(s)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("step %q", s)
			}
			part, step = base, n
		}
		a, b := lo, hi
		switch {
		case part == "*":
		case strings.Contains(part, "-"):
			x, y, _ := strings.Cut(part, "-")
			var err1, err2 error
			a, err1 = strconv.Atoi(x)
			b, err2 = strconv.Atoi(y)
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("range %q", part)
			}
		default:
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("value %q", part)
			}
			a, b = n, n
		}
		if a < lo || b > hi || a > b {
			return nil, fmt.Errorf("out of range %q", part)
		}
		for v := a; v <= b; v += step {
			vals[v] = true
		}
	}
	return vals, nil
}

// cronRuns returns the run times within [start, end), or nil if the schedule can't be parsed.
func Runs(expr string, start, end time.Time) []time.Time {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil
	}
	mins, e1 := Field(f[0], 0, 59)
	hours, e2 := Field(f[1], 0, 23)
	doms, e3 := Field(f[2], 1, 31)
	mons, e4 := Field(f[3], 1, 12)
	dowRaw, e5 := Field(f[4], 0, 7)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil {
		return nil
	}
	dows := map[int]bool{}
	for d := range dowRaw {
		dows[d%7] = true
	}
	hs, ms := sortedKeys(hours), sortedKeys(mins)
	var out []time.Time
	day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, start.Location())
	for day.Before(end) {
		if mons[int(day.Month())] && doms[day.Day()] && dows[int(day.Weekday())] {
			for _, h := range hs {
				for _, m := range ms {
					t := time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, day.Location())
					if !t.Before(start) && t.Before(end) {
						out = append(out, t)
					}
				}
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return out
}

func sortedKeys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func Text(expr string) string {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return expr
	}
	if step, ok := strings.CutPrefix(f[0], "*/"); ok && f[2] == "*" && f[3] == "*" && f[4] == "*" {
		return EveryText(step, f[1], expr)
	}
	m, err1 := strconv.Atoi(f[0])
	h, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil {
		return expr
	}
	at := fmt.Sprintf("%02d:%02d", h, m)
	dom, mon, dow := f[2], f[3], f[4]
	if dom == "*" && mon == "*" {
		switch dow {
		case "*":
			return "매일 " + at
		case "1-5":
			return "평일 " + at
		}
		var names []string
		for _, x := range strings.Split(dow, ",") {
			n, err := strconv.Atoi(x)
			if err != nil || n < 0 || n > 7 {
				return expr
			}
			names = append(names, days[n])
		}
		return "매주 " + strings.Join(names, ", ") + " " + at
	}
	d, err3 := strconv.Atoi(dom)
	mo, err4 := strconv.Atoi(mon)
	if err3 == nil && err4 == nil && dow == "*" {
		return fmt.Sprintf("%d월 %d일 %s", mo, d, at)
	}
	return expr
}

// everyText turns "*/15 0,7-23 * * *" into "15분마다 (0시, 7~23시)".
func EveryText(step, hours, expr string) string {
	n, err := strconv.Atoi(step)
	if err != nil || n <= 0 {
		return expr
	}
	head := strconv.Itoa(n) + "분마다"
	if hours == "*" {
		return head
	}
	var parts []string
	for _, x := range strings.Split(hours, ",") {
		a, b, isRange := strings.Cut(x, "-")
		_, e1 := strconv.Atoi(a)
		_, e2 := strconv.Atoi(b)
		switch {
		case isRange && e1 == nil && e2 == nil:
			parts = append(parts, a+"~"+b+"시")
		case !isRange && e1 == nil:
			parts = append(parts, a+"시")
		default:
			return expr
		}
	}
	return head + " (" + strings.Join(parts, ", ") + ")"
}
