package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/store"
)

// clipQuote cuts the quoted text at 300 characters; task quotes and room quotes share the rule.
func clipQuote(s string) string {
	if r := []rune(s); len(r) > 300 {
		return string(r[:300]) + "…"
	}
	return s
}

func validEmoji(emoji string) error {
	if emoji == "" || len(emoji) > 16 {
		return reject(http.StatusBadRequest, "emoji is required (16 bytes max)")
	}
	return nil
}

// PostRoomMessage stores a person-to-person room post and sends it to the room owner and members, not to the agent.
// A non-zero quoteID quotes a person post in the same room.
func (h *hub) PostRoomMessage(roomOwner, roomID, writer, text string, quoteID int64, atts []protocol.Attachment) (protocol.RoomMessage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var quote *protocol.RoomQuote
	if quoteID != 0 {
		q, err := h.store.RoomMessage(roomID, quoteID, "")
		if errors.Is(err, store.ErrNotFound) || (err == nil && q.Removed != "") {
			return protocol.RoomMessage{}, reject(http.StatusBadRequest, "quoted message not found in this room")
		}
		if err != nil {
			return protocol.RoomMessage{}, err
		}
		text := q.Text
		if strings.TrimSpace(text) == "" && len(q.Attachments) > 0 {
			text = q.Attachments[0].Name // a post that had only a file
		}
		quote = &protocol.RoomQuote{ID: quoteID, Text: clipQuote(text)}
	}
	id, err := h.store.AddRoomMessage(roomID, writer, text, quote, atts)
	if errors.Is(err, store.ErrUploadUsed) {
		return protocol.RoomMessage{}, reject(http.StatusBadRequest, "attachment already used in another message")
	}
	if err != nil {
		return protocol.RoomMessage{}, err
	}
	return h.roomTalkChanged(roomOwner, roomID, id, writer)
}

// EditRoomMessage edits only the author's last person post in that room.
func (h *hub) EditRoomMessage(roomOwner, roomID, editor string, id int64, text string) (protocol.RoomMessage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if text == "" {
		return protocol.RoomMessage{}, reject(http.StatusBadRequest, "text is required")
	}
	m, err := h.store.RoomMessage(roomID, id, editor)
	if errors.Is(err, store.ErrNotFound) || (err == nil && m.Removed != "") {
		return m, reject(http.StatusNotFound, "message not found in this room")
	}
	if err != nil {
		return m, err
	}
	if m.OwnerID != editor {
		return m, reject(http.StatusForbidden, "only the writer can edit")
	}
	if h.store.LastRoomMessageOf(roomID, editor) != id {
		return m, reject(http.StatusConflict, "only your last message can be edited")
	}
	if err := h.store.EditRoomMessage(id, text); err != nil {
		return m, err
	}
	return h.roomTalkChanged(roomOwner, roomID, id, editor)
}

// ReactRoomMessage toggles that person's reaction and returns the state afterwards.
func (h *hub) ReactRoomMessage(roomOwner, roomID, who string, id int64, emoji string) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := validEmoji(emoji); err != nil {
		return false, err
	}
	if m, err := h.store.RoomMessage(roomID, id, who); errors.Is(err, store.ErrNotFound) || (err == nil && m.Removed != "") {
		return false, reject(http.StatusNotFound, "message not found in this room")
	} else if err != nil {
		return false, err
	}
	on, err := h.store.ToggleRoomReaction(id, who, emoji)
	if errors.Is(err, store.ErrTooManyReactions) {
		return false, reject(http.StatusBadRequest, fmt.Sprintf("reactions: %d per message max", store.MaxRoomReactions))
	}
	if err != nil {
		return false, err
	}
	_, err = h.roomTalkChanged(roomOwner, roomID, id, who)
	return on, err
}

// roomTalkChanged sends the post to the room owner and members as room.message, built per viewer because a reaction's mine differs by person.
// Returns the post as the viewer sees it. On receiving the same id, the app swaps the post in place.
func (h *hub) roomTalkChanged(roomOwner, roomID string, id int64, viewer string) (protocol.RoomMessage, error) {
	audience := []string{roomOwner}
	for _, mb := range h.roomMembers(roomID) {
		audience = append(audience, mb.OwnerID)
	}
	var mine protocol.RoomMessage
	for _, who := range audience {
		m, err := h.store.RoomMessage(roomID, id, who)
		if err != nil {
			log.Printf("room %s message %d: %v", roomID, id, err)
			continue
		}
		h.signRoomMessage(&m)
		h.toClients(who, protocol.ClientMessage{Type: "room.message", RoomMessage: &m})
		if who == viewer {
			mine = m
		}
	}
	if mine.ID == 0 {
		m, err := h.store.RoomMessage(roomID, id, viewer)
		if err != nil {
			return m, err
		}
		h.signRoomMessage(&m)
		mine = m
	}
	return mine, nil
}

func (h *hub) signRoomMessage(m *protocol.RoomMessage) {
	if h.signFile == nil {
		return
	}
	for i := range m.Attachments {
		m.Attachments[i].URL = h.signFile(m.Attachments[i].ID)
	}
}
