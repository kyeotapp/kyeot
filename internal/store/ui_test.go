package store_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/kyeotapp/kyeot/internal/store"
)

func openTest(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "deck.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedAgent(t *testing.T, s *store.Store) string {
	t.Helper()
	ag, _, err := s.CreateAgent(store.Owner, "Agent A")
	if err != nil {
		t.Fatal(err)
	}
	return ag.ID
}

func screens(t *testing.T) func([]store.ScreenRow, error) []store.ScreenRow {
	return func(v []store.ScreenRow, err error) []store.ScreenRow {
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func data(t *testing.T) func(map[string]store.DataRow, error) map[string]store.DataRow {
	return func(v map[string]store.DataRow, err error) map[string]store.DataRow {
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func TestScreensKeepVersions(t *testing.T) {
	s := openTest(t)
	agent := seedAgent(t, s)
	v1, _ := s.PutScreen(agent, "revenue", json.RawMessage(`{"a":1}`))
	v2, _ := s.PutScreen(agent, "revenue", json.RawMessage(`{"a":2}`))
	if v1 != 1 || v2 != 2 {
		t.Fatalf("versions %d %d", v1, v2)
	}
	list := screens(t)(s.Screens(agent))
	if len(list) != 1 || list[0].Version != 2 || string(list[0].Spec) != `{"a":2}` {
		t.Fatalf("latest: %+v", list)
	}
	if vs := screens(t)(s.ScreenVersions(agent, "revenue")); len(vs) != 2 || vs[0].Version != 2 {
		t.Fatalf("versions: %+v", vs)
	}
	s.RemoveScreen(agent, "revenue")
	if len(screens(t)(s.Screens(agent))) != 0 || len(screens(t)(s.ScreenVersions(agent, "revenue"))) != 2 {
		t.Fatal("version history is kept after deletion")
	}
	if v3, _ := s.PutScreen(agent, "revenue", json.RawMessage(`{"a":3}`)); v3 != 3 {
		t.Fatalf("after deleting and re-putting, the version number continues: %d", v3)
	}
	if list := screens(t)(s.Screens(agent)); len(list) != 1 || list[0].Version != 3 {
		t.Fatalf("re-putting brings it back: %+v", list)
	}
}

func TestTabsRoundTrip(t *testing.T) {
	s := openTest(t)
	agent := seedAgent(t, s)
	if tabs, set, _ := s.Tabs(agent); set || len(tabs) != 0 {
		t.Fatal("initially empty and never set")
	}
	s.SetTabs(agent, []string{"costs", "revenue"})
	if tabs, set, _ := s.Tabs(agent); !set || len(tabs) != 2 || tabs[1] != "revenue" {
		t.Fatalf("tabs %v", tabs)
	}
}

func TestDataBundles(t *testing.T) {
	s := openTest(t)
	agent := seedAgent(t, s)
	other := seedAgent(t, s)
	if d := data(t)(s.Data(agent)); d == nil || len(d) != 0 {
		t.Fatalf("initially an empty map: %v", d)
	}
	s.PutData(agent, "revenue", json.RawMessage(`{"month":1}`), nil)
	s.PutData(agent, "revenue", json.RawMessage(`{"month":2}`), nil)
	s.PutData(agent, "visits", json.RawMessage(`{"n":3}`), nil)
	d := data(t)(s.Data(agent))
	if len(d) != 2 || string(d["revenue"].Value) != `{"month":2}` || d["revenue"].UpdatedAt == 0 {
		t.Fatalf("replaced as a whole: %+v", d)
	}
	if len(data(t)(s.Data(other))) != 0 {
		t.Fatal("another agent's bundle must not be visible")
	}
	s.RemoveData(agent, "revenue")
	if d := data(t)(s.Data(agent)); len(d) != 1 || d["visits"].Value == nil {
		t.Fatalf("delete: %+v", d)
	}
}

func TestPartsStackVersionsAndRemove(t *testing.T) {
	s := openTest(t)
	a, _, _ := s.CreateAgent(store.Owner, "Agent A")
	for _, spec := range []string{`{"v":1}`, `{"v":2}`} {
		if _, err := s.PutPart(a.ID, "card", []byte(spec)); err != nil {
			t.Fatal(err)
		}
	}
	parts, err := s.Parts(a.ID)
	if err != nil || len(parts) != 1 || parts[0].Version != 2 || string(parts[0].Spec) != `{"v":2}` {
		t.Fatalf("parts %+v %v", parts, err)
	}
	if vs, _ := s.PartVersions(a.ID, "card"); len(vs) != 2 || vs[0].Version != 2 {
		t.Fatalf("versions %+v", vs)
	}
	if err := s.RemovePart(a.ID, "card"); err != nil {
		t.Fatal(err)
	}
	if parts, _ := s.Parts(a.ID); len(parts) != 0 {
		t.Fatalf("removed part still listed %+v", parts)
	}
	if v, _ := s.PutPart(a.ID, "card", []byte(`{"v":3}`)); v != 3 {
		t.Fatalf("re-put version %d", v)
	}
}
