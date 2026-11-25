package uicatalog

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestValidateAcceptsGoodScreen(t *testing.T) {
	s, err := Validate([]byte(`{"version":1,"id":"revenue","title":"Revenue","icon":"chart.bar",
		"blocks":[{"type":"stat","label":"This month","value":{"$data":"m"},"better":"up"},{"type":"divider"}]}`))
	if err != nil || s.ID != "revenue" || len(s.Blocks) != 2 {
		t.Fatalf("got %+v %v", s, err)
	}
}

func TestValidateRejects(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown part":           `{"version":1,"id":"a","title":"t","icon":"i","blocks":[{"type":"map"}]}`,
		"unknown property":       `{"version":1,"id":"a","title":"t","icon":"i","blocks":[{"type":"divider","colour":"red"}]}`,
		"required missing":       `{"version":1,"id":"a","title":"t","icon":"i","blocks":[{"type":"stat","label":"x"}]}`,
		"not a number":           `{"version":1,"id":"a","title":"t","icon":"i","blocks":[{"type":"stat","label":"x","value":"1"}]}`,
		"outside enum":           `{"version":1,"id":"a","title":"t","icon":"i","blocks":[{"type":"stat","label":"x","value":1,"better":"left"}]}`,
		"id format":              `{"version":1,"id":"../x","title":"t","icon":"i","blocks":[{"type":"divider"}]}`,
		"version number":         `{"version":2,"id":"a","title":"t","icon":"i","blocks":[{"type":"divider"}]}`,
		"empty blocks":           `{"version":1,"id":"a","title":"t","icon":"i","blocks":[]}`,
		"title too long":         `{"version":1,"id":"a","title":"` + strings.Repeat("a", 21) + `","icon":"i","blocks":[{"type":"divider"}]}`,
		"not JSON":               `{`,
		"checklist say not text": `{"version":1,"id":"a","title":"t","icon":"i","blocks":[{"type":"checklist","items":[{"title":"a","say":1}]}]}`,
	} {
		if _, err := Validate([]byte(raw)); err == nil {
			t.Errorf("%s: must not pass", name)
		}
	}
}

func TestValidateRejectsHugeSpec(t *testing.T) {
	big := `{"version":1,"id":"a","title":"t","icon":"i","blocks":[{"type":"text","text":"` + strings.Repeat("x", 70000) + `"}]}`
	if _, err := Validate([]byte(big)); err == nil {
		t.Fatal("a spec over 64KB is rejected")
	}
}

func TestStartersAreValid(t *testing.T) {
	for _, s := range Starters() {
		if _, err := Validate(s.Raw); err != nil {
			t.Fatalf("sample %s: %v", s.ID, err)
		}
	}
}

func TestValidateNamesUnknownProp(t *testing.T) {
	_, err := Validate([]byte(`{"version":1,"id":"a","title":"t","icon":"i","blocks":[{"type":"divider","colour":"red"}]}`))
	if err == nil || !strings.Contains(err.Error(), `없는 속성 "colour"`) {
		t.Fatalf("a mistyped property name must be reported: %v", err)
	}
}

func TestDataLimitsAndDeckPaths(t *testing.T) {
	if MaxDataBytes() != 262144 || MaxDataBundles() != 20 {
		t.Fatalf("bundle limits %d %d", MaxDataBytes(), MaxDataBundles())
	}
	want := []string{"deck.costs.today", "deck.costs.week", "deck.costs.month", "deck.costs.daily",
		"deck.approvals.count", "deck.tasks.today", "deck.routines.upcoming"}
	if _, ok := DeckData()["deck.costs.remaining"]; ok {
		t.Error("deck.costs.remaining was removed")
	}
	for _, p := range want {
		d, ok := DeckData()[p]
		if !ok || (d.Kind != "number" && d.Kind != "items") || d.Description == "" {
			t.Errorf("deckData %s: %+v", p, d)
		}
	}
	if len(DeckData()) != len(want) {
		t.Errorf("deckData has %d entries: %d", len(want), len(DeckData()))
	}
}

func TestTitleAcceptsEmoji(t *testing.T) {
	for _, title := range []string{"🏠 Rooms", "💰 Revenue👍", "👨‍👩‍👧 Family"} {
		raw := []byte(`{"version":1,"id":"rooms","title":"` + title + `","icon":"number","blocks":[{"type":"rooms"}]}`)
		if _, err := Validate(raw); err != nil {
			t.Errorf("%q: %v", title, err)
		}
	}
}

func TestValidateMoreBlocks(t *testing.T) {
	blocks := []string{
		`{"type":"stackbars","labels":["a"],"series":[{"name":"x","values":[1]}]}`,
		`{"type":"groupbars","labels":["a"],"series":{"$data":"r.s"}}`,
		`{"type":"area","unit":"KRW","labels":["a"],"series":[{"name":"x","values":[1]}]}`,
		`{"type":"spark","label":"x","value":1,"values":[1,2],"better":"down"}`,
		`{"type":"donut","items":[{"label":"a","value":1}]}`,
		`{"type":"gauge","label":"x","value":{"$data":"r.v"},"max":100}`,
		`{"type":"heatmap","columns":["0"],"rows":[{"label":"Mon","values":[1]}]}`,
		`{"type":"calendar","items":[{"date":"2026-11-12","value":3}]}`,
		`{"type":"scatter","items":[{"x":1,"y":2}]}`,
		`{"type":"funnel","items":[{"label":"a","value":10}]}`,
		`{"type":"treemap","items":[{"label":"a","value":10}]}`,
		`{"type":"rank","limit":3,"items":[{"label":"a","value":10}]}`,
		`{"type":"badges","items":[{"label":"a","tone":"ok"}]}`,
		`{"type":"checklist","items":[{"title":"a","done":true}]}`,
		`{"type":"timeline","items":[{"time":"09:00","title":"a"}]}`,
		`{"type":"keyvalue","items":[{"label":"a","value":"b"}]}`,
		`{"type":"callout","tone":"error","text":"a"}`,
		`{"type":"image","url":"https://example.com/a.png","ratio":1.5}`,
		`{"type":"link","title":"a","url":"https://example.com"}`,
		`{"type":"countdown","label":"a","to":"2026-12-01T00:00:00+09:00"}`,
		`{"type":"compare","aLabel":"a","aValue":1,"bLabel":"b","bValue":2}`,
		`{"type":"status","items":[{"label":"a","state":"warn"}]}`,
		`{"type":"rank","highlight":1,"color":"coral","showBar":false,"sort":"input","items":[{"label":"a","value":1,"tags":["x"],"accent":true}]}`,
		`{"type":"bars","color":"#C2410C","decimals":1,"showTicks":false,"items":[{"label":"a","value":1,"color":"danger"}]}`,
		`{"type":"divider","color":"accent"}`,
		`{"type":"connections","title":"Connected apps","showAccount":true,"items":[{"name":"Gmail","state":"off","icon":"mail","action":{"label":"Connect","say":"Connect Gmail"}}]}`,
	}
	raw := `{"version":1,"id":"more","title":"t","icon":"i","blocks":[` + strings.Join(blocks, ",") + `]}`
	if _, err := Validate([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{"type":"callout","tone":"red","text":"a"}`,
		`{"type":"gauge","label":"x","value":1}`,
		`{"type":"image","url":1}`,
		`{"type":"bars","color":"red","items":[]}`,
		`{"type":"bars","color":"#12345","items":[]}`,
		`{"type":"rank","showBar":"no","items":[]}`,
		`{"type":"rank","sort":"desc","items":[]}`,
		`{"type":"connections","showAccount":"yes","items":[]}`,
	} {
		if _, err := Validate([]byte(`{"version":1,"id":"a","title":"t","icon":"i","blocks":[` + bad + `]}`)); err == nil {
			t.Errorf("%s must be rejected", bad)
		}
	}
}

func TestValidateLinks(t *testing.T) {
	scr := func(b string) []byte {
		return []byte(`{"version":1,"id":"a","title":"t","icon":"i","blocks":[` + b + `]}`)
	}
	for _, ok := range []string{
		`{"type":"bars","title":"x","items":[],"link":{"to":"costs","period":"week"}}`,
		`{"type":"stat","label":"x","value":1,"link":{"to":"approvals"}}`,
		`{"type":"list","items":[],"link":{"to":"my-detail"}}`,
	} {
		if _, err := Validate(scr(ok)); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		`{"type":"bars","items":[],"link":"costs"}`,
		`{"type":"bars","items":[],"link":{"to":"../x"}}`,
		`{"type":"bars","items":[],"link":{"to":"routines","period":"week"}}`,
		`{"type":"bars","items":[],"link":{"to":"costs","period":"year"}}`,
		`{"type":"bars","items":[],"link":{"to":"costs","extra":1}}`,
		`{"type":"divider","link":{"to":"costs"}}`,
	} {
		if _, err := Validate(scr(bad)); err == nil {
			t.Errorf("%s must be rejected", bad)
		}
	}
}

// testdata/app_samples.json is extracted from the sample screens in the app's templates.ts and the part examples in blockGuide.ts
func TestAppSamplesPass(t *testing.T) {
	raw, err := os.ReadFile("testdata/app_samples.json")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Screens []json.RawMessage
		Blocks  []json.RawMessage
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Screens) < 10 || len(s.Blocks) < 60 {
		t.Fatalf("not enough samples: screens %d parts %d", len(s.Screens), len(s.Blocks))
	}
	for _, sc := range s.Screens {
		if _, err := Validate(sc); err != nil {
			t.Errorf("sample screen %s: %v", sc[:min(60, len(sc))], err)
		}
	}
	for _, b := range s.Blocks {
		if err := validateBlock(b); err != nil {
			t.Errorf("part example %s: %v", b, err)
		}
	}
}

func TestValidateStrictKeysAndNull(t *testing.T) {
	scr := func(b string) []byte {
		return []byte(`{"version":1,"id":"a","title":"t","icon":"i","blocks":[` + b + `]}`)
	}
	for name, raw := range map[string]string{
		"uppercase key":       `{"Version":1,"id":"a","title":"t","icon":"i","blocks":[{"type":"divider"}]}`,
		"uppercase ID":        `{"version":1,"ID":"a","title":"t","icon":"i","blocks":[{"type":"divider"}]}`,
		"unknown top-level":   `{"version":1,"id":"a","title":"t","icon":"i","extra":1,"blocks":[{"type":"divider"}]}`,
		"title null":          `{"version":1,"id":"a","title":null,"icon":"i","blocks":[{"type":"divider"}]}`,
		"blocks null":         `{"version":1,"id":"a","title":"t","icon":"i","blocks":null}`,
		"top-level null":      `null`,
		"text null":           string(scr(`{"type":"text","text":null}`)),
		"optional value null": string(scr(`{"type":"text","text":"a","align":null}`)),
		"items null":          string(scr(`{"type":"bars","items":null}`)),
		"block null":          string(scr(`null`)),
		"link.to null":        string(scr(`{"type":"bars","items":[],"link":{"to":null}}`)),
		"uppercase part key":  string(scr(`{"type":"text","Text":"a"}`)),
	} {
		if _, err := Validate([]byte(raw)); err == nil {
			t.Errorf("%s: must not pass", name)
		}
	}
}

func TestValidateRangesUrlsItems(t *testing.T) {
	scr := func(b string) []byte {
		return []byte(`{"version":1,"id":"a","title":"t","icon":"i","blocks":[` + b + `]}`)
	}
	for _, ok := range []string{
		`{"type":"stat","label":"x","value":-1234567.5,"decimals":0}`,
		`{"type":"stat","label":"x","value":1,"decimals":10}`,
		`{"type":"rank","limit":100,"highlight":0,"items":[{"label":"a","value":1,"tags":["x"],"link":{"to":"costs"}}]}`,
		`{"type":"image","url":"HTTPS://example.com/a.png","ratio":0.25}`,
		`{"type":"table","columns":["a","b"],"rows":[["x",1],["y",null]]}`,
		`{"type":"stats","items":[{"label":"a","value":{"$data":"r.v"},"color":"ok"}]}`,
		`{"type":"line","labels":["Jan",2],"series":[{"name":"a","values":[1,null]}]}`,
	} {
		if _, err := Validate(scr(ok)); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		`{"type":"stat","label":"x","value":1,"decimals":-5}`,
		`{"type":"stat","label":"x","value":1,"decimals":11}`,
		`{"type":"stat","label":"x","value":1,"decimals":1.5}`,
		`{"type":"stat","label":"x","value":1e300}`,
		`{"type":"rank","limit":0,"items":[]}`,
		`{"type":"image","url":"https://e.com/a.png","ratio":10}`,
		`{"type":"image","url":"http://example.com/a.png"}`,
		`{"type":"image","url":"javascript:alert(1)"}`,
		`{"type":"link","title":"a","url":"file:///etc/passwd"}`,
		`{"type":"link","title":"a","url":"https://a b"}`,
		`{"type":"bars","items":[1]}`,
		`{"type":"bars","items":[{"label":"a","value":1,"Value":2}]}`,
		`{"type":"bars","items":[{"label":"a","value":1,"color":"red"}]}`,
		`{"type":"hbars","items":[{"label":"a","value":1,"link":{"to":"../x"}}]}`,
		`{"type":"table","columns":[{"a":1}],"rows":[]}`,
		`{"type":"table","columns":["a"],"rows":[[{"a":1}]]}`,
	} {
		if _, err := Validate(scr(bad)); err == nil {
			t.Errorf("%s must be rejected", bad)
		}
	}
}

func TestValidateSearch(t *testing.T) {
	screen := func(blocks ...string) []byte {
		return []byte(`{"version":1,"id":"s","title":"t","icon":"i","blocks":[` + strings.Join(blocks, ",") + `]}`)
	}
	list := `{"type":"list","title":"Todos","items":[{"title":"a"}]}`
	table := `{"type":"table","title":"Orders","columns":["Name","Value"],"rows":[["a",1]]}`
	for _, good := range [][]byte{
		screen(`{"type":"search"}`, list),
		screen(`{"type":"search","placeholder":"Search","targets":["Todos","Orders"],"fields":["title","Name"]}`, list, table),
		screen(list, `{"type":"search","targets":["Todos"]}`),
	} {
		if _, err := Validate(good); err != nil {
			t.Errorf("%s: %v", good, err)
		}
	}
	for _, bad := range [][]byte{
		screen(`{"type":"search","targets":["No such list"]}`, list),
		screen(`{"type":"search","targets":["This week"]}`, `{"type":"bars","title":"This week","items":[]}`),
		screen(`{"type":"search","targets":[1]}`, list),
		screen(`{"type":"search","targets":{"$data":"r.t"}}`, list),
		screen(`{"type":"search","fields":[["title"]]}`, list),
		screen(`{"type":"search","fields":{"$data":"r.f"}}`, list),
		screen(`{"type":"search","placeholder":3}`, list),
		screen(`{"type":"search","query":"a"}`, list),
	} {
		if _, err := Validate(bad); err == nil {
			t.Errorf("%s must be rejected", bad)
		}
	}
}

func TestSearchableMatchesComponents(t *testing.T) {
	if len(cat.Searchable) == 0 {
		t.Fatal("searchable is empty")
	}
	for typ, prop := range cat.Searchable {
		c, ok := cat.Components[typ]
		if !ok || c.Props[prop].Kind != "items" {
			t.Errorf("searchable %s.%s is not an items property of the catalog", typ, prop)
		}
	}
}

func TestValidateCalendarEntriesAndDays(t *testing.T) {
	scr := func(b string) []byte {
		return []byte(`{"version":1,"id":"a","title":"t","icon":"i","blocks":[` + b + `]}`)
	}
	for _, ok := range []string{
		`{"type":"calendar","days":14,"items":[{"date":"2026-11-25","value":1,"entries":[{"title":"Run 5km","time":"07:10","note":"easy"}]}]}`,
		`{"type":"calendar","items":[{"date":"2026-11-25","entries":[{"title":"Read"}]}]}`,
		`{"type":"calendar","days":371,"items":[{"date":"2026-11-25","value":1,"entries":null}]}`,
	} {
		if _, err := Validate(scr(ok)); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		`{"type":"calendar","days":0,"items":[]}`,
		`{"type":"calendar","days":400,"items":[]}`,
		`{"type":"calendar","days":7.5,"items":[]}`,
		`{"type":"calendar","items":[{"date":"2026-11-25","entries":"ran"}]}`,
		`{"type":"calendar","items":[{"date":"2026-11-25","entries":[{"time":"07:10"}]}]}`,
		`{"type":"calendar","items":[{"date":"2026-11-25","entries":[{"title":""}]}]}`,
		`{"type":"calendar","items":[{"date":"2026-11-25","entries":[{"title":"a","who":"me"}]}]}`,
		`{"type":"calendar","items":[{"date":"2026-11-25","entries":[{"title":{"a":1}}]}]}`,
		`{"type":"calendar","items":[{"date":"2026-11-25","entries":[` + strings.Repeat(`{"title":"a"},`, 50) + `{"title":"a"}]}]}`,
	} {
		if _, err := Validate(scr(bad)); err == nil {
			t.Errorf("%s should be rejected", bad)
		}
	}
}
