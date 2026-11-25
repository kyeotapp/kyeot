package server_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/server"
)

func safetyRoom(t *testing.T) (h *decktest.Harness, room protocol.Room, friend, friendID, other, stranger string) {
	t.Helper()
	h = decktest.New(t)
	a := h.CreateAgent("Agent A")
	room = makeRoom(t, h, "Family room", a.Agent.ID)
	friend = invite(t, h, "Alice")
	friendID = shareRoom(t, h, room.ID, friend)
	other = invite(t, h, "Bob")
	shareRoom(t, h, room.ID, other)
	stranger = invite(t, h, "Carol")
	return
}

func report(t *testing.T, h *decktest.Harness, tok, roomID string, body map[string]any, want int) protocol.RoomReport {
	t.Helper()
	var r protocol.RoomReport
	if s := h.APIAs(tok, "POST", "/api/rooms/"+roomID+"/reports", body, &r); s != want {
		t.Fatalf("report %v: %d, want %d", body, s, want)
	}
	return r
}

func reports(t *testing.T, h *decktest.Harness, tok string) []protocol.RoomReport {
	t.Helper()
	var out []protocol.RoomReport
	if s := h.APIAs(tok, "GET", "/api/reports", nil, &out); s != http.StatusOK {
		t.Fatalf("reports: %d", s)
	}
	return out
}

// Only room members can report, and only the handler (room owner) receives reports. The reporter's name doesn't go to other members.
func TestReportOnlyByRoomMembers(t *testing.T) {
	h, room, friend, friendID, other, stranger := safetyRoom(t)
	owner := h.ConnectClient()
	bad := talk(t, h, friend, room.ID, map[string]any{"text": "Annoying words"}, http.StatusCreated)
	mine := talk(t, h, other, room.ID, map[string]any{"text": "My post"}, http.StatusCreated)

	report(t, h, stranger, room.ID, map[string]any{"messageId": bad.ID, "reason": "spam"}, http.StatusNotFound)
	report(t, h, other, room.ID, map[string]any{"messageId": bad.ID, "reason": "nope"}, http.StatusBadRequest)
	report(t, h, other, room.ID, map[string]any{"messageId": mine.ID, "reason": "spam"}, http.StatusBadRequest) // own post
	report(t, h, other, room.ID, map[string]any{"reason": "spam"}, http.StatusBadRequest)
	report(t, h, other, room.ID, map[string]any{"targetId": "nobody", "reason": "spam"}, http.StatusBadRequest)

	r := report(t, h, other, room.ID, map[string]any{"messageId": bad.ID, "reason": "harassment", "note": "Stop"}, http.StatusCreated)
	if r.TargetID != friendID || r.MessageText != "Annoying words" || r.ReporterName != "" {
		t.Fatalf("report: %+v", r)
	}
	h.WaitFor(owner, "room.report", func(m protocol.ClientMessage) bool {
		return m.Type == "room.report" && m.RoomReport != nil && m.RoomReport.ID == r.ID
	})
	again := report(t, h, other, room.ID, map[string]any{"messageId": bad.ID, "reason": "spam"}, http.StatusCreated)
	if again.ID != r.ID {
		t.Fatalf("same report twice made a new one: %d vs %d", again.ID, r.ID)
	}

	got := reports(t, h, decktest.OwnerToken)
	if len(got) != 1 || got[0].ReporterName != "Bob" || got[0].Reason != "harassment" || len(got[0].Actions) != 0 {
		t.Fatalf("owner inbox: %+v", got)
	}
	for _, tok := range []string{friend, other, stranger} {
		if got := reports(t, h, tok); len(got) != 0 {
			t.Fatalf("non-handler sees reports: %+v", got)
		}
	}
	if !roomTalk(t, h, other, room.ID)[bad.ID].Reported || roomTalk(t, h, friend, room.ID)[bad.ID].Reported {
		t.Fatal("reported flag must be only for the reporter")
	}
}

// Only the room owner deletes others' posts (a "deleted post" placeholder). The author deletes their own post row entirely.
func TestDeleteRoomMessagePermissions(t *testing.T) {
	h, room, friend, _, other, stranger := safetyRoom(t)
	path := "/api/rooms/" + room.ID + "/messages/"
	ownerMsg := talk(t, h, decktest.OwnerToken, room.ID, map[string]any{"text": "Owner post"}, http.StatusCreated)
	friendMsg := talk(t, h, friend, room.ID, map[string]any{"text": "Alice post"}, http.StatusCreated)
	otherMsg := talk(t, h, other, room.ID, map[string]any{"text": "Bob post"}, http.StatusCreated)

	for _, c := range []struct {
		tok  string
		id   int64
		want int
	}{
		{friend, ownerMsg.ID, http.StatusForbidden},
		{friend, otherMsg.ID, http.StatusForbidden},
		{stranger, friendMsg.ID, http.StatusNotFound},
	} {
		if s := h.APIAs(c.tok, "DELETE", path+fmt.Sprint(c.id), nil, nil); s != c.want {
			t.Fatalf("delete %d: %d, want %d", c.id, s, c.want)
		}
	}
	if s := h.APIAs(friend, "DELETE", path+fmt.Sprint(friendMsg.ID), nil, nil); s != http.StatusNoContent {
		t.Fatalf("writer delete: %d", s)
	}
	if _, ok := roomTalk(t, h, other, room.ID)[friendMsg.ID]; ok {
		t.Fatal("writer-deleted message is still listed")
	}
	if s := h.API("DELETE", path+fmt.Sprint(otherMsg.ID), nil, nil); s != http.StatusNoContent {
		t.Fatalf("owner delete: %d", s)
	}
	if m := roomTalk(t, h, friend, room.ID)[otherMsg.ID]; m.Removed != "owner" || m.Text != "" {
		t.Fatalf("owner-deleted: %+v", m)
	}
	if s := h.APIAs(other, "PATCH", path+fmt.Sprint(otherMsg.ID), map[string]string{"text": "Revive"}, nil); s != http.StatusNotFound {
		t.Fatalf("edit removed: %d", s)
	}
	if s := h.API("DELETE", path+fmt.Sprint(otherMsg.ID), nil, nil); s != http.StatusNotFound {
		t.Fatalf("delete twice: %d", s)
	}
}

// Only the handler resolves. Deleting the post and removing happen together, and once closed it can't be redone.
func TestResolveReport(t *testing.T) {
	h, room, friend, friendID, other, _ := safetyRoom(t)
	bad := talk(t, h, friend, room.ID, map[string]any{"text": "Annoying words"}, http.StatusCreated)
	r := report(t, h, other, room.ID, map[string]any{"messageId": bad.ID, "reason": "harassment"}, http.StatusCreated)
	path := fmt.Sprintf("/api/reports/%d", r.ID)

	if s := h.APIAs(other, "POST", path, map[string]any{"actions": []string{"delete"}}, nil); s != http.StatusNotFound {
		t.Fatalf("reporter resolves: %d", s)
	}
	if s := h.API("POST", path, map[string]any{"actions": []string{"dismiss", "delete"}}, nil); s != http.StatusBadRequest {
		t.Fatalf("dismiss with delete: %d", s)
	}
	var done protocol.RoomReport
	if s := h.API("POST", path, map[string]any{"actions": []string{"delete", "remove"}}, &done); s != http.StatusOK || len(done.Actions) != 2 {
		t.Fatalf("resolve: %d %+v", s, done)
	}
	if m := roomTalk(t, h, other, room.ID)[bad.ID]; m.Removed != "owner" {
		t.Fatalf("message not removed: %+v", m)
	}
	var members []struct{ OwnerID string }
	h.API("GET", "/api/rooms/"+room.ID+"/members", nil, &members)
	for _, m := range members {
		if m.OwnerID == friendID {
			t.Fatal("target still a member")
		}
	}
	if s := h.API("POST", path, map[string]any{"actions": []string{"dismiss"}}, nil); s != http.StatusConflict {
		t.Fatalf("resolve twice: %d", s)
	}
}

// Blocking is per server. Only the blocker gets blocked, and the original text stays. Unblocking removes it.
func TestBlockMarksOnlyForBlocker(t *testing.T) {
	h, room, friend, friendID, other, _ := safetyRoom(t)
	m := talk(t, h, friend, room.ID, map[string]any{"text": "Hello"}, http.StatusCreated)
	if s := h.APIAs(other, "PUT", "/api/blocks/"+friendID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("block: %d", s)
	}
	if s := h.APIAs(other, "PUT", "/api/blocks/nobody", nil, nil); s != http.StatusNotFound {
		t.Fatalf("block nobody: %d", s)
	}
	var me struct{ OwnerID string }
	h.APIAs(other, "GET", "/api/me", nil, &me)
	if s := h.APIAs(other, "PUT", "/api/blocks/"+me.OwnerID, nil, nil); s != http.StatusBadRequest {
		t.Fatalf("block self: %d", s)
	}
	if got := roomTalk(t, h, other, room.ID)[m.ID]; !got.Blocked || got.Text != "Hello" {
		t.Fatalf("blocker view: %+v", got)
	}
	if roomTalk(t, h, decktest.OwnerToken, room.ID)[m.ID].Blocked {
		t.Fatal("others must not see blocked")
	}
	var list []protocol.BlockedPerson
	if h.APIAs(other, "GET", "/api/blocks", nil, &list); len(list) != 1 || list[0].Name != "Alice" {
		t.Fatalf("block list: %+v", list)
	}
	h.APIAs(other, "DELETE", "/api/blocks/"+friendID, nil, nil)
	if roomTalk(t, h, other, room.ID)[m.ID].Blocked {
		t.Fatal("unblock did not clear")
	}
}

// Without agreeing to the terms you can't post to a shared room. After agreeing you can.
func TestTermsRequiredForSharedRoom(t *testing.T) {
	h, room, _, _, _, _ := safetyRoom(t)
	var inv struct{ Code string }
	h.API("POST", "/api/owners/invite", nil, &inv)
	fresh := redeemNoTerms(t, h, inv.Code, "Newcomer", http.StatusOK)
	shareRoom(t, h, room.ID, fresh)
	var me struct {
		TermsAgreed  bool
		TermsVersion string
	}
	h.APIAs(fresh, "GET", "/api/me", nil, &me)
	if me.TermsAgreed || me.TermsVersion != server.TermsVersion {
		t.Fatalf("me: %+v", me)
	}
	talk(t, h, fresh, room.ID, map[string]any{"text": "Hello"}, http.StatusForbidden)
	if s := h.APIAs(fresh, "POST", "/api/me/terms", map[string]string{"version": "old"}, nil); s != http.StatusConflict {
		t.Fatalf("old version: %d", s)
	}
	h.AgreeTerms(fresh)
	talk(t, h, fresh, room.ID, map[string]any{"text": "Hello"}, http.StatusCreated)
}

// Word filtering is on by default and filtered is attached only for the receiver. It disappears when the room owner turns it off.
func TestRoomFilter(t *testing.T) {
	h, room, friend, _, other, _ := safetyRoom(t)
	if room.FilterOff {
		t.Fatal("filter must start on")
	}
	bad := talk(t, h, friend, room.ID, map[string]any{"text": "이 시발"}, http.StatusCreated)
	ok := talk(t, h, friend, room.ID, map[string]any{"text": "What's for dinner"}, http.StatusCreated)
	if got := roomTalk(t, h, other, room.ID); !got[bad.ID].Filtered || got[ok.ID].Filtered || got[bad.ID].Text == "" {
		t.Fatalf("receiver view: %+v", got)
	}
	if roomTalk(t, h, friend, room.ID)[bad.ID].Filtered {
		t.Fatal("writer must see own text")
	}
	if s := h.APIAs(friend, "PATCH", "/api/rooms/"+room.ID, map[string]any{"filter": false}, nil); s != http.StatusNotFound {
		t.Fatalf("member toggles filter: %d", s)
	}
	var r protocol.Room
	if s := h.API("PATCH", "/api/rooms/"+room.ID, map[string]any{"filter": false}, &r); s != http.StatusOK || !r.FilterOff || r.Name != "Family room" {
		t.Fatalf("filter off: %d %+v", s, r)
	}
	if roomTalk(t, h, other, room.ID)[bad.ID].Filtered {
		t.Fatal("filter off still hides")
	}
}

// When the room owner is the server owner and the room owner is reported, the report box carries no reporter name and removal is refused.
func TestReportOnServerOwnerHidesReporter(t *testing.T) {
	h, room, friend, _, _, _ := safetyRoom(t)
	mine := talk(t, h, decktest.OwnerToken, room.ID, map[string]any{"text": "Owner post"}, http.StatusCreated)
	r := report(t, h, friend, room.ID, map[string]any{"messageId": mine.ID, "reason": "harassment"}, http.StatusCreated)
	got := reports(t, h, decktest.OwnerToken)
	if len(got) != 1 || got[0].ReporterName != "" || !got[0].TargetOwner {
		t.Fatalf("self-target report: %+v", got)
	}
	if s := h.API("POST", fmt.Sprintf("/api/reports/%d", r.ID), map[string]any{"actions": []string{"remove"}}, nil); s != http.StatusBadRequest {
		t.Fatalf("remove room owner: %d", s)
	}
	var done protocol.RoomReport
	h.API("POST", fmt.Sprintf("/api/reports/%d", r.ID), map[string]any{"actions": []string{"dismiss"}}, &done)
	if done.MessageText != "" {
		t.Fatalf("closed report keeps text: %q", done.MessageText)
	}
}
