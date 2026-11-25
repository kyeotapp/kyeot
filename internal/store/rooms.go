package store

import (
	"database/sql"
	"errors"
	"slices"

	"github.com/google/uuid"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

func (s *Store) CreateRoom(ownerID, name, agentID string, routineIDs []string) (protocol.Room, error) {
	id := uuid.NewString()
	tx, err := s.db.Begin()
	if err != nil {
		return protocol.Room{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO rooms (id, owner_id, name, agent_id, routine_id, created_at) VALUES (?, ?, ?, ?, '', ?)`,
		id, ownerID, name, agentID, now()); err != nil {
		return protocol.Room{}, err
	}
	if err := setRoomRoutines(tx, id, routineIDs); err != nil {
		return protocol.Room{}, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.Room{}, err
	}
	r, _, err := s.Room(id)
	return r, err
}

// SetRoomRoutines replaces the whole list of routines that send results to the room.
func (s *Store) SetRoomRoutines(roomID string, routineIDs []string) (protocol.Room, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return protocol.Room{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM room_routines WHERE room_id = ?`, roomID); err != nil {
		return protocol.Room{}, err
	}
	if err := setRoomRoutines(tx, roomID, routineIDs); err != nil {
		return protocol.Room{}, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.Room{}, err
	}
	r, _, err := s.Room(roomID)
	return r, err
}

// setRoomRoutines stores the list and sets the legacy column rooms.routine_id to the first routine (for old apps and migration).
func setRoomRoutines(tx *sql.Tx, roomID string, routineIDs []string) error {
	first := ""
	for i, rid := range routineIDs {
		if i == 0 {
			first = rid
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO room_routines (room_id, routine_id, position) VALUES (?, ?, ?)`, roomID, rid, i); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`UPDATE rooms SET routine_id = ? WHERE id = ?`, first, roomID)
	return err
}

// DeleteRoom deletes the room, its human posts, and their reaction and attachment rows. Conversation history accumulated in the room stays in the DB.
// The caller deletes the files of the returned attachment ids from disk.
func (s *Store) DeleteRoom(id string) ([]string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	files, err := roomMessageUploads(tx, `(?)`, id)
	if err != nil {
		return nil, err
	}
	for _, q := range []string{
		`DELETE FROM uploads WHERE room_message_id IN (SELECT id FROM room_messages WHERE room_id = ?)`,
		`DELETE FROM room_message_reactions WHERE message_id IN (SELECT id FROM room_messages WHERE room_id = ?)`,
		`DELETE FROM room_members WHERE room_id = ?`,
		`DELETE FROM room_reports WHERE room_id = ?`,
		`DELETE FROM room_messages WHERE room_id = ?`,
		`DELETE FROM room_routines WHERE room_id = ?`,
		`DELETE FROM rooms WHERE id = ?`,
		`DELETE FROM list_prefs WHERE kind = '` + ListRooms + `' AND target_id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return nil, err
		}
	}
	err = tx.Commit()
	s.membersChanged()
	if err != nil {
		return nil, err
	}
	return files, nil
}

func (s *Store) RenameRoom(id, name string) (protocol.Room, error) {
	if _, err := s.db.Exec(`UPDATE rooms SET name = ? WHERE id = ?`, name, id); err != nil {
		return protocol.Room{}, err
	}
	r, _, err := s.Room(id)
	return r, err
}

func (s *Store) scanRoom(row interface{ Scan(...any) error }) (protocol.Room, string, error) {
	var r protocol.Room
	var owner string
	var filterOff int
	err := row.Scan(&r.ID, &r.Name, &r.AgentID, &r.RoutineID, &r.CreatedAt, &owner, &filterOff)
	r.FilterOff = filterOff != 0
	if errors.Is(err, sql.ErrNoRows) {
		return r, "", ErrNotFound
	}
	if err != nil {
		return r, "", err
	}
	rows, err := s.db.Query(`SELECT routine_id FROM room_routines WHERE room_id = ? ORDER BY position, routine_id`, r.ID)
	if err != nil {
		return r, "", err
	}
	if r.RoutineIDs, err = scanStrings(rows); err != nil {
		return r, "", err
	}
	if r.RoutineIDs == nil {
		r.RoutineIDs = []string{}
	}
	r.RoutineID = ""
	if len(r.RoutineIDs) > 0 {
		r.RoutineID = r.RoutineIDs[0]
	}
	var last sql.NullInt64
	s.db.QueryRow(`SELECT max(updated_at) FROM tasks WHERE `+roomFeed, owner, r.ID, r.AgentID, r.ID).Scan(&last)
	r.LastAt = max(last.Int64, r.CreatedAt)
	return r, owner, nil
}

// Room treats rooms of unregistered agents and revoked owners as nonexistent (same as Rooms).
func (s *Store) Room(id string) (protocol.Room, string, error) {
	return s.scanRoom(s.db.QueryRow(`SELECT id, name, agent_id, routine_id, created_at, owner_id, filter_off FROM rooms WHERE id = ? AND `+liveRoom, id))
}

// Rooms returns my rooms and others' rooms I joined. Others' rooms have Shared set.
func (s *Store) Rooms(ownerID string) ([]protocol.Room, error) {
	rows, err := s.db.Query(`SELECT id FROM rooms WHERE (owner_id = ? OR id IN (SELECT room_id FROM room_members WHERE owner_id = ?))
		AND `+liveRoom+` ORDER BY created_at`, ownerID, ownerID)
	if err != nil {
		return nil, err
	}
	ids, err := scanStrings(rows)
	if err != nil {
		return nil, err
	}
	prefs, err := s.listPrefMap(ownerID, ListRooms)
	if err != nil {
		return nil, err
	}
	out := []protocol.Room{}
	for _, id := range ids {
		r, owner, err := s.Room(id)
		if err != nil {
			return nil, err
		}
		if r.Shared = owner != ownerID; r.Shared {
			r.OwnerName, r.CanAsk = s.OwnerName(owner), s.MemberRole(id, ownerID) == RoleAgent
		}
		r.Pinned, r.GroupID = prefs[id].Pinned, prefs[id].GroupID
		out = append(out, r)
	}
	return out, nil
}

type RoomPost struct {
	Task    protocol.Task `json:"task"`
	Preview string        `json:"preview"`
	First   string        `json:"first,omitempty"` // first agent reply; the thread card shows this (old apps use preview)
	Replies int           `json:"replies"`         // number of messages after the first post
}

// RoomPosts returns the latest limit posts, oldest first. Preview is the last agent reply, First the first agent reply, Replies the thread reply count, Task.Question the first user post.
func (s *Store) RoomPosts(room protocol.Room, ownerID string, limit int) ([]RoomPost, error) {
	rows, err := s.db.Query(`SELECT `+taskCols+`,
		  coalesce((SELECT json_extract(payload, '$.text') FROM events e
		            WHERE e.task_id = tasks.id AND e.source = 'agent' AND e.type = 'message' ORDER BY e.id DESC LIMIT 1), ''),
		  max(0, (SELECT count(*) FROM events e WHERE e.task_id = tasks.id
		            AND ((e.source = 'agent' AND e.type = 'message') OR (e.source = 'user' AND e.type = 'user_message')))
		         - (CASE WHEN tasks.routine_id <> '' THEN 1 ELSE 0 END)),
		  coalesce((SELECT substr(json_extract(payload, '$.text'), 1, 400) FROM events e
		            WHERE e.task_id = tasks.id AND e.type = 'task.start' ORDER BY e.id LIMIT 1), ''),
		  coalesce((SELECT json_extract(payload, '$.text') FROM events e
		            WHERE e.task_id = tasks.id AND e.source = 'agent' AND e.type = 'message' ORDER BY e.id LIMIT 1), '')
		FROM tasks WHERE `+roomFeed+` ORDER BY created_at DESC LIMIT ?`,
		ownerID, room.ID, room.AgentID, room.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RoomPost{}
	for rows.Next() {
		var t OwnedTask
		var p RoomPost
		if err := rows.Scan(&t.ID, &t.AgentID, &t.Title, &t.State, &t.CreatedAt, &t.UpdatedAt, &t.OwnerID, &t.RoomID, &t.RoutineID, &t.AskedBy, &p.Preview, &p.Replies, &t.Question, &p.First); err != nil {
			return nil, err
		}
		p.Task = t.Task
		out = append(out, p)
	}
	slices.Reverse(out)
	return out, rows.Err()
}
