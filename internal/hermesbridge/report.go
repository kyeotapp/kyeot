package hermesbridge

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kyeotapp/kyeot/internal/cron"
	"github.com/kyeotapp/kyeot/internal/report"
	_ "modernc.org/sqlite"
)

// Report format; field names must match Report in app/src/lib/protocol.ts.
// Calculation follows the jobs, usage and forecast of the personal dashboard script.

type Report struct {
	GeneratedAt int64     `json:"generatedAt"`
	Routines    []Routine `json:"routines"`
	Periods     []Period  `json:"periods"`
	Forecast    *Forecast `json:"forecast"`
	// Per-day records of the last dailyDays days; the app computes its chosen period from these. Oldest day first
	Daily []DayCost `json:"daily"`
}

type DayCost struct {
	Date      string             `json:"date"` // 2006-01-02, in this Mac's time zone
	Cost      float64            `json:"cost"`
	Sessions  int                `json:"sessions"`
	Tokens    int64              `json:"tokens"`
	Unpriced  int                `json:"unpriced"`
	BySource  map[string]float64 `json:"bySource"`
	ByRoutine []RoutineCost      `json:"byRoutine,omitempty"`
}

type Routine struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Schedule   string   `json:"schedule"`        // Korean text for old apps
	Cron       string   `json:"cron,omitempty"`  // original cron expression; new apps render it in their own language
	Every      int      `json:"every,omitempty"` // routine with a minute interval
	At         *int64   `json:"at,omitempty"`    // time of the single run
	State      string   `json:"state"`           // ok, off, done, error (canonical values of internal/report)
	NextRun    *int64   `json:"nextRun"`
	LastRun    *int64   `json:"lastRun"`
	LastStatus string   `json:"lastStatus"`
	Deliver    string   `json:"deliver"`
	About      string   `json:"about"`
	NoAgent    bool     `json:"noAgent"`
	Runs       int      `json:"runs"`                // runs in the aggregation period (those with a cost)
	AvgCost    *float64 `json:"avgCost"`             // average USD per run
	MonthCost  *float64 `json:"monthCost,omitempty"` // USD this month
	MonthRuns  int      `json:"monthRuns,omitempty"`
	Error      string   `json:"error,omitempty"`

	Once      bool        `json:"once"`      // routine that runs once
	On        bool        `json:"on"`        // is on (switch)
	Completed int         `json:"completed"` // runs so far
	Script    string      `json:"script"`    // script path; empty if none
	Info      *ScriptInfo `json:"info"`      // what was learned by reading the script
}

type Period struct {
	Key         string        `json:"key"`
	Name        string        `json:"name"`
	CompareName string        `json:"compareName"`
	Cost        float64       `json:"cost"`
	Before      float64       `json:"before"`
	Sessions    int           `json:"sessions"`
	Tokens      int64         `json:"tokens"`
	Unpriced    int           `json:"unpriced"`
	BySource    []SourceCost  `json:"bySource"`
	ByRoutine   []RoutineCost `json:"byRoutine"` // sum = routine amount in BySource
	Days        []float64     `json:"days"`
}

type RoutineCost struct {
	Name string  `json:"name"`
	Runs int     `json:"runs"`
	Cost float64 `json:"cost"`
	Gone bool    `json:"gone,omitempty"` // no longer in jobs.json
}

const unnamedRoutine = "이름 없는 루틴"

// routineName is the leading part ("<routine> · …") of the title of a routine-owned conversation
func routineName(title string) string {
	return cmp.Or(strings.TrimSpace(strings.SplitN(title, " · ", 2)[0]), unnamedRoutine)
}

type SourceCost struct {
	Label string  `json:"label"`
	Cost  float64 `json:"cost"`
}

type Forecast struct {
	Days     int           `json:"days"`
	Rows     []ForecastRow `json:"rows"`
	Unknown  []string      `json:"unknown"`
	Routines float64       `json:"routines"`
	Other    float64       `json:"other"`
}

type ForecastRow struct {
	Name   string  `json:"name"`
	Runs   int     `json:"runs"`
	PerRun float64 `json:"perRun"`
	Total  float64 `json:"total"`
}

const (
	horizonDays  = 7
	forecastDays = 30
	dailyDays    = 180
)

var sources = map[string]string{"cron": "루틴", "discord": "디스코드", "oneshot": "직접 실행", "subagent": "하위 에이전트",
	"cli": "터미널", "tui": "터미널", "telegram": "텔레그램", "api_server": "곁"}

type rawJob struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Prompt   string `json:"prompt"`
	Script   string `json:"script"`
	NoAgent  bool   `json:"no_agent"`
	Enabled  *bool  `json:"enabled"`
	State    string `json:"state"`
	Deliver  string `json:"deliver"`
	NextRun  string `json:"next_run_at"`
	LastRun  string `json:"last_run_at"`
	Status   string `json:"last_status"`
	Error    string `json:"last_error"`
	Failures int    `json:"failure_streak"`
	Schedule struct {
		Kind    string  `json:"kind"`
		Expr    string  `json:"expr"`
		Minutes float64 `json:"minutes"`
		RunAt   string  `json:"run_at"`
	} `json:"schedule"`
	Repeat struct {
		Times     int `json:"times"`
		Completed int `json:"completed"`
	} `json:"repeat"`
}

func readJobs(home string) []rawJob {
	raw, err := os.ReadFile(filepath.Join(home, "cron", "jobs.json"))
	if err != nil {
		return nil
	}
	var doc struct {
		Jobs []json.RawMessage `json:"jobs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		logOnce("jobs.json", "could not read jobs.json: %v", err)
		return nil
	}
	jobs := make([]rawJob, 0, len(doc.Jobs))
	for _, b := range doc.Jobs { // one broken entry does not hide the rest
		var j rawJob
		if err := json.Unmarshal(b, &j); err != nil {
			logOnce("jobs.json job", "could not read one routine in jobs.json: %v", err)
			continue
		}
		jobs = append(jobs, j)
	}
	return jobs
}

func parseTime(s string) *int64 {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			ms := t.UnixMilli()
			return &ms
		}
	}
	return nil
}

var sentenceEnd = regexp.MustCompile(`^.+?[.!?。！？](\s|$)`)

func firstSentence(text string) string {
	text = strings.TrimLeft(strings.Join(strings.Fields(text), " "), "#*->` ") // strip markdown leading marks
	if m := sentenceEnd.FindString(text); m != "" {
		text = strings.TrimSpace(m)
	}
	if r := []rune(text); len(r) > 120 {
		return string(r[:119]) + "…"
	}
	return text
}

var platforms = map[string]string{"local": "봇 안에만 저장", "origin": "만든 대화로", "discord": "디스코드", "telegram": "텔레그램",
	"email": "이메일", "sms": "문자", "slack": "Slack", "signal": "Signal", "matrix": "Matrix", "whatsapp": "WhatsApp"}

// deliverLabel reduces "platform:channelID" to the platform name, joining several with commas.
func deliverLabel(d string) string {
	var out []string
	for _, part := range strings.Split(d, ",") {
		name, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(part)), ":")
		label := platforms[cmp.Or(name, "local")]
		if label == "" {
			label = strings.ToUpper(name[:1]) + name[1:]
		}
		if !slices.Contains(out, label) {
			out = append(out, label)
		}
	}
	return strings.Join(out, ", ")
}

func (j rawJob) schedule() (text, expr string, interval int) {
	switch j.Schedule.Kind {
	case "interval":
		m := int(math.Ceil(j.Schedule.Minutes))
		return report.Schedule("", m, nil, false), "", m
	case "once":
		at := parseTime(j.Schedule.RunAt)
		return strings.TrimSuffix(report.Schedule("", 0, at, true), ", 한 번"), "", 0
	}
	return report.Schedule(j.Schedule.Expr, 0, nil, false), j.Schedule.Expr, 0
}

func (j rawJob) runAt() *int64 {
	if j.Schedule.Kind != "once" {
		return nil
	}
	return parseTime(j.Schedule.RunAt)
}

func itoa(n int) string { return strconv.Itoa(n) }

func (j rawJob) state() (label string, on bool) {
	paused := (j.Enabled != nil && !*j.Enabled) || j.State == "paused"
	done := j.State == "completed"
	failed := j.Failures > 0 || (j.Status != "" && j.Status != "ok")
	switch {
	case paused:
		return report.StateOff, false
	case done:
		return report.StateDone, false
	case failed:
		return report.StateError, true
	}
	return report.StateOK, true
}

type session struct {
	at     time.Time
	source string
	title  string
	tokens int64
	cost   *float64
}

// openStateDB opens Hermes state.db read-only; in rollback-journal mode it waits briefly while Hermes writes.
func openStateDB(home string) (*sql.DB, error) {
	path := filepath.Join(home, "state.db")
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	return sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(1000)")
}

// readSessions opens state.db read-only; nil if missing or unopenable.
func readSessions(home string, since time.Time) []session {
	db, err := openStateDB(home)
	if err != nil {
		return nil
	}
	defer db.Close()
	rows, err := db.Query(`SELECT started_at, coalesce(source, ''), coalesce(title, ''),
		coalesce(input_tokens,0)+coalesce(output_tokens,0)+coalesce(cache_read_tokens,0)+coalesce(cache_write_tokens,0),
		coalesce(actual_cost_usd, estimated_cost_usd) FROM sessions WHERE started_at >= ?`, float64(since.Unix()))
	if err != nil {
		logOnce("sessions", "could not read state.db costs: %v", err)
		return nil
	}
	defer rows.Close()
	var out []session
	for rows.Next() {
		var s session
		var started float64
		var cost sql.NullFloat64
		if rows.Scan(&started, &s.source, &s.title, &s.tokens, &cost) != nil {
			continue
		}
		s.at = time.Unix(int64(started), 0)
		if cost.Valid {
			v := cost.Float64
			s.cost = &v
		}
		out = append(out, s)
	}
	return out
}

// daily collects dailyDays days one by one, counting back from today; nil if there are no records.
func daily(rows []session, names map[string]bool, today time.Time) []DayCost {
	if rows == nil {
		return nil
	}
	start := today.AddDate(0, 0, -(dailyDays - 1))
	out := make([]DayCost, dailyDays)
	byRoutine := make([]map[string]*RoutineCost, dailyDays)
	for i := range out {
		out[i] = DayCost{Date: start.AddDate(0, 0, i).Format("2006-01-02"), BySource: map[string]float64{}}
	}
	for _, s := range rows {
		d := time.Date(s.at.Year(), s.at.Month(), s.at.Day(), 0, 0, 0, 0, today.Location())
		i := int(d.Sub(start).Hours()/24 + 0.5)
		if i < 0 || i >= dailyDays {
			continue
		}
		out[i].Sessions++
		out[i].Tokens += s.tokens
		if s.cost == nil {
			out[i].Unpriced++
			continue
		}
		out[i].Cost += *s.cost
		out[i].BySource[cmp.Or(sources[s.source], s.source, "기타")] += *s.cost
		if s.source == "cron" {
			if byRoutine[i] == nil {
				byRoutine[i] = map[string]*RoutineCost{}
			}
			addRoutine(byRoutine[i], names, s)
		}
	}
	for i, m := range byRoutine {
		if m != nil {
			out[i].ByRoutine = sortedRoutines(m)
		}
	}
	return out
}

func monthStart(t time.Time, back int) time.Time {
	return time.Date(t.Year(), t.Month()-time.Month(back), 1, 0, 0, 0, 0, t.Location())
}

func aggregate(rows []session, names map[string]bool, key, name, compare string, start, end, pstart, pend time.Time) Period {
	p := Period{Key: key, Name: name, CompareName: compare, Days: make([]float64, int(end.Sub(start).Hours()/24+0.5)), ByRoutine: []RoutineCost{}}
	bySource := map[string]float64{}
	byRoutine := map[string]*RoutineCost{}
	for _, s := range rows {
		if !s.at.Before(pstart) && s.at.Before(pend) && s.cost != nil {
			p.Before += *s.cost
		}
		if s.at.Before(start) || !s.at.Before(end) {
			continue
		}
		p.Sessions++
		p.Tokens += s.tokens
		if s.cost == nil {
			p.Unpriced++
			continue
		}
		p.Cost += *s.cost
		if i := int(s.at.Sub(start).Hours() / 24); i >= 0 && i < len(p.Days) {
			p.Days[i] += *s.cost
		}
		bySource[cmp.Or(sources[s.source], s.source, "기타")] += *s.cost
		addRoutine(byRoutine, names, s)
	}
	for label, c := range bySource {
		p.BySource = append(p.BySource, SourceCost{label, c})
	}
	slices.SortFunc(p.BySource, func(a, b SourceCost) int { return cmp.Compare(b.Cost, a.Cost) })
	p.ByRoutine = sortedRoutines(byRoutine)
	return p
}

// addRoutine adds one conversation that is routine-owned (source cron) and has a value to the total by routine name.
func addRoutine(m map[string]*RoutineCost, names map[string]bool, s session) {
	if s.source != "cron" || s.cost == nil {
		return
	}
	n := routineName(s.title)
	rc := m[n]
	if rc == nil {
		rc = &RoutineCost{Name: n, Gone: n != unnamedRoutine && !names[n]}
		m[n] = rc
	}
	rc.Runs++
	rc.Cost += *s.cost
}

func sortedRoutines(m map[string]*RoutineCost) []RoutineCost {
	out := make([]RoutineCost, 0, len(m))
	for _, rc := range m {
		out = append(out, *rc)
	}
	slices.SortFunc(out, func(a, b RoutineCost) int {
		return cmp.Or(cmp.Compare(b.Cost, a.Cost), cmp.Compare(a.Name, b.Name))
	})
	return out
}

func jobNames(jobs []rawJob) map[string]bool {
	names := map[string]bool{}
	for _, j := range jobs {
		names[cmp.Or(j.Name, j.ID)] = true
	}
	return names
}

// claimRoutineSessions attributes conversations a script routine started internally (title "<routine> · …") to that routine.
func claimRoutineSessions(rows []session, names map[string]bool) {
	for i, s := range rows {
		if name, _, ok := strings.Cut(s.title, " · "); ok && names[strings.TrimSpace(name)] {
			rows[i].source = "cron"
		}
	}
}

// BuildReport builds the report by only reading the Hermes home folder.
func BuildReport(home string, now time.Time) Report {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	tomorrow := today.AddDate(0, 0, 1)
	w0 := today.AddDate(0, 0, -(horizonDays - 1))
	m0, last0, prev0 := monthStart(now, 0), monthStart(now, 1), monthStart(now, 2)
	sameSpan := last0.AddDate(0, 0, now.Day())
	if sameSpan.After(m0) {
		sameSpan = m0
	}
	since := today.AddDate(0, 0, -(dailyDays - 1))
	for _, t := range []time.Time{w0.AddDate(0, 0, -horizonDays), prev0} {
		if t.Before(since) {
			since = t
		}
	}
	rows := readSessions(home, since)
	jobs := readJobs(home)
	names := jobNames(jobs)
	claimRoutineSessions(rows, names)

	r := Report{GeneratedAt: now.UnixMilli(), Routines: []Routine{}, Periods: []Period{}, Daily: daily(rows, names, today)}
	if rows != nil {
		r.Periods = []Period{
			aggregate(rows, names, "week", "최근 7일", "그 전 7일", w0, tomorrow, w0.AddDate(0, 0, -horizonDays), w0),
			aggregate(rows, names, "month", itoa(int(now.Month()))+"월", "지난달 같은 기간", m0, tomorrow, last0, sameSpan),
			aggregate(rows, names, "last", itoa(int(last0.Month()))+"월", "그 전 달", last0, m0, prev0, last0),
		}
	}

	type spend struct {
		n     int
		total float64
	}
	byName := map[string]spend{}
	for _, s := range rows {
		if s.source == "cron" && s.title != "" && s.cost != nil {
			name := routineName(s.title)
			v := byName[name]
			byName[name] = spend{v.n + 1, v.total + *s.cost}
		}
	}

	f := &Forecast{Days: forecastDays, Rows: []ForecastRow{}, Unknown: []string{}}
	end := now.AddDate(0, 0, forecastDays)
	for _, j := range jobs {
		if j.ID == "" {
			continue
		}
		text, expr, interval := j.schedule()
		once := j.Repeat.Times == 1 || j.Schedule.Kind == "once"
		if once {
			text = strings.TrimPrefix(text+", 한 번", ", ")
		}
		label, on := j.state()
		name := cmp.Or(j.Name, j.ID)
		about := firstSentence(j.Prompt)
		if about == "" && j.Script != "" {
			about = j.Script + " 스크립트를 실행한다"
		}
		rt := Routine{ID: j.ID, Name: name, Schedule: text, Cron: expr, Every: interval, At: j.runAt(), State: label, NextRun: parseTime(j.NextRun),
			LastRun: parseTime(j.LastRun), LastStatus: j.Status, Deliver: deliverLabel(j.Deliver), About: about,
			NoAgent: j.NoAgent, Error: j.Error, Once: once, On: on, Completed: j.Repeat.Completed}
		if j.Script != "" && filepath.IsLocal(j.Script) { // nothing outside the scripts folder is read
			rt.Script = filepath.Join(home, "scripts", j.Script)
			rt.Info = scriptInfo(home, j.Script)
			if rt.Info != nil && rt.Info.Summary != "" && j.NoAgent {
				rt.About = rt.Info.Summary
			}
		}
		sp, priced := byName[name]
		if priced {
			avg := sp.total / float64(sp.n)
			rt.Runs, rt.AvgCost = sp.n, &avg
		}
		for _, p := range r.Periods {
			for _, rc := range p.ByRoutine {
				if p.Key == "month" && rc.Name == name {
					c := rc.Cost
					rt.MonthCost, rt.MonthRuns = &c, rc.Runs
				}
			}
		}
		r.Routines = append(r.Routines, rt)

		if !on {
			continue
		}
		runs := 0
		switch {
		case interval > 0:
			runs = forecastDays * 24 * 60 / interval
		case once:
			if t := parseTime(j.NextRun); t != nil && *t >= now.UnixMilli() && *t < end.UnixMilli() {
				runs = 1
			}
		default:
			runs = len(cron.Runs(expr, now, end))
		}
		if runs == 0 {
			continue
		}
		if !priced {
			f.Unknown = append(f.Unknown, name)
			continue
		}
		per := sp.total / float64(sp.n)
		f.Rows = append(f.Rows, ForecastRow{name, runs, per, float64(runs) * per})
	}
	slices.SortFunc(f.Rows, func(a, b ForecastRow) int { return cmp.Compare(b.Total, a.Total) })
	for _, row := range f.Rows {
		f.Routines += row.Total
	}
	if len(r.Periods) > 0 {
		for _, sc := range r.Periods[0].BySource {
			if sc.Label != sources["cron"] {
				f.Other += sc.Cost * forecastDays / horizonDays
			}
		}
	}
	r.Forecast = f
	return r
}
