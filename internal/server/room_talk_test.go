package server_test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/server"
)

func talk(t *testing.T, h *decktest.Harness, tok, roomID string, body map[string]any, want int) protocol.RoomMessage {
	t.Helper()
	body["ask"] = false
	var m protocol.RoomMessage
	if s := h.APIAs(tok, "POST", "/api/rooms/"+roomID+"/messages", body, &m); s != want {
		t.Fatalf("talk %v: %d, want %d", body, s, want)
	}
	return m
}

func roomTalk(t *testing.T, h *decktest.Harness, tok, roomID string) map[int64]protocol.RoomMessage {
	t.Helper()
	var msgs []protocol.RoomMessage
	if s := h.APIAs(tok, "GET", "/api/rooms/"+roomID+"/messages", nil, &msgs); s != http.StatusOK {
		t.Fatalf("messages: %d", s)
	}
	out := map[int64]protocol.RoomMessage{}
	for _, m := range msgs {
		out[m.ID] = m
	}
	return out
}

func fetchStatus(t *testing.T, url string) int {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

// Room person posts support quoting, editing the last post, reactions, and attachments like chat. Room members only.
func TestRoomTalkReplyEditReactFiles(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	owner := h.ConnectClient()
	room := makeRoom(t, h, "Contest", a.Agent.ID)
	other := makeRoom(t, h, "Other room", a.Agent.ID)
	tok2 := invite(t, h, "Bob")
	friend := h.ConnectClientAs(tok2)
	shareRoom(t, h, room.ID, tok2)
	stranger := invite(t, h, "Carol")
	path := "/api/rooms/" + room.ID + "/messages"

	// quote: same-room posts only; the original is cut at 300 characters
	long := talk(t, h, decktest.OwnerToken, room.ID, map[string]any{"text": strings.Repeat("a", 400)}, http.StatusCreated)
	elsewhere := talk(t, h, decktest.OwnerToken, other.ID, map[string]any{"text": "Other room post"}, http.StatusCreated)
	reply := talk(t, h, tok2, room.ID, map[string]any{"text": "That one", "quoteId": fmt.Sprint(long.ID)}, http.StatusCreated)
	if reply.Quote == nil || reply.Quote.ID != long.ID || reply.Quote.Text != strings.Repeat("a", 300)+"…" {
		t.Fatalf("quote: %+v", reply.Quote)
	}
	if got := roomTalk(t, h, decktest.OwnerToken, room.ID)[reply.ID]; got.Quote == nil || got.Quote.Text != reply.Quote.Text {
		t.Fatalf("stored quote: %+v", got)
	}
	talk(t, h, tok2, room.ID, map[string]any{"text": "x", "quoteId": fmt.Sprint(elsewhere.ID)}, http.StatusBadRequest)

	// edit: only the author's last post
	if s := h.API("PATCH", path+"/"+fmt.Sprint(reply.ID), map[string]string{"text": "Edited by other"}, nil); s != http.StatusForbidden {
		t.Fatalf("owner edits friend's: %d", s)
	}
	if s := h.APIAs(stranger, "PATCH", path+"/"+fmt.Sprint(reply.ID), map[string]string{"text": "Nope"}, nil); s != http.StatusNotFound {
		t.Fatalf("stranger edits: %d", s)
	}
	if s := h.APIAs(tok2, "PATCH", "/api/rooms/"+other.ID+"/messages/"+fmt.Sprint(elsewhere.ID), map[string]string{"text": "x"}, nil); s != http.StatusNotFound {
		t.Fatalf("edit in a room I'm not in: %d", s)
	}
	if s := h.APIAs(tok2, "PATCH", path+"/"+fmt.Sprint(reply.ID), map[string]string{"text": " "}, nil); s != http.StatusBadRequest {
		t.Fatalf("empty edit: %d", s)
	}
	var edited protocol.RoomMessage
	if s := h.APIAs(tok2, "PATCH", path+"/"+fmt.Sprint(reply.ID), map[string]string{"text": "Not that"}, &edited); s != http.StatusOK || !edited.Edited || edited.Text != "Not that" || edited.Quote == nil {
		t.Fatalf("edit: %d %+v", s, edited)
	}
	h.WaitFor(owner, "edited pushed", func(m protocol.ClientMessage) bool {
		return m.Type == "room.message" && m.RoomMessage.ID == reply.ID && m.RoomMessage.Edited && m.RoomMessage.Text == "Not that"
	})
	talk(t, h, tok2, room.ID, map[string]any{"text": "Next words"}, http.StatusCreated)
	if s := h.APIAs(tok2, "PATCH", path+"/"+fmt.Sprint(reply.ID), map[string]string{"text": "Late edit"}, nil); s != http.StatusConflict {
		t.Fatalf("edit older: %d", s)
	}

	// reactions: toggled per person; mine is relative to the viewer
	react := func(tok string, id int64, want bool) {
		t.Helper()
		var out struct{ On bool }
		if s := h.APIAs(tok, "POST", path+"/"+fmt.Sprint(id)+"/reactions", map[string]string{"emoji": "👍"}, &out); s != http.StatusOK || out.On != want {
			t.Fatalf("react: %d %+v, want %v", s, out, want)
		}
	}
	react(decktest.OwnerToken, reply.ID, true)
	react(tok2, reply.ID, true)
	if r := roomTalk(t, h, tok2, room.ID)[reply.ID].Reactions; len(r) != 1 || r[0] != (protocol.RoomReaction{Emoji: "👍", Count: 2, Mine: true}) {
		t.Fatalf("reactions: %+v", r)
	}
	react(decktest.OwnerToken, reply.ID, false)
	if r := roomTalk(t, h, decktest.OwnerToken, room.ID)[reply.ID].Reactions; len(r) != 1 || r[0] != (protocol.RoomReaction{Emoji: "👍", Count: 1, Mine: false}) {
		t.Fatalf("reactions after off: %+v", r)
	}
	h.WaitFor(friend, "reaction pushed with friend's mine", func(m protocol.ClientMessage) bool {
		r := m.RoomMessage
		return m.Type == "room.message" && r.ID == reply.ID && len(r.Reactions) == 1 && r.Reactions[0].Count == 1 && r.Reactions[0].Mine
	})
	if s := h.APIAs(stranger, "POST", path+"/"+fmt.Sprint(reply.ID)+"/reactions", map[string]string{"emoji": "👍"}, nil); s != http.StatusNotFound {
		t.Fatalf("stranger reacts: %d", s)
	}
	if s := h.APIAs(tok2, "POST", path+"/"+fmt.Sprint(elsewhere.ID)+"/reactions", map[string]string{"emoji": "👍"}, nil); s != http.StatusNotFound {
		t.Fatalf("react to another room's message through this room: %d", s)
	}
	if s := h.APIAs(tok2, "POST", path+"/"+fmt.Sprint(reply.ID)+"/reactions", map[string]string{"emoji": strings.Repeat("x", 17)}, nil); s != http.StatusBadRequest {
		t.Fatalf("long emoji: %d", s)
	}

	// attachments: attached to person posts; members fetch via signed URL; an outsider sees neither room posts nor files
	file := uploadAs(t, h, tok2, "photo.txt", "photo")
	withFile := talk(t, h, tok2, room.ID, map[string]any{"text": "", "attachments": []string{file.ID}}, http.StatusCreated)
	if len(withFile.Attachments) != 1 || withFile.Attachments[0].ID != file.ID || withFile.Attachments[0].URL == "" {
		t.Fatalf("attachments: %+v", withFile)
	}
	seen := roomTalk(t, h, decktest.OwnerToken, room.ID)[withFile.ID]
	if len(seen.Attachments) != 1 || fetchStatus(t, h.URL+seen.Attachments[0].URL) != http.StatusOK {
		t.Fatalf("owner fetches member's file: %+v", seen)
	}
	if s := h.APIAs(stranger, "GET", path, nil, nil); s != http.StatusNotFound {
		t.Fatalf("stranger lists talk: %d", s)
	}
	if s := h.APIAs(stranger, "GET", "/api/uploads/"+file.ID, nil, nil); s != http.StatusNotFound {
		t.Fatalf("stranger fetches by token: %d", s)
	}
	ownerFile := uploadAs(t, h, decktest.OwnerToken, "mine.txt", "Owner file")
	talk(t, h, decktest.OwnerToken, room.ID, map[string]any{"text": "Owner attachment", "attachments": []string{ownerFile.ID}}, http.StatusCreated)
	if s := h.APIAs(a.Token, "GET", "/api/uploads/"+ownerFile.ID, nil, nil); s != http.StatusNotFound {
		t.Fatalf("owner's agent fetches room talk file: %d", s)
	}
	fileOnly := talk(t, h, decktest.OwnerToken, room.ID, map[string]any{"text": "This?", "quoteId": fmt.Sprint(withFile.ID)}, http.StatusCreated)
	if fileOnly.Quote == nil || fileOnly.Quote.Text != "photo.txt" {
		t.Fatalf("quote of a file-only message: %+v", fileOnly.Quote)
	}
	talk(t, h, tok2, room.ID, map[string]any{"text": "Other's file", "attachments": []string{uploadAs(t, h, decktest.OwnerToken, "o.txt", "o").ID}}, http.StatusBadRequest)
}

// Deleting an account removes reactions and attachments on person posts in that user's room, while attachments placed in others' rooms stay. Reactions pressed in others' rooms are removed.
func TestDeleteAccountCleansRoomTalk(t *testing.T) {
	dir := t.TempDir()
	dbPath, upDir := filepath.Join(dir, "kyeot.db"), filepath.Join(dir, "uploads")
	h := decktest.NewWith(t, func(o *server.Options) { o.DBPath, o.UploadDir = dbPath, upDir })
	a := h.CreateAgent("Agent A")
	mine := makeRoom(t, h, "Owner room", a.Agent.ID)
	tok2 := invite(t, h, "Leaving friend")
	shareRoom(t, h, mine.ID, tok2)

	// What the friend leaves in the owner's room: a post with a file, reactions
	keptFile := uploadAs(t, h, tok2, "kept.txt", "Kept file")
	kept := talk(t, h, tok2, mine.ID, map[string]any{"text": "Kept post", "attachments": []string{keptFile.ID}}, http.StatusCreated)
	ownerMsg := talk(t, h, decktest.OwnerToken, mine.ID, map[string]any{"text": "Owner post"}, http.StatusCreated)
	h.APIAs(tok2, "POST", "/api/rooms/"+mine.ID+"/messages/"+fmt.Sprint(ownerMsg.ID)+"/reactions", map[string]string{"emoji": "🔥"}, nil)

	// Friend's room: the owner joins and leaves a post with a file and reactions
	var fa decktest.Created
	h.APIAs(tok2, "POST", "/api/agents", map[string]string{"name": "Friend bot"}, &fa)
	var theirs protocol.Room
	if s := h.APIAs(tok2, "POST", "/api/rooms", map[string]any{"name": "Friend room", "agentId": fa.Agent.ID}, &theirs); s != http.StatusCreated {
		t.Fatalf("friend room: %d", s)
	}
	if s := h.API("POST", "/api/rooms/join", map[string]string{"code": roomCode(t, h, tok2, theirs.ID)}, nil); s != http.StatusOK {
		t.Fatalf("owner joins: %d", s)
	}
	goneFile := uploadAs(t, h, decktest.OwnerToken, "gone.txt", "Vanishing file")
	gone := talk(t, h, decktest.OwnerToken, theirs.ID, map[string]any{"text": "Vanishing post", "attachments": []string{goneFile.ID}}, http.StatusCreated)
	h.API("POST", "/api/rooms/"+theirs.ID+"/messages/"+fmt.Sprint(gone.ID)+"/reactions", map[string]string{"emoji": "😀"}, nil)

	if s := h.APIAs(tok2, "DELETE", "/api/me", nil, nil); s != http.StatusNoContent {
		t.Fatalf("delete: %d", s)
	}
	if _, err := os.Stat(filepath.Join(upDir, goneFile.ID)); !os.IsNotExist(err) {
		t.Fatalf("friend room file left on disk: %v", err)
	}
	if hits := rowsContaining(t, dbPath, goneFile.ID, "Vanishing post", "😀", "🔥"); len(hits) > 0 {
		t.Fatalf("left behind: %v", hits)
	}
	got := roomTalk(t, h, decktest.OwnerToken, mine.ID)
	k := got[kept.ID]
	if k.OwnerID != "gone" || len(k.Attachments) != 1 || fetchStatus(t, h.URL+k.Attachments[0].URL) != http.StatusOK {
		t.Fatalf("kept message: %+v", k)
	}
	if r := got[ownerMsg.ID].Reactions; len(r) != 0 {
		t.Fatalf("deleted user's reaction stayed: %+v", r)
	}
}

// Deleting a room also deletes its person posts' attachment files from disk.
func TestDeleteRoomRemovesTalkFiles(t *testing.T) {
	upDir := t.TempDir()
	h := decktest.NewWith(t, func(o *server.Options) { o.UploadDir = upDir })
	a := h.CreateAgent("Agent A")
	room := makeRoom(t, h, "Room", a.Agent.ID)
	f := uploadAs(t, h, decktest.OwnerToken, "a.txt", "a")
	talk(t, h, decktest.OwnerToken, room.ID, map[string]any{"text": "File", "attachments": []string{f.ID}}, http.StatusCreated)
	if s := h.API("DELETE", "/api/rooms/"+room.ID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("delete room: %d", s)
	}
	if _, err := os.Stat(filepath.Join(upDir, f.ID)); !os.IsNotExist(err) {
		t.Fatalf("file left: %v", err)
	}
}

// A file already attached to a task or another room's post is not attached to a room post again, since it would become a broken attachment when the original post is deleted.
func TestRoomTalkRejectsUsedUpload(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	c := h.ConnectClient()
	h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))
	room := makeRoom(t, h, "Room", a.Agent.ID)
	other := makeRoom(t, h, "Other room", a.Agent.ID)

	f := uploadAs(t, h, decktest.OwnerToken, "a.txt", "a")
	talk(t, h, decktest.OwnerToken, room.ID, map[string]any{"text": "First", "attachments": []string{f.ID}}, http.StatusCreated)
	talk(t, h, decktest.OwnerToken, room.ID, map[string]any{"text": "Again", "attachments": []string{f.ID}}, http.StatusBadRequest)
	talk(t, h, decktest.OwnerToken, other.ID, map[string]any{"text": "In other room", "attachments": []string{f.ID}}, http.StatusBadRequest)

	g := uploadAs(t, h, decktest.OwnerToken, "b.txt", "b")
	var task protocol.Task
	if s := h.API("POST", "/api/tasks", map[string]any{"agentId": a.Agent.ID, "text": "Look", "attachments": []string{g.ID}}, &task); s != http.StatusCreated {
		t.Fatalf("task: %d", s)
	}
	talk(t, h, decktest.OwnerToken, room.ID, map[string]any{"text": "Task file", "attachments": []string{g.ID}}, http.StatusBadRequest)

	// a rejected post is not kept
	for _, m := range roomTalk(t, h, decktest.OwnerToken, room.ID) {
		if m.Text != "First" {
			t.Fatalf("rejected message stored: %+v", m)
		}
	}
}

// One person can set up to 8 reactions on a post. Turning one off frees a slot. Other people are counted separately.
func TestRoomReactionLimit(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	room := makeRoom(t, h, "Room", a.Agent.ID)
	tok2 := invite(t, h, "Bob")
	shareRoom(t, h, room.ID, tok2)
	m := talk(t, h, decktest.OwnerToken, room.ID, map[string]any{"text": "Post"}, http.StatusCreated)
	path := "/api/rooms/" + room.ID + "/messages/" + fmt.Sprint(m.ID) + "/reactions"
	react := func(tok, emoji string) int {
		t.Helper()
		return h.APIAs(tok, "POST", path, map[string]string{"emoji": emoji}, nil)
	}
	for i := range 8 {
		if s := react(decktest.OwnerToken, fmt.Sprint("e", i)); s != http.StatusOK {
			t.Fatalf("reaction %d: %d", i, s)
		}
	}
	if s := react(decktest.OwnerToken, "e8"); s != http.StatusBadRequest {
		t.Fatalf("9th reaction: %d", s)
	}
	if s := react(tok2, "e8"); s != http.StatusOK {
		t.Fatalf("other person's reaction: %d", s)
	}
	if s := react(decktest.OwnerToken, "e0"); s != http.StatusOK {
		t.Fatalf("turn off: %d", s)
	}
	if s := react(decktest.OwnerToken, "e8"); s != http.StatusOK {
		t.Fatalf("after freeing a slot: %d", s)
	}
	if n := len(roomTalk(t, h, decktest.OwnerToken, room.ID)[m.ID].Reactions); n != 8 {
		t.Fatalf("reaction kinds: %d", n)
	}
}
