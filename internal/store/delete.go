package store

import "database/sql"

// GoneOwner is the author of posts a departed person left in others' rooms; it has no token, so nobody can sign in as it.
const (
	GoneOwner     = "gone"
	GoneOwnerName = "탈퇴한 사용자"
)

// DeletedOwner is what DeleteOwner removed that the caller must clean up after commit.
type DeletedOwner struct {
	Agents  []string // agent connections to drop
	Uploads []string // attachments to delete from disk
	Left    []string // others' rooms they were a member of
}

// DeleteOwner deletes an invited user's account. Posts in others' rooms keep their content with the author changed to GoneOwner.
// ErrNotFound if the user is the owner or does not exist.
func (s *Store) DeleteOwner(id string) (DeletedOwner, error) {
	if id == s.OwnerID() || id == GoneOwner {
		return DeletedOwner{}, ErrNotFound
	}
	tx, err := s.db.Begin()
	if err != nil {
		return DeletedOwner{}, err
	}
	defer tx.Rollback()
	d, err := deleteOwnerTx(tx, id)
	if err != nil {
		return d, err
	}
	if err := tx.Commit(); err != nil {
		return d, err
	}
	s.membersChanged()
	return d, nil
}

// deleteOwnerTx is the body of DeleteOwner; also used when an ownership handover deletes the old owner in the same transaction.
func deleteOwnerTx(tx *sql.Tx, id string) (DeletedOwner, error) {
	var d DeletedOwner
	var err error
	var one int
	if err := tx.QueryRow(`SELECT 1 FROM owners WHERE id = ?`, id).Scan(&one); err == sql.ErrNoRows {
		return d, ErrNotFound
	} else if err != nil {
		return d, err
	}
	list := func(q string) ([]string, error) {
		rows, err := tx.Query(q, id)
		if err != nil {
			return nil, err
		}
		return scanStrings(rows)
	}
	if d.Agents, err = list(`SELECT id FROM agents WHERE owner_id = ?`); err != nil {
		return d, err
	}
	if d.Left, err = list(`SELECT room_id FROM room_members WHERE owner_id = ?`); err != nil {
		return d, err
	}
	const mine = `(SELECT id FROM tasks WHERE owner_id = ?1 OR agent_id IN (SELECT id FROM agents WHERE owner_id = ?1))`
	const myRooms = `(SELECT id FROM rooms WHERE owner_id = ?1)`
	const myAgents = `(SELECT id FROM agents WHERE owner_id = ?1)`
	const myRoomTalk = `(SELECT id FROM room_messages WHERE room_id IN ` + myRooms + `)`
	const othersRoomTalk = `(SELECT id FROM room_messages WHERE room_id NOT IN ` + myRooms + `)`
	// files on conversations to delete and on posts in their own room (including uploads by members), plus files they uploaded that are not attached to others' conversations or room posts
	if d.Uploads, err = list(`SELECT id FROM uploads WHERE task_id IN ` + mine + ` OR room_message_id IN ` + myRoomTalk + `
		OR (owner_id = ?1 AND task_id NOT IN (SELECT id FROM tasks WHERE owner_id != ?1) AND room_message_id NOT IN ` + othersRoomTalk + `)`); err != nil {
		return d, err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO owners (id, name, revoked_at) VALUES (?, ?, 0)`, GoneOwner, GoneOwnerName); err != nil {
		return d, err
	}
	for _, q := range []string{
		// kept: posts in others' rooms, records of calling others' tasks, messages in others' tasks, files attached to others' tasks
		`UPDATE room_messages SET owner_id = '` + GoneOwner + `' WHERE owner_id = ?1 AND room_id NOT IN ` + myRooms,
		`UPDATE tasks SET asked_by = '` + GoneOwner + `' WHERE asked_by = ?1 AND id NOT IN ` + mine,
		`UPDATE events SET payload = json_set(payload, '$.by', '` + GoneOwner + `', '$.byName', '` + GoneOwnerName + `')
			WHERE instr(payload, ?1) > 0 AND json_valid(payload) AND json_extract(payload, '$.by') = ?1 AND task_id NOT IN ` + mine,
		`UPDATE uploads SET owner_id = '` + GoneOwner + `' WHERE owner_id = ?1 AND (task_id IN (SELECT id FROM tasks WHERE owner_id != ?1) OR room_message_id IN ` + othersRoomTalk + `)`,
		// deleted: reactions belong to the person, so ones left in others' rooms go too
		`DELETE FROM uploads WHERE task_id IN ` + mine + ` OR room_message_id IN ` + myRoomTalk + ` OR owner_id = ?1`,
		`DELETE FROM room_message_reactions WHERE owner_id = ?1 OR message_id IN ` + myRoomTalk,
		`DELETE FROM events WHERE task_id IN ` + mine,
		`DELETE FROM tasks WHERE id IN ` + mine,
		`DELETE FROM list_prefs WHERE owner_id = ?1 OR (kind = '` + ListRooms + `' AND target_id IN ` + myRooms + `)`,
		`DELETE FROM list_groups WHERE owner_id = ?1`,
		`DELETE FROM room_members WHERE owner_id = ?1 OR room_id IN ` + myRooms,
		`DELETE FROM room_reports WHERE owner_id = ?1 OR target_id = ?1 OR handler_id = ?1 OR room_id IN ` + myRooms,
		`DELETE FROM blocks WHERE owner_id = ?1 OR blocked_id = ?1`,
		`DELETE FROM room_messages WHERE room_id IN ` + myRooms,
		`DELETE FROM room_routines WHERE room_id IN ` + myRooms,
		`DELETE FROM rooms WHERE owner_id = ?1`,
		`DELETE FROM ui_screens WHERE agent_id IN ` + myAgents,
		`DELETE FROM ui_parts WHERE agent_id IN ` + myAgents,
		`DELETE FROM ui_layouts WHERE agent_id IN ` + myAgents,
		`DELETE FROM ui_data WHERE agent_id IN ` + myAgents,
		`DELETE FROM agent_routines WHERE agent_id IN ` + myAgents,
		`DELETE FROM routine_history WHERE agent_id IN ` + myAgents,
		`DELETE FROM agents WHERE owner_id = ?1`,
		`DELETE FROM devices WHERE owner_id = ?1`,
		`DELETE FROM owner_tokens WHERE owner_id = ?1`,
		`DELETE FROM owners WHERE id = ?1`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return d, err
		}
	}
	return d, nil
}
