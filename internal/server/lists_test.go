package server_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/server"
)

type listGroup struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Position int    `json:"position"`
}

type listItem struct {
	ID       string `json:"id"`
	Pinned   *int   `json:"pinned"`
	GroupID  string `json:"groupId"`
	GroupPos int    `json:"groupPos"`
}

type listPrefs struct {
	Groups []listGroup `json:"groups"`
	Items  []listItem  `json:"items"`
}

func pinned(p listPrefs) []string {
	var out []string
	for _, it := range p.Items {
		if it.Pinned != nil {
			out = append(out, it.ID)
		}
	}
	return out
}

func itemOf(p listPrefs, id string) listItem {
	for _, it := range p.Items {
		if it.ID == id {
			return it
		}
	}
	return listItem{ID: id}
}

// newTasks makes n conversations with an agent attached.
func newTasks(t *testing.T, h *decktest.Harness, agentID string, n int) []string {
	t.Helper()
	var ids []string
	for i := range n {
		var task protocol.Task
		if s := h.API("POST", "/api/tasks", map[string]string{"agentId": agentID, "text": "words " + string(rune('A'+i))}, &task); s != http.StatusCreated {
			t.Fatalf("task: %d", s)
		}
		ids = append(ids, task.ID)
	}
	return ids
}

func agentUp(t *testing.T, h *decktest.Harness) decktest.Created {
	a := h.CreateAgent("Agent A")
	c := h.ConnectClient()
	h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	return a
}

func TestPinUnpinAndReorder(t *testing.T) {
	h := decktest.New(t)
	a := agentUp(t, h)
	ids := newTasks(t, h, a.Agent.ID, 3)

	var p listPrefs
	for _, id := range []string{ids[2], ids[0]} {
		if s := h.API("PATCH", "/api/lists/tasks/items/"+id, map[string]any{"pinned": true}, &p); s != http.StatusOK {
			t.Fatalf("pin: %d", s)
		}
	}
	if got := pinned(p); !slices.Equal(got, []string{ids[2], ids[0]}) {
		t.Fatalf("pin order: %v", got)
	}
	h.API("PATCH", "/api/lists/tasks/items/"+ids[2], map[string]any{"pinned": true}, &p) // pinning again keeps the order
	if got := pinned(p); !slices.Equal(got, []string{ids[2], ids[0]}) {
		t.Fatalf("re-pin moved: %v", got)
	}
	if s := h.API("PUT", "/api/lists/tasks/pins", map[string]any{"ids": []string{ids[0], ids[1], ids[2]}}, &p); s != http.StatusOK {
		t.Fatalf("reorder: %d", s)
	}
	if got := pinned(p); !slices.Equal(got, []string{ids[0], ids[1], ids[2]}) {
		t.Fatalf("reorder: %v", got)
	}
	h.API("PATCH", "/api/lists/tasks/items/"+ids[1], map[string]any{"pinned": false}, &p)
	if got := pinned(p); !slices.Equal(got, []string{ids[0], ids[2]}) {
		t.Fatalf("unpin: %v", got)
	}
	if s := h.API("PUT", "/api/lists/tasks/pins", map[string]any{"ids": []string{ids[0], ids[0]}}, nil); s != http.StatusBadRequest {
		t.Fatalf("dup ids: %d", s)
	}

	var tasks []protocol.Task
	h.API("GET", "/api/tasks", nil, &tasks)
	for _, task := range tasks {
		switch task.ID {
		case ids[0]:
			if task.Pinned == nil || *task.Pinned != 0 {
				t.Fatalf("first pin in list: %v", task.Pinned)
			}
		case ids[1]:
			if task.Pinned != nil {
				t.Fatalf("unpinned has pin %d", *task.Pinned)
			}
		}
	}
}

func TestListGroups(t *testing.T) {
	h := decktest.New(t)
	a := agentUp(t, h)
	ids := newTasks(t, h, a.Agent.ID, 2)

	var g1, g2 listGroup
	if s := h.API("POST", "/api/lists/tasks/groups", map[string]string{"name": " Work "}, &g1); s != http.StatusCreated || g1.Name != "Work" {
		t.Fatalf("create: %d %+v", s, g1)
	}
	h.API("POST", "/api/lists/tasks/groups", map[string]string{"name": "Play"}, &g2)
	if s := h.API("POST", "/api/lists/tasks/groups", map[string]string{"name": ""}, nil); s != http.StatusBadRequest {
		t.Fatalf("empty name: %d", s)
	}
	if s := h.API("PATCH", "/api/lists/tasks/groups/"+g1.ID, map[string]string{"name": "Office"}, &g1); s != http.StatusOK || g1.Name != "Office" {
		t.Fatalf("rename: %d %+v", s, g1)
	}
	var p listPrefs
	if s := h.API("PUT", "/api/lists/tasks/groups", map[string]any{"ids": []string{g2.ID, g1.ID}}, &p); s != http.StatusOK || p.Groups[0].ID != g2.ID {
		t.Fatalf("group order: %d %+v", s, p.Groups)
	}
	if s := h.API("PUT", "/api/lists/tasks/groups", map[string]any{"ids": []string{g2.ID}}, nil); s != http.StatusBadRequest {
		t.Fatalf("partial group order: %d", s)
	}
	h.API("PATCH", "/api/lists/tasks/items/"+ids[0], map[string]any{"groupId": g1.ID, "pinned": true}, &p)
	h.API("PATCH", "/api/lists/tasks/items/"+ids[1], map[string]any{"groupId": g1.ID}, &p)
	if itemOf(p, ids[1]).GroupID != g1.ID {
		t.Fatalf("in group: %+v", p.Items)
	}
	// room groups are separate from conversation groups
	if s := h.API("PATCH", "/api/lists/tasks/items/"+ids[1], map[string]any{"groupId": "nope"}, nil); s != http.StatusNotFound {
		t.Fatalf("unknown group: %d", s)
	}
	var rg listPrefs
	h.API("GET", "/api/lists/rooms", nil, &rg)
	if len(rg.Groups) != 0 {
		t.Fatalf("room groups leak: %+v", rg.Groups)
	}
	h.API("PATCH", "/api/lists/tasks/items/"+ids[1], map[string]any{"groupId": ""}, &p)
	if itemOf(p, ids[1]).GroupID != "" {
		t.Fatal("ungroup")
	}

	if s := h.API("DELETE", "/api/lists/tasks/groups/"+g1.ID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("delete group: %d", s)
	}
	h.API("GET", "/api/lists/tasks", nil, &p)
	if it := itemOf(p, ids[0]); it.GroupID != "" || it.Pinned == nil {
		t.Fatalf("after group delete: %+v", it)
	}
	if len(p.Groups) != 1 || p.Groups[0].ID != g2.ID {
		t.Fatalf("groups: %+v", p.Groups)
	}
	if s := h.API("DELETE", "/api/lists/tasks/groups/"+g1.ID, nil, nil); s != http.StatusNotFound {
		t.Fatalf("delete twice: %d", s)
	}
	if s := h.API("GET", "/api/lists/other", nil, nil); s != http.StatusNotFound {
		t.Fatalf("unknown kind: %d", s)
	}
}

func TestGroupItemOrder(t *testing.T) {
	h := decktest.New(t)
	a := agentUp(t, h)
	ids := newTasks(t, h, a.Agent.ID, 4)
	var g, g2 listGroup
	h.API("POST", "/api/lists/tasks/groups", map[string]string{"name": "Work"}, &g)
	h.API("POST", "/api/lists/tasks/groups", map[string]string{"name": "Home"}, &g2)
	var p listPrefs
	h.API("PATCH", "/api/lists/tasks/items/"+ids[0], map[string]any{"groupId": g.ID}, &p)
	h.API("PATCH", "/api/lists/tasks/items/"+ids[1], map[string]any{"groupId": g.ID}, &p)
	if itemOf(p, ids[0]).GroupPos >= itemOf(p, ids[1]).GroupPos {
		t.Fatalf("adding via the menu goes to the very end: %+v", p.Items)
	}
	// ids[2], which was in another group, is inserted in the middle. ids[1], not in the list, stays behind
	h.API("PATCH", "/api/lists/tasks/items/"+ids[2], map[string]any{"groupId": g2.ID}, &p)
	if s := h.API("PUT", "/api/lists/tasks/groups/"+g.ID+"/items", map[string]any{"ids": []string{ids[0], ids[2]}}, &p); s != http.StatusOK {
		t.Fatalf("put items: %d", s)
	}
	pos := func(id string) int { return itemOf(p, id).GroupPos }
	if itemOf(p, ids[2]).GroupID != g.ID || !(pos(ids[0]) < pos(ids[2]) && pos(ids[2]) < pos(ids[1])) {
		t.Fatalf("order must be ids0 < ids2 < ids1: %+v", p.Items)
	}
	if s := h.API("PUT", "/api/lists/tasks/groups/nope/items", map[string]any{"ids": []string{ids[0]}}, nil); s != http.StatusNotFound {
		t.Fatalf("unknown group: %d", s)
	}
	if s := h.API("PUT", "/api/lists/tasks/groups/"+g.ID+"/items", map[string]any{"ids": []string{"nope"}}, nil); s != http.StatusNotFound {
		t.Fatalf("unknown task: %d", s)
	}
	if s := h.API("PUT", "/api/lists/tasks/groups/"+g.ID+"/items", map[string]any{"ids": []string{ids[0], ids[0]}}, nil); s != http.StatusBadRequest {
		t.Fatalf("duplicate: %d", s)
	}
	// adding to the same group again via the menu does not change the position
	before := pos(ids[0])
	h.API("PATCH", "/api/lists/tasks/items/"+ids[0], map[string]any{"groupId": g.ID}, &p)
	if pos(ids[0]) != before {
		t.Fatalf("reassigning the same group changed the order: %+v", p.Items)
	}
}

func TestListPrefsArePerPerson(t *testing.T) {
	h := decktest.New(t)
	a := agentUp(t, h)
	var r1, r2 protocol.Room
	h.API("POST", "/api/rooms", map[string]any{"name": "One", "agentId": a.Agent.ID}, &r1)
	h.API("POST", "/api/rooms", map[string]any{"name": "Two", "agentId": a.Agent.ID}, &r2)
	tok2 := invite(t, h, "Bob")
	for _, r := range []protocol.Room{r1, r2} {
		var inv struct{ Code string }
		h.API("POST", "/api/rooms/"+r.ID+"/invite", nil, &inv)
		if s := h.APIAs(tok2, "POST", "/api/rooms/join", map[string]string{"code": inv.Code}, nil); s != http.StatusOK {
			t.Fatalf("join: %d", s)
		}
	}

	h.API("PUT", "/api/lists/rooms/pins", map[string]any{"ids": []string{r1.ID}}, nil)
	h.APIAs(tok2, "PUT", "/api/lists/rooms/pins", map[string]any{"ids": []string{r2.ID, r1.ID}}, nil)
	var g listGroup
	h.APIAs(tok2, "POST", "/api/lists/rooms/groups", map[string]string{"name": "Bob group"}, &g)
	h.APIAs(tok2, "PATCH", "/api/lists/rooms/items/"+r1.ID, map[string]any{"groupId": g.ID}, nil)
	// cannot add with someone else's group id
	if s := h.API("PATCH", "/api/lists/rooms/items/"+r1.ID, map[string]any{"groupId": g.ID}, nil); s != http.StatusNotFound {
		t.Fatalf("other's group: %d", s)
	}
	if s := h.API("PATCH", "/api/lists/rooms/groups/"+g.ID, map[string]string{"name": "hijack"}, nil); s != http.StatusNotFound {
		t.Fatalf("rename other's group: %d", s)
	}

	var mine, theirs listPrefs
	h.API("GET", "/api/lists/rooms", nil, &mine)
	h.APIAs(tok2, "GET", "/api/lists/rooms", nil, &theirs)
	if !slices.Equal(pinned(mine), []string{r1.ID}) || len(mine.Groups) != 0 || itemOf(mine, r1.ID).GroupID != "" {
		t.Fatalf("owner prefs: %+v", mine)
	}
	if !slices.Equal(pinned(theirs), []string{r2.ID, r1.ID}) || itemOf(theirs, r1.ID).GroupID != g.ID {
		t.Fatalf("friend prefs: %+v", theirs)
	}
	var rooms []protocol.Room
	h.API("GET", "/api/rooms", nil, &rooms)
	for _, r := range rooms {
		if r.ID == r2.ID && r.Pinned != nil {
			t.Fatal("friend's pin shows for owner")
		}
		if r.GroupID != "" {
			t.Fatal("friend's group shows for owner")
		}
	}
	h.APIAs(tok2, "GET", "/api/rooms", nil, &rooms)
	for _, r := range rooms {
		if r.ID == r2.ID && (r.Pinned == nil || *r.Pinned != 0) {
			t.Fatalf("friend room pin: %v", r.Pinned)
		}
	}

	// when Bob leaves the room, only Bob's settings for that room are deleted
	var me struct{ OwnerID string }
	h.APIAs(tok2, "GET", "/api/me", nil, &me)
	if s := h.APIAs(tok2, "DELETE", "/api/rooms/"+r1.ID+"/members/"+me.OwnerID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("leave: %d", s)
	}
	h.APIAs(tok2, "GET", "/api/lists/rooms", nil, &theirs)
	h.API("GET", "/api/lists/rooms", nil, &mine)
	if !slices.Equal(pinned(theirs), []string{r2.ID}) || !slices.Equal(pinned(mine), []string{r1.ID}) {
		t.Fatalf("after leave: friend %v owner %v", pinned(theirs), pinned(mine))
	}
	// deleting the room removes it from everyone's settings
	h.API("DELETE", "/api/rooms/"+r2.ID, nil, nil)
	h.APIAs(tok2, "GET", "/api/lists/rooms", nil, &theirs)
	if len(pinned(theirs)) != 0 {
		t.Fatalf("after room delete: %v", pinned(theirs))
	}
}

func TestListTargetsOfOthersAreNotFound(t *testing.T) {
	h := decktest.New(t)
	a := agentUp(t, h)
	ids := newTasks(t, h, a.Agent.ID, 1)
	var room protocol.Room
	h.API("POST", "/api/rooms", map[string]any{"name": "my room", "agentId": a.Agent.ID}, &room)
	tok2 := invite(t, h, "Other")
	for _, c := range []struct{ path, body string }{
		{"/api/lists/tasks/items/" + ids[0], `{"pinned":true}`},
		{"/api/lists/rooms/items/" + room.ID, `{"pinned":true}`},
		{"/api/lists/tasks/items/nope", `{"pinned":true}`},
	} {
		var body map[string]any
		json.Unmarshal([]byte(c.body), &body)
		if s := h.APIAs(tok2, "PATCH", c.path, body, nil); s != http.StatusNotFound {
			t.Fatalf("%s: %d", c.path, s)
		}
	}
	if s := h.APIAs(tok2, "PUT", "/api/lists/rooms/pins", map[string]any{"ids": []string{room.ID}}, nil); s != http.StatusNotFound {
		t.Fatalf("pin others room: %d", s)
	}
	var p listPrefs
	h.APIAs(tok2, "GET", "/api/lists/rooms", nil, &p)
	if len(p.Items) != 0 {
		t.Fatalf("leaked: %+v", p)
	}
}

func TestHiddenTaskLeavesPrefs(t *testing.T) {
	h := decktest.New(t)
	a := agentUp(t, h)
	ids := newTasks(t, h, a.Agent.ID, 2)
	h.API("PUT", "/api/lists/tasks/pins", map[string]any{"ids": ids}, nil)
	h.API("POST", "/api/tasks/"+ids[0]+"/cancel", nil, nil)
	if s := h.API("DELETE", "/api/tasks/"+ids[0], nil, nil); s != http.StatusNoContent {
		t.Fatalf("hide: %d", s)
	}
	var p listPrefs
	h.API("GET", "/api/lists/tasks", nil, &p)
	if !slices.Equal(pinned(p), []string{ids[1]}) {
		t.Fatalf("after hide: %v", pinned(p))
	}
	if s := h.API("PATCH", "/api/lists/tasks/items/"+ids[0], map[string]any{"pinned": true}, nil); s != http.StatusNotFound {
		t.Fatalf("pin hidden: %d", s)
	}
}

// Fields old apps read stay as they are, and with no pins or groups the new fields are not added either.
func TestListFieldsOnlyAdded(t *testing.T) {
	h := decktest.New(t)
	a := agentUp(t, h)
	ids := newTasks(t, h, a.Agent.ID, 2)
	var room protocol.Room
	h.API("POST", "/api/rooms", map[string]any{"name": "Room", "agentId": a.Agent.ID}, &room)
	var g listGroup
	h.API("POST", "/api/lists/tasks/groups", map[string]string{"name": "Bundle"}, &g)
	h.API("PATCH", "/api/lists/tasks/items/"+ids[0], map[string]any{"pinned": true, "groupId": g.ID}, nil)

	var tasks []map[string]any
	h.API("GET", "/api/tasks", nil, &tasks)
	for _, task := range tasks {
		for _, k := range []string{"id", "agentId", "title", "state", "createdAt", "updatedAt"} {
			if _, ok := task[k]; !ok {
				t.Fatalf("missing %s: %v", k, task)
			}
		}
		_, hasPin := task["pinned"]
		_, hasGroup := task["groupId"]
		if task["id"] == ids[0] && (!hasPin || task["groupId"] != g.ID || task["pinned"].(float64) != 0) {
			t.Fatalf("pinned task: %v", task)
		}
		if task["id"] == ids[1] && (hasPin || hasGroup) {
			t.Fatalf("plain task got new fields: %v", task)
		}
	}
	var rooms []map[string]any
	h.API("GET", "/api/rooms", nil, &rooms)
	for _, k := range []string{"id", "name", "agentId", "routineId", "routineIds", "createdAt", "lastAt"} {
		if _, ok := rooms[0][k]; !ok {
			t.Fatalf("room missing %s: %v", k, rooms[0])
		}
	}
	if _, ok := rooms[0]["pinned"]; ok {
		t.Fatalf("plain room got pinned: %v", rooms[0])
	}
	if !slices.Contains(server.Features, "lists.pin") || !slices.Contains(server.Features, "lists.groups") {
		t.Fatalf("features: %v", server.Features)
	}
}
