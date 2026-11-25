package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

type RoomMessage = protocol.RoomMessage

const roomMessageCols = `m.id, m.room_id, m.owner_id, o.name, m.text, m.created_at, m.quote_id, m.quote_text, m.edited_at IS NOT NULL, m.attachments, m.removed_at IS NOT NULL`

// ErrUploadUsed means an attempt to attach a file already attached to another post.
var ErrUploadUsed = errors.New("upload already attached")

// ErrTooManyReactions means a person exceeded the number of reactions allowed on one post.
var ErrTooManyReactions = errors.New("too many reactions")

// MaxRoomReactions is the number of distinct reactions one person may set on a room post.
const MaxRoomReactions = 8

// AddRoomMessage stores a room human post and binds the attached files to it. ErrUploadUsed if a file is already attached to another post.
func (s *Store) AddRoomMessage(roomID, ownerID, text string, quote *protocol.RoomQuote, atts []protocol.Attachment) (int64, error) {
	var quoteID int64
	var quoteText string
	if quote != nil {
		quoteID, quoteText = quote.ID, quote.Text
	}
	stored := make([]protocol.Attachment, len(atts))
	for i, a := range atts {
		a.URL = "" // the URL is signed anew on every export
		stored[i] = a
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	at := now()
	res, err := tx.Exec(`INSERT INTO room_messages (room_id, owner_id, text, created_at, quote_id, quote_text, attachments) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		roomID, ownerID, text, at, quoteID, quoteText, string(raw))
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	for _, a := range atts {
		res, err := tx.Exec(`UPDATE uploads SET used_at = ?, room_message_id = ? WHERE id = ? AND used_at IS NULL`, at, id, a.ID)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return 0, ErrUploadUsed
		}
	}
	return id, tx.Commit()
}

// RoomMessage is one post of that room. Reaction mine is relative to viewer. ErrNotFound if it is another room's post.
func (s *Store) RoomMessage(roomID string, id int64, viewer string) (RoomMessage, error) {
	rows, err := s.db.Query(`SELECT `+roomMessageCols+` FROM room_messages m JOIN owners o ON o.id = m.owner_id
		WHERE m.room_id = ? AND m.id = ?`, roomID, id)
	if err != nil {
		return RoomMessage{}, err
	}
	out, err := s.scanRoomMessages(rows, roomID, viewer)
	if err != nil {
		return RoomMessage{}, err
	}
	if len(out) == 0 {
		return RoomMessage{}, ErrNotFound
	}
	return out[0], nil
}

// RoomMessages returns the latest limit posts, oldest first. Reaction mine is relative to viewer.
func (s *Store) RoomMessages(roomID string, limit int, viewer string) ([]RoomMessage, error) {
	rows, err := s.db.Query(`SELECT `+roomMessageCols+` FROM room_messages m JOIN owners o ON o.id = m.owner_id
		WHERE m.room_id = ? ORDER BY m.id DESC LIMIT ?`, roomID, limit)
	if err != nil {
		return nil, err
	}
	out, err := s.scanRoomMessages(rows, roomID, viewer)
	slices.Reverse(out)
	return out, err
}

func (s *Store) scanRoomMessages(rows *sql.Rows, roomID, viewer string) ([]RoomMessage, error) {
	out, err := scanRoomRows(rows) // there is one connection, so close before the next query
	if err != nil || len(out) == 0 {
		return out, err
	}
	lo, hi := out[0].ID, out[0].ID
	at := map[int64]int{}
	for i, m := range out {
		lo, hi = min(lo, m.ID), max(hi, m.ID)
		at[m.ID] = i
	}
	if err := s.markForViewer(out, roomID, viewer, lo, hi); err != nil {
		return out, err
	}
	r, err := s.db.Query(`SELECT x.message_id, x.emoji, count(*), max(x.owner_id = ?) FROM room_message_reactions x JOIN room_messages m ON m.id = x.message_id
		WHERE m.room_id = ? AND x.message_id BETWEEN ? AND ? GROUP BY x.message_id, x.emoji ORDER BY min(x.rowid)`, viewer, roomID, lo, hi)
	if err != nil {
		return out, err
	}
	defer r.Close()
	for r.Next() {
		var id int64
		var x protocol.RoomReaction
		if err := r.Scan(&id, &x.Emoji, &x.Count, &x.Mine); err != nil {
			return out, err
		}
		if i, ok := at[id]; ok {
			out[i].Reactions = append(out[i].Reactions, x)
		}
	}
	return out, r.Err()
}

func scanRoomRows(rows *sql.Rows) ([]RoomMessage, error) {
	defer rows.Close()
	out := []RoomMessage{}
	for rows.Next() {
		var m RoomMessage
		var quoteID int64
		var quoteText, atts string
		var removed bool
		if err := rows.Scan(&m.ID, &m.RoomID, &m.OwnerID, &m.Name, &m.Text, &m.CreatedAt, &quoteID, &quoteText, &m.Edited, &atts, &removed); err != nil {
			return nil, err
		}
		if removed {
			m.Removed = RemovedByOwner
		}
		if quoteID != 0 {
			m.Quote = &protocol.RoomQuote{ID: quoteID, Text: quoteText}
		}
		if atts != "[]" {
			json.Unmarshal([]byte(atts), &m.Attachments)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// LastRoomMessageOf is the id of the last human post that person wrote in that room; 0 if none.
func (s *Store) LastRoomMessageOf(roomID, ownerID string) int64 {
	var id sql.NullInt64
	s.db.QueryRow(`SELECT max(id) FROM room_messages WHERE room_id = ? AND owner_id = ?`, roomID, ownerID).Scan(&id)
	return id.Int64
}

func (s *Store) EditRoomMessage(id int64, text string) error {
	_, err := s.db.Exec(`UPDATE room_messages SET text = ?, edited_at = ? WHERE id = ?`, text, now(), id)
	return err
}

// ToggleRoomReaction toggles that person's reaction; true if on afterwards. ErrTooManyReactions if no slot is left.
func (s *Store) ToggleRoomReaction(messageID int64, ownerID, emoji string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM room_message_reactions WHERE message_id = ? AND owner_id = ? AND emoji = ?`, messageID, ownerID, emoji)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return false, nil
	}
	res, err = s.db.Exec(`INSERT INTO room_message_reactions (message_id, owner_id, emoji)
		SELECT ?1, ?2, ?3 WHERE (SELECT count(*) FROM room_message_reactions WHERE message_id = ?1 AND owner_id = ?2) < ?4`, messageID, ownerID, emoji, MaxRoomReactions)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, ErrTooManyReactions
	}
	return true, nil
}

// roomMessageUploads returns ids of files attached to human posts of those rooms. rooms is SQL selecting room_id.
func roomMessageUploads(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, rooms string, args ...any) ([]string, error) {
	rows, err := q.Query(`SELECT id FROM uploads WHERE room_message_id IN (SELECT id FROM room_messages WHERE room_id IN `+rooms+`)`, args...)
	if err != nil {
		return nil, err
	}
	return scanStrings(rows)
}
