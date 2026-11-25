package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/push"
	"github.com/kyeotapp/kyeot/internal/store"
)

// TermsVersion is the current terms version; bump the date when the text changes and everyone agrees again.
const TermsVersion = "2026-11-22"

const reportNoteRunes = 300

var reasonLabel = map[string]string{
	"spam":          "스팸이나 광고",
	"harassment":    "괴롭힘이나 욕설",
	"inappropriate": "부적절한 내용",
	"other":         "기타",
}

// needTerms rejects posting to a shared room (one with members, or one I'm a member of) without having agreed to the terms.
// A solo room is invisible to others, so it isn't asked.
func (s *Server) needTerms(roomID, roomOwner, writer string) error {
	if writer == roomOwner && !s.store.HasRoomMembers(roomID) {
		return nil
	}
	if s.store.TermsVersion(writer) != TermsVersion {
		return reject(http.StatusForbidden, "terms_required: agree to the terms before writing in a shared room")
	}
	return nil
}

// DeleteRoomMessage deletes the row if the caller is the author, or leaves a "deleted post" placeholder if the room owner. Anyone else gets 403.
// The caller deletes the files of the returned attachment ids.
func (h *hub) DeleteRoomMessage(roomOwner, roomID, actor string, id int64) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	m, err := h.store.RoomMessage(roomID, id, actor)
	if errors.Is(err, store.ErrNotFound) || (err == nil && m.Removed != "") {
		return nil, reject(http.StatusNotFound, "message not found in this room")
	}
	if err != nil {
		return nil, err
	}
	switch {
	case m.OwnerID == actor:
		files, err := h.store.DeleteRoomMessage(id)
		if err != nil {
			return nil, err
		}
		gone := protocol.RoomMessage{ID: m.ID, RoomID: roomID, OwnerID: m.OwnerID, Name: m.Name, CreatedAt: m.CreatedAt, Removed: store.RemovedBySelf}
		for _, who := range h.roomAudience(roomOwner, roomID) {
			h.toClients(who, protocol.ClientMessage{Type: "room.message", RoomMessage: &gone})
		}
		return files, nil
	case actor == roomOwner:
		return h.removeRoomMessage(roomOwner, roomID, id)
	}
	return nil, reject(http.StatusForbidden, "only the writer or the room owner can delete")
}

// removeRoomMessage is shared by owner delete and the report action "delete post". Called under the hub lock.
func (h *hub) removeRoomMessage(roomOwner, roomID string, id int64) ([]string, error) {
	files, err := h.store.RemoveRoomMessage(id)
	if err != nil {
		return nil, err
	}
	_, err = h.roomTalkChanged(roomOwner, roomID, id, roomOwner)
	return files, err
}

func (h *hub) roomAudience(roomOwner, roomID string) []string {
	audience := []string{roomOwner}
	for _, mb := range h.roomMembers(roomID) {
		audience = append(audience, mb.OwnerID)
	}
	return audience
}

// FileReport records a report and sends it to the handler by websocket and push.
func (h *hub) FileReport(room protocol.Room, r store.NewReport) (protocol.RoomReport, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id, fresh, err := h.store.AddReport(r)
	if err != nil {
		return protocol.RoomReport{}, err
	}
	rep, _, err := h.store.RoomReport(id)
	if err != nil {
		return rep, err
	}
	if fresh {
		h.toClients(r.HandlerID, protocol.ClientMessage{Type: "room.report", RoomReport: &rep})
		if h.push != nil {
			h.pushTo(r.HandlerID, push.Notice{Title: room.Name + "에 신고가 들어왔어요", Body: reasonLabel[r.Reason], RoomID: room.ID})
		}
	}
	if r.MessageID != 0 { // reported is seen only by the reporter, so it is resent to that person only
		m, err := h.store.RoomMessage(room.ID, r.MessageID, r.OwnerID)
		if err != nil {
			return rep, err
		}
		h.signRoomMessage(&m)
		h.toClients(r.OwnerID, protocol.ClientMessage{Type: "room.message", RoomMessage: &m})
	}
	return rep, nil
}

// ResolveReport is called only by the handler. Deleting the post happens here; removal is done by the caller through the same path as member removal.
func (h *hub) ResolveReport(handler string, id int64, actions []string) (protocol.RoomReport, []string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rep, owner, err := h.store.RoomReport(id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && owner != handler) {
		return rep, nil, reject(http.StatusNotFound, "report not found")
	}
	if err != nil {
		return rep, nil, err
	}
	if len(rep.Actions) > 0 {
		return rep, nil, reject(http.StatusConflict, "report already handled")
	}
	var files []string
	if slices.Contains(actions, store.ActionDelete) && rep.MessageID != 0 {
		if _, roomOwner, err := h.store.Room(rep.RoomID); err == nil {
			if m, err := h.store.RoomMessage(rep.RoomID, rep.MessageID, ""); err == nil && m.Removed == "" {
				if files, err = h.removeRoomMessage(roomOwner, rep.RoomID, rep.MessageID); err != nil {
					return rep, nil, err
				}
			}
		}
	}
	ids, err := h.store.CloseReports(id, actions)
	if err != nil {
		return rep, files, err
	}
	for _, x := range ids {
		if r, _, err := h.store.RoomReport(x); err == nil {
			h.toClients(handler, protocol.ClientMessage{Type: "room.report", RoomReport: &r})
			if x == id {
				rep = r
			}
		}
	}
	return rep, files, nil
}

// removeMember removes a member from the room and notifies them and the rest; shared by member removal and report handling.
func (s *Server) removeMember(room protocol.Room, roomOwner, member string) error {
	return s.hub.locked(func() error {
		removed, err := s.store.RemoveRoomMember(room.ID, member)
		if err != nil {
			return err
		}
		if !removed {
			return reject(http.StatusNotFound, "member not found")
		}
		room.Shared = true
		s.hub.toClients(member, protocol.ClientMessage{Type: "room.removed", Room: &room})
		if fresh, _, err := s.store.Room(room.ID); err == nil {
			s.hub.toRoom(roomOwner, fresh) // participant list for the remaining people
		}
		return nil
	})
}

func (s *Server) roomSafetyRoutes() {
	m := s.mux

	// delete post: the author deletes their own, the room owner deletes others' (a "deleted post" placeholder remains)
	m.HandleFunc("DELETE /api/rooms/{id}/messages/{mid}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		room, roomOwner, err := s.visibleRoom(r.PathValue("id"), owner)
		if err != nil {
			return err
		}
		mid, err := strconv.ParseInt(r.PathValue("mid"), 10, 64)
		if err != nil {
			return reject(http.StatusNotFound, "message not found in this room")
		}
		files, err := s.hub.DeleteRoomMessage(roomOwner, room.ID, owner, mid)
		if err != nil {
			return err
		}
		s.removeUploadFiles("room post delete", files)
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	// report: only people who can see the room; for a post, its author is the target
	m.HandleFunc("POST /api/rooms/{id}/reports", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		room, roomOwner, err := s.visibleRoom(r.PathValue("id"), owner)
		if err != nil {
			return err
		}
		var b struct {
			MessageID int64  `json:"messageId"`
			TargetID  string `json:"targetId"`
			Reason    string `json:"reason"`
			Note      string `json:"note"`
		}
		if err := decodeJSON(r, &b, 1<<14); err != nil {
			return err
		}
		if !slices.Contains(store.ReportReasons, b.Reason) {
			return reject(http.StatusBadRequest, "reason must be spam, harassment, inappropriate or other")
		}
		note := strings.TrimSpace(b.Note)
		if len([]rune(note)) > reportNoteRunes {
			return reject(http.StatusBadRequest, "note: 300 characters max")
		}
		nr := store.NewReport{OwnerID: owner, RoomID: room.ID, TargetID: b.TargetID, MessageID: b.MessageID, Reason: b.Reason, Note: note}
		if b.MessageID != 0 {
			msg, err := s.store.RoomMessage(room.ID, b.MessageID, owner)
			if errors.Is(err, store.ErrNotFound) || (err == nil && msg.Removed != "") {
				return reject(http.StatusNotFound, "message not found in this room")
			}
			if err != nil {
				return err
			}
			nr.TargetID, nr.MessageText = msg.OwnerID, clipQuote(msg.Text)
		}
		if nr.TargetID == "" {
			return reject(http.StatusBadRequest, "messageId or targetId is required")
		}
		if nr.TargetID == owner {
			return reject(http.StatusBadRequest, "you cannot report yourself")
		}
		if nr.TargetID != roomOwner && !s.store.IsRoomMember(room.ID, nr.TargetID) && b.MessageID == 0 {
			return reject(http.StatusBadRequest, "target is not in this room")
		}
		// a report on the room owner goes to the server owner; if the room owner is the server owner, it stays with them
		nr.HandlerID = roomOwner
		if nr.TargetID == roomOwner {
			nr.HandlerID = s.store.OwnerID()
		}
		rep, err := s.hub.FileReport(room, nr)
		if err != nil {
			return err
		}
		rep.ReporterName = "" // no need to include it even when returning to the reporter
		writeJSON(w, http.StatusCreated, rep)
		return nil
	}))

	m.HandleFunc("GET /api/reports", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		list, err := s.store.Reports(owner)
		if err == nil {
			writeJSON(w, http.StatusOK, list)
		}
		return err
	}))

	// handle: {actions: ["delete", "remove"]} or ["dismiss"]
	m.HandleFunc("POST /api/reports/{id}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			return reject(http.StatusNotFound, "report not found")
		}
		var b struct {
			Actions []string `json:"actions"`
		}
		if err := decodeJSON(r, &b, 1024); err != nil {
			return err
		}
		if !validActions(b.Actions) {
			return reject(http.StatusBadRequest, "actions: delete and/or remove, or dismiss alone")
		}
		before, handler, err := s.store.RoomReport(id)
		if errors.Is(err, store.ErrNotFound) || (err == nil && handler != owner) {
			return reject(http.StatusNotFound, "report not found")
		}
		if err != nil {
			return err
		}
		room, roomOwner, roomErr := s.store.Room(before.RoomID)
		if slices.Contains(b.Actions, store.ActionRemove) {
			if before.TargetID == roomOwner {
				return reject(http.StatusBadRequest, "the room owner cannot be removed from their room")
			}
			if roomErr != nil {
				return reject(http.StatusNotFound, "room not found")
			}
		}
		rep, files, err := s.hub.ResolveReport(owner, id, b.Actions)
		if err != nil {
			return err
		}
		s.removeUploadFiles("report handling", files)
		if slices.Contains(b.Actions, store.ActionRemove) && s.store.IsRoomMember(room.ID, before.TargetID) {
			if err := s.removeMember(room, roomOwner, before.TargetID); err != nil {
				return err
			}
		}
		writeJSON(w, http.StatusOK, rep)
		return nil
	}))

	m.HandleFunc("GET /api/blocks", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		list, err := s.store.Blocked(owner)
		if err == nil {
			writeJSON(w, http.StatusOK, list)
		}
		return err
	}))
	m.HandleFunc("PUT /api/blocks/{id}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		id := r.PathValue("id")
		if id == owner {
			return reject(http.StatusBadRequest, "you cannot block yourself")
		}
		if !s.store.OwnerExists(id) {
			return reject(http.StatusNotFound, "user not found")
		}
		if err := s.store.Block(owner, id); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))
	m.HandleFunc("DELETE /api/blocks/{id}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if err := s.store.Unblock(owner, r.PathValue("id")); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	// terms agreement; version must equal the current one
	m.HandleFunc("POST /api/me/terms", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		var b struct {
			Version string `json:"version"`
		}
		if err := decodeJSON(r, &b, 1024); err != nil {
			return err
		}
		if b.Version != TermsVersion {
			return reject(http.StatusConflict, "terms version changed: "+TermsVersion)
		}
		if err := s.store.AgreeTerms(owner, TermsVersion); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, s.me(owner))
		return nil
	}))
}

func validActions(a []string) bool {
	if len(a) == 1 && a[0] == store.ActionDismiss {
		return true
	}
	if len(a) == 0 || len(a) > 2 {
		return false
	}
	seen := map[string]bool{}
	for _, x := range a {
		if (x != store.ActionDelete && x != store.ActionRemove) || seen[x] {
			return false
		}
		seen[x] = true
	}
	return true
}

func decodeJSON(r *http.Request, v any, limit int64) error {
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, limit)).Decode(v); err != nil {
		return reject(http.StatusBadRequest, "invalid json")
	}
	return nil
}
