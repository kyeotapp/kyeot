// Package report shapes the report an agent sends (routine list and costs) into the form the app expects.
// Agents send only the raw data. Missing lists become empty lists, states become English, and the server builds the schedule text and display names.
// So agents don't need to know the Hermes bridge's format (AGENTS.md, the rule against changes that add new demands on agents).
package report

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kyeotapp/kyeot/internal/cron"
)

// Canonical routine states. Old bridge Korean values and common English synonyms are accepted too.
const (
	StateOK    = "ok"
	StateOff   = "off"
	StateDone  = "done"
	StateError = "error"
)

var stateAlias = map[string]string{
	"ok": StateOK, "정상": StateOK, "on": StateOK, "active": StateOK, "running": StateOK, "enabled": StateOK,
	"off": StateOff, "꺼짐": StateOff, "paused": StateOff, "disabled": StateOff, "stopped": StateOff,
	"done": StateDone, "끝남": StateDone, "completed": StateDone, "finished": StateDone,
	"error": StateError, "오류": StateError, "failed": StateError, "fail": StateError,
}

// Features are the agent capabilities that can be learned from the report.
type Features struct {
	Routines bool // sent a routines list (even if empty)
	Costs    bool // periods or daily has values
}

// Normalize normalizes the report JSON. kind is the agent kind (hello's kind). Unknown fields are left as they are.
func Normalize(raw json.RawMessage, kind string, now time.Time) (json.RawMessage, Features, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, Features{}, fmt.Errorf("report is not an object")
	}
	var f Features
	_, f.Routines = m["routines"]
	routines := list(m["routines"])
	out := make([]any, 0, len(routines))
	for _, r := range routines {
		if rm, ok := r.(map[string]any); ok && normalizeRoutine(rm, kind) {
			out = append(out, rm)
		}
	}
	m["routines"] = out
	periods := list(m["periods"])
	for _, p := range periods {
		if pm, ok := p.(map[string]any); ok {
			normalizePeriod(pm, kind)
		}
	}
	m["periods"] = periods
	daily := list(m["daily"])
	for _, d := range daily {
		if dm, ok := d.(map[string]any); ok {
			titleRoutines(dm, kind)
		}
	}
	m["daily"] = daily
	f.Costs = len(periods) > 0 || len(daily) > 0
	if n, _ := m["generatedAt"].(float64); n <= 0 {
		m["generatedAt"] = now.UnixMilli()
	}
	b, err := json.Marshal(m)
	return b, f, err
}

func list(v any) []any {
	l, _ := v.([]any)
	if l == nil {
		return []any{}
	}
	return l
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// normalizeRoutine shapes one routine. Returns false (discarded) if it has neither id nor name.
func normalizeRoutine(r map[string]any, kind string) bool {
	id, name := strings.TrimSpace(str(r, "id")), strings.TrimSpace(str(r, "name"))
	if id == "" && name == "" {
		return false
	}
	if id == "" {
		id = name
	}
	if name == "" {
		name = id
	}
	r["id"], r["name"] = id, name
	if _, has := r["title"]; !has {
		r["title"] = Title(name, kind)
	}

	state := stateAlias[strings.ToLower(strings.TrimSpace(str(r, "state")))]
	on, hasOn := r["on"].(bool)
	if state == "" {
		state = StateOK
		if hasOn && !on {
			state = StateOff
		}
	}
	r["state"] = state
	if !hasOn {
		r["on"] = state == StateOK || state == StateError
	}

	once, _ := r["once"].(bool)
	if str(r, "schedule") == "" {
		r["schedule"] = Schedule(str(r, "cron"), num(r["every"]), at(r["at"]), once)
	}
	for _, k := range []string{"lastStatus", "deliver", "about"} {
		if _, ok := r[k].(string); !ok {
			r[k] = ""
		}
	}
	for _, k := range []string{"nextRun", "lastRun", "avgCost"} {
		if _, ok := r[k].(float64); !ok {
			r[k] = nil
		}
	}
	if _, ok := r["runs"].(float64); !ok {
		r["runs"] = 0
	}
	if _, ok := r["noAgent"].(bool); !ok {
		r["noAgent"] = false
	}
	return true
}

func normalizePeriod(p map[string]any, kind string) {
	for _, k := range []string{"key", "name", "compareName"} {
		if _, ok := p[k].(string); !ok {
			p[k] = ""
		}
	}
	for _, k := range []string{"cost", "before", "sessions", "tokens", "unpriced"} {
		if _, ok := p[k].(float64); !ok {
			p[k] = 0
		}
	}
	p["bySource"] = list(p["bySource"])
	titleRoutines(p, kind)
}

// titleRoutines adds display names to byRoutine (per-routine cost) rows. Routines that no longer exist get the same shape as in the routine list.
func titleRoutines(m map[string]any, kind string) {
	l, _ := m["byRoutine"].([]any)
	for _, v := range l {
		if rc, ok := v.(map[string]any); ok && str(rc, "title") == "" {
			rc["title"] = Title(str(rc, "name"), kind)
		}
	}
}

func num(v any) int {
	f, _ := v.(float64)
	return int(f)
}

func at(v any) *int64 {
	f, ok := v.(float64)
	if !ok || f <= 0 {
		return nil
	}
	n := int64(f)
	return &n
}

// Title is the routine's display name. Hermes hyphenates names, so hyphens become spaces. Other agents keep the name as is.
func Title(name, kind string) string {
	if strings.EqualFold(kind, "hermes") {
		return strings.Join(strings.Fields(strings.ReplaceAll(name, "-", " ")), " ")
	}
	return name
}

// Schedule turns the raw schedule (cron expression, minute interval, one-time instant) into Korean text for old apps. New apps convert the raw schedule into their own language.
func Schedule(expr string, every int, at *int64, once bool) string {
	switch {
	case at != nil:
		return time.UnixMilli(*at).Format("1월 2일 15:04") + ", 한 번"
	case every > 0:
		if every%60 == 0 {
			return fmt.Sprintf("%d시간마다", every/60)
		}
		return fmt.Sprintf("%d분마다", every)
	case expr != "":
		text := cron.Text(expr)
		if once {
			text += ", 한 번"
		}
		return text
	}
	return ""
}
