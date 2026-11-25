// Package uicatalog validates screen specs uploaded by agents against the app catalog. The original is catalog.json in this folder; the app has a copy.
package uicatalog

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

//go:embed catalog.json
var Raw []byte

type prop struct {
	Kind     string   `json:"kind"`
	Required bool     `json:"required"`
	Values   []string `json:"values"`
	Fields   []string `json:"fields"`
}

type catalog struct {
	Limits struct {
		MaxBlocks      int `json:"maxBlocks"`
		MaxSpecBytes   int `json:"maxSpecBytes"`
		MaxVisibleTabs int `json:"maxVisibleTabs"`
		MaxScreens     int `json:"maxScreens"`
		MaxDataBytes   int `json:"maxDataBytes"`
		MaxDataBundles int `json:"maxDataBundles"`
	} `json:"limits"`
	Components map[string]struct {
		Props map[string]prop `json:"props"`
	} `json:"components"`
	Parts struct {
		MaxParts  int `json:"maxParts"`
		MaxParams int `json:"maxParams"`
		MaxBlocks int `json:"maxBlocks"`
	} `json:"parts"`
	DeckData   map[string]DeckValue `json:"deckData"`
	Colors     []string             `json:"colors"`
	Searchable map[string]string    `json:"searchable"`
	Icons      []string             `json:"icons"` // SF Symbols the app can draw on every platform
	Links      struct {
		Targets []string `json:"targets"`
		Periods []string `json:"periods"`
	} `json:"links"`
}

// DeckValue is one deck value a spec can point to with {"$data": "deck.…"}. The app computes it.
type DeckValue struct {
	Kind        string   `json:"kind"`
	Fields      []string `json:"fields"`
	Description string   `json:"description"`
}

var cat = func() catalog {
	var c catalog
	if err := json.Unmarshal(Raw, &c); err != nil {
		panic("uicatalog: " + err.Error())
	}
	return c
}()

func MaxVisibleTabs() int { return cat.Limits.MaxVisibleTabs }
func MaxScreens() int     { return cat.Limits.MaxScreens }
func MaxDataBytes() int   { return cat.Limits.MaxDataBytes }
func MaxDataBundles() int { return cat.Limits.MaxDataBundles }

func DeckData() map[string]DeckValue { return cat.DeckData }

type Screen struct {
	Version int               `json:"version"`
	ID      string            `json:"id"`
	Title   string            `json:"title"`
	Icon    string            `json:"icon"`
	Blocks  []json.RawMessage `json:"blocks"`
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
var hexColor = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
var httpsURL = regexp.MustCompile(`^(?i:https)://[^\s]+$`)

// Validate returns the parsed spec if it follows the catalog rules, else a reason the agent can fix.
func Validate(raw []byte) (Screen, error) {
	var s Screen
	if len(raw) > cat.Limits.MaxSpecBytes {
		return s, fmt.Errorf("설계도가 %d바이트를 넘는다", cat.Limits.MaxSpecBytes)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil || top == nil {
		return s, fmt.Errorf("JSON 객체가 아니다")
	}
	// encoding/json ignores key case, so keys are matched exactly by hand
	fields := map[string]any{"version": &s.Version, "id": &s.ID, "title": &s.Title, "icon": &s.Icon, "blocks": &s.Blocks}
	for k := range top {
		if _, ok := fields[k]; !ok {
			return s, fmt.Errorf("설계도에 없는 속성 %q", k)
		}
	}
	for k, dst := range fields {
		v, ok := top[k]
		if k == "icon" && (!ok || isNull(v)) {
			continue // if empty, FitIcon fills it
		}
		if !ok || isNull(v) {
			return s, fmt.Errorf("%s 가 필요하다", k)
		}
		if err := json.Unmarshal(v, dst); err != nil {
			return s, fmt.Errorf("%s 의 형식이 틀렸다", k)
		}
	}
	switch {
	case s.Version != 1:
		return s, fmt.Errorf("version 은 1 이어야 한다")
	case !idPattern.MatchString(s.ID):
		return s, fmt.Errorf("id 는 소문자, 숫자, - 로 32자 이하")
	case !between(s.Title, 1, 20):
		return s, fmt.Errorf("title 은 1~20자")
	case !between(s.Icon, 0, 60):
		return s, fmt.Errorf("icon 은 SF Symbols 이름 60자 이하")
	case len(s.Blocks) == 0 || len(s.Blocks) > cat.Limits.MaxBlocks:
		return s, fmt.Errorf("blocks 는 1~%d개", cat.Limits.MaxBlocks)
	}
	for i, b := range s.Blocks {
		if err := validateBlock(b); err != nil {
			return s, fmt.Errorf("blocks[%d]: %v", i, err)
		}
	}
	if err := checkSearch(s.Blocks); err != nil {
		return s, err
	}
	if err := checkRefresh(s.Blocks); err != nil {
		return s, err
	}
	return s, nil
}

var routineIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidBundleName is the form that screen ids and data bundle names follow.
func ValidBundleName(s string) bool { return idPattern.MatchString(s) }

// checkRefresh checks that refresh has one of say, routine, data, bundles, and that routine and data/bundles have the form of a routine id and bundle names.
func checkRefresh(blocks []json.RawMessage) error {
	for i, b := range blocks {
		var x struct {
			Type    string `json:"type"`
			Say     any    `json:"say"`
			Routine any    `json:"routine"`
			Data    any    `json:"data"`
			Bundles any    `json:"bundles"`
		}
		json.Unmarshal(b, &x)
		if x.Type != "refresh" {
			continue
		}
		say, _ := x.Say.(string)
		routine, rOK := x.Routine.(string)
		data, dOK := x.Data.(string)
		switch {
		case strings.TrimSpace(say) == "" && x.Routine == nil && x.Data == nil && x.Bundles == nil:
			return fmt.Errorf("blocks[%d]: refresh 는 say, routine, data, bundles 중 하나가 필요하다", i)
		case x.Routine != nil && (!rOK || !routineIDPattern.MatchString(routine)):
			return fmt.Errorf("blocks[%d]: refresh.routine 은 루틴 id(영문, 숫자, _, - 64자 이하)여야 한다", i)
		case x.Data != nil && (!dOK || !ValidBundleName(data)):
			return fmt.Errorf("blocks[%d]: refresh.data 는 묶음 이름(소문자, 숫자, - 32자 이하)이어야 한다", i)
		case x.Bundles != nil && !validBundleList(x.Bundles):
			return fmt.Errorf("blocks[%d]: refresh.bundles 는 묶음 이름 1~10개의 배열이어야 한다", i)
		}
	}
	return nil
}

func validBundleList(v any) bool {
	list, ok := v.([]any)
	if !ok || len(list) == 0 || len(list) > 10 {
		return false
	}
	for _, x := range list {
		s, ok := x.(string)
		if !ok || !ValidBundleName(s) {
			return false
		}
	}
	return true
}

// checkSearch checks that search's targets and fields are text arrays and that targets are titles of list parts on this screen.
func checkSearch(blocks []json.RawMessage) error {
	var titles []string
	for _, b := range blocks {
		var x struct {
			Type  string `json:"type"`
			Title string `json:"title"`
		}
		json.Unmarshal(b, &x)
		if _, ok := cat.Searchable[x.Type]; ok && x.Title != "" {
			titles = append(titles, x.Title)
		}
	}
	for i, b := range blocks {
		var x struct {
			Type    string          `json:"type"`
			Targets json.RawMessage `json:"targets"`
			Fields  json.RawMessage `json:"fields"`
		}
		json.Unmarshal(b, &x)
		if x.Type != "search" {
			continue
		}
		var targets, fields []string
		if x.Targets != nil && json.Unmarshal(x.Targets, &targets) != nil {
			return fmt.Errorf("blocks[%d]: search.targets 는 목록 부품 title 의 글 배열이어야 한다", i)
		}
		if x.Fields != nil && json.Unmarshal(x.Fields, &fields) != nil {
			return fmt.Errorf("blocks[%d]: search.fields 는 칸 이름 글 배열이어야 한다", i)
		}
		for _, t := range targets {
			if !slices.Contains(titles, t) {
				return fmt.Errorf("blocks[%d]: search.targets 의 %q 와 title 이 같은 목록 부품(%v)이 이 화면에 없다", i, t, searchableTypes())
			}
		}
	}
	return nil
}

func searchableTypes() []string {
	out := make([]string, 0, len(cat.Searchable))
	for k := range cat.Searchable {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func validateBlock(raw json.RawMessage) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return fmt.Errorf("객체가 아니다")
	}
	var typ string
	json.Unmarshal(fields["type"], &typ)
	if typ == "use" {
		return fmt.Errorf("use 는 조립 부품을 펼칠 때만 쓴다")
	}
	comp, ok := cat.Components[typ]
	if !ok {
		return fmt.Errorf("모르는 부품 %q", typ)
	}
	for name, v := range fields {
		if name == "type" {
			continue
		}
		p, ok := comp.Props[name]
		if !ok {
			return fmt.Errorf("%s 에 없는 속성 %q", typ, name)
		}
		if err := checkKind(name, p, v); err != nil {
			return fmt.Errorf("%s.%s: %v", typ, name, err)
		}
	}
	for name, p := range comp.Props {
		if _, ok := fields[name]; p.Required && !ok {
			return fmt.Errorf("%s 에 %s 가 필요하다", typ, name)
		}
	}
	return nil
}

// numberRange gives the number range and integer-ness per name, matching how the app uses them
var numberRange = map[string]struct {
	lo, hi  float64
	integer bool
}{
	"decimals":  {0, 10, true},
	"limit":     {1, 100, true},
	"highlight": {0, 100, true},
	"ratio":     {0.25, 4, false},
	"days":      {1, 371, true},
}

// other numbers up to the size JS handles exactly
const maxMagnitude = 1e15

func checkKind(name string, p prop, v json.RawMessage) error {
	if isNull(v) {
		return fmt.Errorf("null 은 받지 않는다. 빼거나 값을 넣는다")
	}
	if isDataRef(v) && (p.Kind == "number" || p.Kind == "items") {
		return nil
	}
	switch p.Kind {
	case "text":
		var s string
		if json.Unmarshal(v, &s) != nil || utf8.RuneCountInString(s) > 2000 {
			return fmt.Errorf("2000자 이하 글이어야 한다")
		}
		if name == "url" && !httpsURL.MatchString(s) {
			return fmt.Errorf("https:// 로 시작하는 주소여야 한다")
		}
	case "number":
		var n float64
		if json.Unmarshal(v, &n) != nil {
			return fmt.Errorf("숫자나 {\"$data\": 이름} 이어야 한다")
		}
		return checkNumber(name, n)
	case "enum":
		var s string
		if json.Unmarshal(v, &s) != nil || !slices.Contains(p.Values, s) {
			return fmt.Errorf("%v 중 하나여야 한다", p.Values)
		}
	case "bool":
		var b bool
		if json.Unmarshal(v, &b) != nil {
			return fmt.Errorf("true 나 false 여야 한다")
		}
	case "color":
		if !isColor(v) {
			return fmt.Errorf("%v 중 하나나 #RRGGBB 여야 한다", cat.Colors)
		}
	case "link":
		return checkLink(v)
	case "items":
		var a []json.RawMessage
		if json.Unmarshal(v, &a) != nil || len(a) > 500 {
			return fmt.Errorf("500개 이하 배열이나 {\"$data\": 이름} 이어야 한다")
		}
		for i, x := range a {
			if err := checkItem(p.Fields, x); err != nil {
				return fmt.Errorf("[%d]: %v", i, err)
			}
		}
	default:
		return fmt.Errorf("카탈로그의 kind %q 를 모른다", p.Kind)
	}
	return nil
}

func checkNumber(name string, n float64) error {
	if r, ok := numberRange[name]; ok {
		if n < r.lo || n > r.hi || (r.integer && n != math.Trunc(n)) {
			kind := "수"
			if r.integer {
				kind = "정수"
			}
			return fmt.Errorf("%g~%g 사이 %s여야 한다", r.lo, r.hi, kind)
		}
		return nil
	}
	if math.Abs(n) > maxMagnitude {
		return fmt.Errorf("절댓값 %g 이하여야 한다", maxMagnitude)
	}
	return nil
}

// checkItem checks one list element: an object with only the named fields if fields is set, otherwise text, a number, or an array of them (a table row).
func checkItem(fields []string, x json.RawMessage) error {
	if len(fields) == 0 {
		var row []json.RawMessage
		if json.Unmarshal(x, &row) == nil && row != nil {
			for _, c := range row {
				if !scalar(c) {
					return fmt.Errorf("행의 칸은 글이나 숫자여야 한다")
				}
			}
			return nil
		}
		if !scalar(x) {
			return fmt.Errorf("글이나 숫자여야 한다")
		}
		return nil
	}
	var o map[string]json.RawMessage
	if json.Unmarshal(x, &o) != nil || o == nil {
		return fmt.Errorf("%v 를 가진 객체여야 한다", fields)
	}
	for k, v := range o {
		if !slices.Contains(fields, k) {
			return fmt.Errorf("없는 속성 %q. %v 만 쓴다", k, fields)
		}
		switch {
		case k == "link" && !isNull(v):
			if err := checkLink(v); err != nil {
				return err
			}
		case k == "color" && !isNull(v) && !isColor(v):
			return fmt.Errorf("color 는 %v 중 하나나 #RRGGBB", cat.Colors)
		case k == "entries" && !isNull(v):
			if err := checkEntries(v); err != nil {
				return err
			}
		case (k == "say" || k == "undo") && !isText(v):
			return fmt.Errorf("%s 는 2000자 이하 글이어야 한다", k)
		case k == "decimals" && !isNull(v):
			var n float64
			if json.Unmarshal(v, &n) != nil {
				return fmt.Errorf("decimals 는 숫자여야 한다")
			}
			if err := checkNumber("decimals", n); err != nil {
				return fmt.Errorf("decimals 는 %v", err)
			}
		}
	}
	return nil
}

const maxEntries = 50

// checkEntries checks a calendar cell's log: up to 50 of {title, time?, note?}, all short text.
func checkEntries(v json.RawMessage) error {
	var list []map[string]json.RawMessage
	if json.Unmarshal(v, &list) != nil || len(list) > maxEntries {
		return fmt.Errorf("entries 는 %d개 이하 [{title, time, note}] 배열이어야 한다", maxEntries)
	}
	for i, e := range list {
		if e == nil {
			return fmt.Errorf("entries[%d] 는 {title, time, note} 객체여야 한다", i)
		}
		if _, ok := e["title"]; !ok {
			return fmt.Errorf("entries[%d].title 이 없다", i)
		}
		for k, x := range e {
			if k != "title" && k != "time" && k != "note" {
				return fmt.Errorf("entries[%d]: 없는 속성 %q. title, time, note 만 쓴다", i, k)
			}
			var str string
			if json.Unmarshal(x, &str) != nil || isNull(x) || utf8.RuneCountInString(str) > 200 || (k == "title" && str == "") {
				return fmt.Errorf("entries[%d].%s 는 200자 이하 글이어야 한다", i, k)
			}
		}
	}
	return nil
}

func isText(v json.RawMessage) bool {
	var s string
	return json.Unmarshal(v, &s) == nil && !isNull(v) && utf8.RuneCountInString(s) <= 2000
}

// scalar is text, number, boolean, or null
func scalar(v json.RawMessage) bool {
	b := bytes.TrimSpace(v)
	return len(b) > 0 && b[0] != '{' && b[0] != '['
}

func isColor(v json.RawMessage) bool {
	var s string
	return json.Unmarshal(v, &s) == nil && !isNull(v) && (slices.Contains(cat.Colors, s) || hexColor.MatchString(s))
}

func isNull(v json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(v), []byte("null")) }

// checkLink accepts only {"to": base screen name or screen id, "period": cost period}.
func checkLink(v json.RawMessage) error {
	var l map[string]json.RawMessage
	if json.Unmarshal(v, &l) != nil || l == nil {
		return fmt.Errorf(`{"to": 화면} 객체여야 한다`)
	}
	var to, period string
	for k, raw := range l {
		var s string
		if isNull(raw) || json.Unmarshal(raw, &s) != nil {
			return fmt.Errorf("link.%s 는 글이어야 한다", k)
		}
		switch k {
		case "to":
			to = s
		case "period":
			period = s
		default:
			return fmt.Errorf("link 에 없는 속성 %q", k)
		}
	}
	if !slices.Contains(cat.Links.Targets, to) && !idPattern.MatchString(to) {
		return fmt.Errorf("link.to 는 %v 이나 화면 id 여야 한다", cat.Links.Targets)
	}
	if _, ok := l["period"]; ok && (to != "costs" || !slices.Contains(cat.Links.Periods, period)) {
		return fmt.Errorf("link.period 는 to 가 costs 일 때 %v 중 하나", cat.Links.Periods)
	}
	return nil
}

func isDataRef(v json.RawMessage) bool {
	var ref map[string]string
	if json.Unmarshal(v, &ref) != nil || len(ref) != 1 {
		return false
	}
	name, ok := ref["$data"]
	return ok && between(name, 1, 100)
}

func between(s string, lo, hi int) bool {
	n := utf8.RuneCountInString(s)
	return n >= lo && n <= hi
}

type Starter struct {
	ID  string
	Raw []byte
}

// Starters are the samples first put into a new agent. The order is the tab order.
// FirstTabs are the tabs a new agent starts with. Empty: it starts with home only and picks tabs via "customize my tabs".
func FirstTabs() []string { return []string{} }

func Starters() []Starter {
	mk := func(id, title, icon string) Starter {
		raw, _ := json.Marshal(map[string]any{"version": 1, "id": id, "title": title, "icon": icon, "blocks": []map[string]string{{"type": id}}})
		return Starter{id, raw}
	}
	return []Starter{mk("rooms", "방", "bubble.left"), mk("routines", "루틴", "repeat"), mk("costs", "비용", "chart.bar")}
}
