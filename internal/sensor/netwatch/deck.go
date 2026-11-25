package netwatch

import (
	"bufio"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kyeotapp/kyeot/internal/sensor/text"
)

const (
	flowN          = 60 // about 20 minutes
	recentIncident = 8
	minIncident    = 60 * time.Second
	tsLayout       = "2006-01-02 15:04:05"
)

var label = map[string]string{"OK": "정상", "NO_ROUTE": "경로 없음", "GW_DOWN": "공유기 응답 없음", "WAN_DOWN": "외부 연결 끊김", "DNS_DOWN": "DNS 실패"}

// lamp treats any state other than OK and DNS_DOWN as danger.
func lamp(st, empty string) string {
	switch st {
	case "OK":
		return "ok"
	case "DNS_DOWN":
		return "warn"
	case "":
		return empty
	}
	return "danger"
}

// stateKey is the key of the state label. Unknown state codes are kept verbatim.
func stateKey(st string) any {
	if _, ok := label[st]; ok {
		return text.M("netwatch.state." + st)
	}
	if st == "" {
		return text.M("netwatch.state.none")
	}
	return st
}

func stateLabel(st string) string {
	if l, ok := label[st]; ok {
		return l
	}
	if st == "" {
		return "기록 없음"
	}
	return st
}

type record map[string]string

func parseLine(line string) record {
	parts := strings.Split(strings.TrimRight(line, "\r\n"), "\t")
	if len(parts) < 2 {
		return nil
	}
	r := record{"ts": parts[0]}
	for _, p := range parts[1:] {
		if k, v, ok := strings.Cut(p, "="); ok {
			r[k] = v
		}
	}
	return r
}

func readRecords(path string) []record {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if r := parseLine(sc.Text()); r != nil {
			out = append(out, r)
		}
	}
	return out
}

func num(v string) *float64 {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil
	}
	return &f
}

func asleep(r record) bool {
	if a := r["asleep"]; a == "0" || a == "1" {
		return a == "1"
	}
	g := num(r["gap"])
	return g != nil && *g > 120
}

func durText(d time.Duration) string {
	sec := int(d.Seconds())
	switch {
	case sec < 60:
		return fmt.Sprintf("%d초", sec)
	case sec < 3600:
		return fmt.Sprintf("%d분", sec/60)
	}
	return fmt.Sprintf("%d시간 %d분", sec/3600, sec%3600/60)
}

func durKey(d time.Duration) text.Msg {
	sec := int(d.Seconds())
	switch {
	case sec < 60:
		return text.M("duration.seconds", "n", sec)
	case sec < 3600:
		return text.M("duration.minutes", "n", sec/60)
	}
	return text.M("duration.hours_minutes", "h", sec/3600, "m", sec%3600/60)
}

type outage struct {
	start, end, recovered string
	states                map[string]int
	order                 []string // order of first appearance; ties use this order
}

// outageRuns groups abnormal stretches. A sleeping sample and the battery NO_ROUTE right after it are a boundary, not an outage.
func outageRuns(recs []record) []*outage {
	var runs []*outage
	var cur *outage
	prevDozing := false
	for _, r := range recs {
		dozing := asleep(r) || (prevDozing && r["st"] == "NO_ROUTE" && r["pwr"] == "Battery")
		prevDozing = dozing
		if dozing {
			if cur != nil {
				runs, cur = append(runs, cur), nil
			}
			continue
		}
		st, ok := r["st"]
		if !ok {
			st = "OK"
		}
		if st != "OK" {
			if cur == nil {
				cur = &outage{start: r["ts"], states: map[string]int{}}
			}
			cur.end = r["ts"]
			if cur.states[st] == 0 {
				cur.order = append(cur.order, st)
			}
			cur.states[st]++
		} else if cur != nil {
			cur.recovered = r["ts"]
			runs, cur = append(runs, cur), nil
		}
	}
	if cur != nil {
		runs = append(runs, cur) // outage still ongoing
	}
	return runs
}

type incident struct {
	Time     string `json:"time"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Tone     string `json:"tone"`
}

type incidentKeys struct{ title, subtitle any }

func incidentsOf(runs []*outage) ([]incident, []incidentKeys, int) {
	out := []incident{}
	var keys []incidentKeys
	counted := 0
	for i := len(runs) - 1; i >= 0; i-- {
		r := runs[i]
		t0, _ := time.ParseInLocation(tsLayout, r.start, time.Local)
		t1, _ := time.ParseInLocation(tsLayout, cmpOr(r.recovered, r.end), time.Local)
		d := t1.Sub(t0)
		states := slices.Clone(r.order)
		slices.SortStableFunc(states, func(a, b string) int { return r.states[b] - r.states[a] })
		main := states[0]
		ok := d >= minIncident
		if ok {
			counted++
		}
		parts := make([]string, len(states))
		partKeys := make([]any, len(states))
		for j, s := range states {
			parts[j] = fmt.Sprintf("%s %d", stateLabel(s), r.states[s])
			partKeys[j] = text.M("netwatch.state_count", "state", stateKey(s), "n", r.states[s])
		}
		from, to := t0.Format("15:04"), t1.Format("15:04")
		span := "netwatch.incident_span"
		it := incident{
			Time:     t0.Format("01/02 15:04"),
			Title:    "순간 끊김",
			Subtitle: fmt.Sprintf("%s–%s · %s · %s", from, to, durText(d), strings.Join(parts, ", ")),
			Tone:     "info",
		}
		k := incidentKeys{title: text.M("netwatch.blip")}
		if r.recovered == "" {
			it.Subtitle += " · 진행 중"
			span = "netwatch.incident_span_ongoing"
		}
		k.subtitle = text.M(span, "from", from, "to", to, "dur", durKey(d), "states", partKeys)
		if ok {
			it.Title, it.Tone = stateLabel(main), "danger"
			k.title = stateKey(main)
			if main == "DNS_DOWN" {
				it.Tone = "warn"
			}
		}
		out = append(out, it)
		keys = append(keys, k)
	}
	if len(out) > recentIncident {
		out, keys = out[:recentIncident], keys[:recentIncident]
	}
	return out, keys, counted
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

type statusItem struct {
	Label string `json:"label"`
	State string `json:"state"`
	Note  string `json:"note"`
}

type series struct {
	Name   string     `json:"name"`
	Values []*float64 `json:"values"`
	Color  string     `json:"color"`
}

// Bundle is the shape of the netwatch bundle. The template blueprint reads these paths.
type Bundle struct {
	Now struct {
		State   string   `json:"state"`
		Color   string   `json:"color"`
		Checked string   `json:"checked"`
		GwMs    *float64 `json:"gw_ms"`
		WanMs   *float64 `json:"wan_ms"`
		TCP     *int     `json:"tcp"`
	} `json:"now"`
	Status []statusItem `json:"status"`
	Flow   struct {
		Labels []string `json:"labels"`
		Series []series `json:"series"`
	} `json:"flow"`
	Incidents    []incident `json:"incidents"`
	SamplesToday int        `json:"samples_today"`
	Text         text.Map   `json:"$text"`
}

// build makes the bundle from today's records. If failed is set, the "지금" (now) field gives the reason measurement failed.
func build(recs []record, failed string) Bundle {
	b := Bundle{Text: text.Map{}}
	t := b.Text
	last := record{}
	if len(recs) > 0 {
		last = recs[len(recs)-1]
	}
	st := last["st"]
	checked := cmpOr(last["ts"], "—")
	clock := ""
	if len(checked) > 11 {
		clock = checked[11:]
	}
	b.Now.State, b.Now.Color, b.Now.Checked = stateLabel(st), lamp(st, "neutral"), checked
	b.Now.GwMs, b.Now.WanMs = num(last["gw_ms"]), num(last["wan_ms"])
	if t := num(last["tcp"]); t != nil {
		n := int(*t)
		b.Now.TCP = &n
	}
	t.Put("now.state", stateKey(st))
	incidents, incidentText, today := incidentsOf(outageRuns(recs))
	dnsOK := last["dns"] != "" && last["dns"] != "-"
	now := statusItem{"지금", lamp(st, "off"), stateLabel(st) + " · " + clock}
	t.Put("status.0.note", text.M("netwatch.state_at", "state", stateKey(st), "clock", clock))
	if failed != "" {
		b.Now.State, b.Now.Color = "측정 못 함", "danger"
		now = statusItem{"지금", "danger", "측정 못 함: " + failed}
		t.Put("now.state", text.M("sensor.measure_failed"))
		t.Put("status.0.note", text.M("sensor.measure_failed_reason", "reason", failed))
	}
	t.Put("status.0.label", text.M("netwatch.label.now"))
	t.Put("status.1.label", text.M("netwatch.label.dns"))
	t.Put("status.1.note", text.M(map[bool]string{true: "netwatch.dns.ok", false: "netwatch.dns.none"}[dnsOK]))
	t.Put("status.2.label", text.M("netwatch.label.incidents_today"))
	t.Put("status.2.note", text.M("count.cases", "n", today))
	b.Status = []statusItem{
		now,
		{"DNS", map[bool]string{true: "ok", false: "warn"}[dnsOK], map[bool]string{true: "응답함", false: "응답 없음"}[dnsOK]},
		{"오늘 장애 기록", map[bool]string{true: "ok", false: "warn"}[today == 0], fmt.Sprintf("%d건", today)},
	}
	flow := recs[max(0, len(recs)-flowN):]
	b.Flow.Labels = make([]string, len(flow))
	gw, wan := make([]*float64, len(flow)), make([]*float64, len(flow))
	for i, r := range flow {
		if len(r["ts"]) >= 19 {
			b.Flow.Labels[i] = r["ts"][11:19]
		}
		gw[i], wan[i] = num(r["gw_ms"]), num(r["wan_ms"])
	}
	b.Flow.Series = []series{{"공유기", gw, "blue"}, {"외부", wan, "purple"}}
	t.Put("flow.series.0.name", text.M("netwatch.series.router"))
	t.Put("flow.series.1.name", text.M("netwatch.series.internet"))
	b.Incidents = incidents
	for i, k := range incidentText {
		t.Put(text.Path("incidents", i, "title"), k.title)
		t.Put(text.Path("incidents", i, "subtitle"), k.subtitle)
	}
	b.SamplesToday = len(recs)
	return b
}
