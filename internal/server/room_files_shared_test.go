package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")

func uploadRaw(t *testing.T, h *decktest.Harness, token, name, mime string, body []byte) protocol.Attachment {
	t.Helper()
	req, _ := http.NewRequest("POST", h.URL+"/api/uploads?name="+url.QueryEscape(name), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", mime)
	var a protocol.Attachment
	if s := doJSON(t, req, &a); s != http.StatusCreated {
		t.Fatalf("upload %s: %d", name, s)
	}
	return a
}

func doJSON(t *testing.T, req *http.Request, out any) int {
	t.Helper()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		b, _ := io.ReadAll(res.Body)
		_ = json.Unmarshal(b, out)
	}
	return res.StatusCode
}

// fetchAs fetches path with token (no auth if empty) and returns status, type, and body.
func fetchAs(t *testing.T, h *decktest.Harness, token, path string) (int, string, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", h.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header.Get("Content-Type"), b
}

func attOf(t *testing.T, msgs map[int64]protocol.RoomMessage, id int64, n int) []protocol.Attachment {
	t.Helper()
	m, ok := msgs[id]
	if !ok || len(m.Attachments) != n {
		t.Fatalf("message %d attachments: %+v", id, m)
	}
	return m.Attachments
}

// Photos and files in a shared room are fetched by owner and member alike. Name, size, type, and bytes match. They remain after replies and edits.
func TestRoomFilesSharedBothWays(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	room := makeRoom(t, h, "Shared", a.Agent.ID)
	tokB := invite(t, h, "Bob")
	shareRoom(t, h, room.ID, tokB)

	pdf := []byte("%PDF-1.4 fake document")
	pic := uploadRaw(t, h, decktest.OwnerToken, "사진 1.png", "image/png", pngBytes)
	doc := uploadRaw(t, h, decktest.OwnerToken, "보고서.pdf", "application/pdf", pdf)
	fromA := talk(t, h, decktest.OwnerToken, room.ID, map[string]any{"text": "Look", "attachments": []string{pic.ID, doc.ID}}, http.StatusCreated)

	got := attOf(t, roomTalk(t, h, tokB, room.ID), fromA.ID, 2)
	want := []struct {
		name, mime string
		body       []byte
	}{{"사진 1.png", "image/png", pngBytes}, {"보고서.pdf", "application/pdf", pdf}}
	for i, w := range want {
		g := got[i]
		if g.Name != w.name || g.Mime != w.mime || g.Size != int64(len(w.body)) || g.URL == "" {
			t.Fatalf("B sees %d: %+v", i, g)
		}
		s, ct, b := fetchAs(t, h, tokB, g.URL)
		if s != http.StatusOK || ct != w.mime || !bytes.Equal(b, w.body) {
			t.Fatalf("B fetches %s: %d %s %q", w.name, s, ct, b)
		}
		if s, _, b := fetchAs(t, h, "", g.URL); s != http.StatusOK || !bytes.Equal(b, w.body) { // web <Image> fetches without headers
			t.Fatalf("signed url without header: %d", s)
		}
	}

	// A fetches the file B uploaded. Attachments stay the same after replies and edits
	bBody := []byte("Bob's memo")
	note := uploadRaw(t, h, tokB, "memo.txt", "text/plain", bBody)
	fromB := talk(t, h, tokB, room.ID, map[string]any{"text": "Reply", "quoteId": fmt.Sprint(fromA.ID), "attachments": []string{note.ID}}, http.StatusCreated)
	if fromB.Quote == nil || fromB.Quote.ID != fromA.ID {
		t.Fatalf("quote: %+v", fromB.Quote)
	}
	if s := h.APIAs(tokB, "PATCH", "/api/rooms/"+room.ID+"/messages/"+fmt.Sprint(fromB.ID), map[string]string{"text": "Edited reply"}, nil); s != http.StatusOK {
		t.Fatalf("edit: %d", s)
	}
	talk(t, h, decktest.OwnerToken, room.ID, map[string]any{"text": "Quote", "quoteId": fmt.Sprint(fromB.ID)}, http.StatusCreated)
	seenByA := roomTalk(t, h, decktest.OwnerToken, room.ID)
	nb := attOf(t, seenByA, fromB.ID, 1)[0]
	if seenByA[fromB.ID].Text != "Edited reply" || nb.Name != "memo.txt" {
		t.Fatalf("after edit: %+v", seenByA[fromB.ID])
	}
	if s, _, b := fetchAs(t, h, decktest.OwnerToken, nb.URL); s != http.StatusOK || !bytes.Equal(b, bBody) {
		t.Fatalf("A fetches B's file: %d %q", s, b)
	}
	attOf(t, seenByA, fromA.ID, 2) // the quoted post's attachments remain too
}

// A person not in the room, an unauthenticated request, and a forged URL can't fetch attachments.
func TestRoomFilesHiddenFromOutsiders(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("Agent A")
	room := makeRoom(t, h, "Shared", a.Agent.ID)
	tokB := invite(t, h, "Bob")
	shareRoom(t, h, room.ID, tokB)
	tokC := invite(t, h, "Carol")

	f := uploadRaw(t, h, decktest.OwnerToken, "a.png", "image/png", pngBytes)
	m := talk(t, h, decktest.OwnerToken, room.ID, map[string]any{"text": "", "attachments": []string{f.ID}}, http.StatusCreated)
	signed := m.Attachments[0].URL
	q, _ := url.ParseQuery(signed[strings.Index(signed, "?")+1:])
	bare := "/api/uploads/" + f.ID
	other := uploadRaw(t, h, tokC, "c.txt", "text/plain", []byte("c"))
	past := strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)

	if s := h.APIAs(tokC, "GET", "/api/rooms/"+room.ID+"/messages", nil, nil); s != http.StatusNotFound {
		t.Fatalf("C lists: %d", s)
	}
	for name, c := range map[string]struct{ tok, path string }{
		"C token, no sig":       {tokC, bare},
		"B token, no sig":       {tokB, bare}, // even a member can't fetch others' files without a signature
		"no auth, no sig":       {"", bare},
		"wrong sig":             {"", bare + "?exp=" + q.Get("exp") + "&sig=" + strings.Repeat("0", 32)},
		"later exp, same sig":   {"", bare + "?exp=" + strconv.FormatInt(time.Now().Add(48*time.Hour).Unix(), 10) + "&sig=" + q.Get("sig")},
		"past exp, same sig":    {"", bare + "?exp=" + past + "&sig=" + q.Get("sig")},
		"sig moved to other id": {"", "/api/uploads/" + other.ID + "?exp=" + q.Get("exp") + "&sig=" + q.Get("sig")},
		"agent token":           {a.Token, bare},
	} {
		if s, _, _ := fetchAs(t, h, c.tok, c.path); s != http.StatusNotFound {
			t.Errorf("%s: %d", name, s)
		}
	}
}

// A member who left or was removed can't get the room post list or new signed URLs. A signed URL already obtained stays open until it expires (24 hours).
func TestRoomFilesAfterMemberLeavesOrIsRemoved(t *testing.T) {
	for _, how := range []string{"leave", "kick"} {
		t.Run(how, func(t *testing.T) {
			h := decktest.New(t)
			a := h.CreateAgent("Agent A")
			room := makeRoom(t, h, "Shared", a.Agent.ID)
			tokB := invite(t, h, "Bob")
			idB := shareRoom(t, h, room.ID, tokB)
			f := uploadRaw(t, h, decktest.OwnerToken, "a.png", "image/png", pngBytes)
			m := talk(t, h, decktest.OwnerToken, room.ID, map[string]any{"text": "", "attachments": []string{f.ID}}, http.StatusCreated)
			held := attOf(t, roomTalk(t, h, tokB, room.ID), m.ID, 1)[0].URL

			tok := tokB
			if how == "kick" {
				tok = decktest.OwnerToken
			}
			if s := h.APIAs(tok, "DELETE", "/api/rooms/"+room.ID+"/members/"+idB, nil, nil); s != http.StatusNoContent {
				t.Fatalf("%s: %d", how, s)
			}
			if s := h.APIAs(tokB, "GET", "/api/rooms/"+room.ID+"/messages", nil, nil); s != http.StatusNotFound {
				t.Fatalf("list after %s: %d", how, s)
			}
			if s, _, _ := fetchAs(t, h, tokB, "/api/uploads/"+f.ID); s != http.StatusNotFound {
				t.Fatalf("token fetch after %s: %d", how, s)
			}
			if s, _, _ := fetchAs(t, h, tokB, held); s != http.StatusOK {
				t.Fatalf("held signed url after %s: %d (signed URLs are valid until expiry by design)", how, s)
			}
		})
	}
}
