package store

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/wordfilter"
)

// RoomMessage.Removed values
const (
	RemovedByOwner = "owner"
	RemovedBySelf  = "self"
)

// report reasons
var ReportReasons = []string{"spam", "harassment", "inappropriate", "other"}

// report actions
const (
	ActionDelete  = "delete"
	ActionRemove  = "remove"
	ActionDismiss = "dismiss"
)

// markForViewer tags posts with block, report, and word-filter marks relative to the viewer. The original text is kept (the app expands it via "view").
func (s *Store) markForViewer(out []RoomMessage, roomID, viewer string, lo, hi int64) error {
	blocked := map[string]bool{}
	reported := map[int64]bool{}
	if viewer != "" {
		rows, err := s.db.Query(`SELECT blocked_id FROM blocks WHERE owner_id = ?`, viewer)
		if err != nil {
			return err
		}
		ids, err := scanStrings(rows)
		if err != nil {
			return err
		}
		for _, id := range ids {
			blocked[id] = true
		}
		r, err := s.db.Query(`SELECT DISTINCT message_id FROM room_reports WHERE owner_id = ? AND room_id = ? AND message_id BETWEEN ? AND ?`, viewer, roomID, lo, hi)
		if err != nil {
			return err
		}
		for r.Next() {
			var id int64
			if err := r.Scan(&id); err != nil {
				r.Close()
				return err
			}
			reported[id] = true
		}
		r.Close()
		if err := r.Err(); err != nil {
			return err
		}
	}
	filterOn := s.RoomFilterOn(roomID)
	for i := range out {
		m := &out[i]
		if m.Removed != "" {
			continue
		}
		m.Blocked = blocked[m.OwnerID]
		m.Reported = reported[m.ID]
		m.Filtered = filterOn && m.OwnerID != viewer && wordfilter.Match(m.Text)
		if filterOn && m.Quote != nil && wordfilter.Match(m.Quote.Text) {
			m.Quote.Text = "…" // so the original does not leak through a quote
		}
	}
	return nil
}

// RoomFilterOn reports whether word masking is on for that room. New rooms start with it on.
func (s *Store) RoomFilterOn(roomID string) bool {
	var off int
	s.db.QueryRow(`SELECT filter_off FROM rooms WHERE id = ?`, roomID).Scan(&off)
	return off == 0
}

func (s *Store) SetRoomFilter(roomID string, on bool) (protocol.Room, error) {
	off := 1
	if on {
		off = 0
	}
	if _, err := s.db.Exec(`UPDATE rooms SET filter_off = ? WHERE id = ?`, off, roomID); err != nil {
		return protocol.Room{}, err
	}
	r, _, err := s.Room(roomID)
	return r, err
}

// DeleteRoomMessage removes a post deleted by its author, row and all. The caller deletes the files of the returned attachment ids.
func (s *Store) DeleteRoomMessage(id int64) ([]string, error) {
	return s.dropRoomMessage(id, `DELETE FROM room_messages WHERE id = ?1`)
}

// RemoveRoomMessage keeps a post removed by the room owner as a "removed post" placeholder and deletes its content, attachments, and reactions.
func (s *Store) RemoveRoomMessage(id int64) ([]string, error) {
	return s.dropRoomMessage(id, `UPDATE room_messages SET text = '', attachments = '[]', quote_id = 0, quote_text = '', removed_at = ?2 WHERE id = ?1`)
}

func (s *Store) dropRoomMessage(id int64, last string) ([]string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id FROM uploads WHERE room_message_id = ?`, id)
	if err != nil {
		return nil, err
	}
	files, err := scanStrings(rows)
	if err != nil {
		return nil, err
	}
	for _, q := range []string{
		`DELETE FROM uploads WHERE room_message_id = ?`,
		`DELETE FROM room_message_reactions WHERE message_id = ?`,
		`UPDATE room_messages SET quote_text = '' WHERE quote_id = ?`, // so the original does not remain in quoting posts
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return nil, err
		}
	}
	args := []any{id}
	if strings.Contains(last, "?2") {
		args = append(args, now())
	}
	if _, err := tx.Exec(last, args...); err != nil {
		return nil, err
	}
	return files, tx.Commit()
}

// Block is a server-wide block. Blocking the same person again keeps the first time.
func (s *Store) Block(ownerID, blockedID string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO blocks (owner_id, blocked_id, created_at) VALUES (?, ?, ?)`, ownerID, blockedID, now())
	return err
}

func (s *Store) Unblock(ownerID, blockedID string) error {
	_, err := s.db.Exec(`DELETE FROM blocks WHERE owner_id = ? AND blocked_id = ?`, ownerID, blockedID)
	return err
}

// Blocked returns the people blocked, newest first.
func (s *Store) Blocked(ownerID string) ([]protocol.BlockedPerson, error) {
	rows, err := s.db.Query(`SELECT b.blocked_id, o.name, o.avatar, b.created_at FROM blocks b JOIN owners o ON o.id = b.blocked_id
		WHERE b.owner_id = ? ORDER BY b.created_at DESC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.BlockedPerson{}
	for rows.Next() {
		var b protocol.BlockedPerson
		if err := rows.Scan(&b.OwnerID, &b.Name, &b.Avatar, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// NewReport is one report. OwnerID is the reporter.
type NewReport struct {
	OwnerID, RoomID, HandlerID, TargetID string
	MessageID                            int64
	MessageText, Reason, Note            string
}

// AddReport files a report. If the same person already has an open report on the same post (or the same person, for a person report), returns its id instead of creating a new one.
func (s *Store) AddReport(r NewReport) (id int64, fresh bool, err error) {
	err = s.db.QueryRow(`SELECT id FROM room_reports WHERE owner_id = ? AND room_id = ? AND message_id = ? AND target_id = ? AND actions = ''`, r.OwnerID, r.RoomID, r.MessageID, r.TargetID).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	res, err := s.db.Exec(`INSERT INTO room_reports (owner_id, room_id, handler_id, target_id, message_id, message_text, reason, note, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.OwnerID, r.RoomID, r.HandlerID, r.TargetID, r.MessageID, r.MessageText, r.Reason, r.Note, now())
	if err != nil {
		return 0, false, err
	}
	id, _ = res.LastInsertId()
	return id, true, nil
}

const reportCols = `r.id, r.room_id, coalesce(rm.name, ''), r.target_id, coalesce(t.name, ''), CASE WHEN r.target_id = r.handler_id THEN '' ELSE coalesce(o.name, '') END, coalesce(r.target_id = rm.owner_id, 0), r.message_id, r.message_text, r.reason, r.note, r.created_at, r.actions, coalesce(r.handled_at, 0), r.handler_id`
const reportFrom = ` FROM room_reports r LEFT JOIN rooms rm ON rm.id = r.room_id LEFT JOIN owners t ON t.id = r.target_id LEFT JOIN owners o ON o.id = r.owner_id`

func scanReports(rows *sql.Rows) ([]protocol.RoomReport, []string, error) {
	defer rows.Close()
	out := []protocol.RoomReport{}
	var handlers []string
	for rows.Next() {
		var r protocol.RoomReport
		var actions, handler string
		if err := rows.Scan(&r.ID, &r.RoomID, &r.RoomName, &r.TargetID, &r.TargetName, &r.ReporterName, &r.TargetOwner, &r.MessageID, &r.MessageText, &r.Reason, &r.Note, &r.CreatedAt, &actions, &r.HandledAt, &handler); err != nil {
			return nil, nil, err
		}
		r.Actions = []string{}
		if actions != "" {
			r.Actions = strings.Split(actions, ",")
		}
		out = append(out, r)
		handlers = append(handlers, handler)
	}
	return out, handlers, rows.Err()
}

// Reports returns the reports that person must handle: open ones first, then newest first. Latest 200.
func (s *Store) Reports(handlerID string) ([]protocol.RoomReport, error) {
	rows, err := s.db.Query(`SELECT `+reportCols+reportFrom+` WHERE r.handler_id = ? ORDER BY r.actions != '', r.id DESC LIMIT 200`, handlerID)
	if err != nil {
		return nil, err
	}
	out, _, err := scanReports(rows)
	return out, err
}

// RoomReport is one report and the person who handles it.
func (s *Store) RoomReport(id int64) (protocol.RoomReport, string, error) {
	rows, err := s.db.Query(`SELECT `+reportCols+reportFrom+` WHERE r.id = ?`, id)
	if err != nil {
		return protocol.RoomReport{}, "", err
	}
	out, handlers, err := scanReports(rows)
	if err != nil {
		return protocol.RoomReport{}, "", err
	}
	if len(out) == 0 {
		return protocol.RoomReport{}, "", ErrNotFound
	}
	return out[0], handlers[0], nil
}

// CloseReports closes that report together with the same handler's open reports on the same post. Returns the closed ids.
func (s *Store) CloseReports(id int64, actions []string) ([]int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var msg int64
	var room, handler string
	if err := tx.QueryRow(`SELECT message_id, room_id, handler_id FROM room_reports WHERE id = ?`, id).Scan(&msg, &room, &handler); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	rows, err := tx.Query(`SELECT id FROM room_reports WHERE actions = '' AND (id = ? OR (message_id != 0 AND message_id = ? AND room_id = ? AND handler_id = ?))`, id, msg, room, handler)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var x int64
		if err := rows.Scan(&x); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, x)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	at := now()
	for _, x := range ids {
		if _, err := tx.Exec(`UPDATE room_reports SET actions = ?, handled_at = ?, message_text = '' WHERE id = ?`, strings.Join(actions, ","), at, x); err != nil { // closed reports keep no original text
			return nil, err
		}
	}
	return ids, tx.Commit()
}

// AgreeTerms records agreement to the terms. Asked again when the version changes.
func (s *Store) AgreeTerms(ownerID, version string) error {
	_, err := s.db.Exec(`UPDATE owners SET terms_version = ?, terms_at = ? WHERE id = ?`, version, now(), ownerID)
	return err
}

// TermsVersion is the version that person last agreed to; "" if none.
func (s *Store) TermsVersion(ownerID string) string {
	var v string
	s.db.QueryRow(`SELECT terms_version FROM owners WHERE id = ?`, ownerID).Scan(&v)
	return v
}

// HasRoomMembers reports whether the room has any member.
func (s *Store) HasRoomMembers(roomID string) bool {
	var one int
	return s.db.QueryRow(`SELECT 1 FROM room_members WHERE room_id = ? LIMIT 1`, roomID).Scan(&one) == nil
}

// OwnerExists reports whether the user is non-revoked.
func (s *Store) OwnerExists(id string) bool {
	var one int
	return id != GoneOwner && s.db.QueryRow(`SELECT 1 FROM owners WHERE id = ? AND revoked_at IS NULL`, id).Scan(&one) == nil
}
