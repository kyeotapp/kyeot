package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"

	"github.com/google/uuid"
)

// CreateOwner creates one more user and returns the owner token. The raw token is not stored.
func (s *Store) CreateOwner(name string) (id, token string, err error) {
	buf := make([]byte, 32)
	rand.Read(buf)
	token = base64.RawURLEncoding.EncodeToString(buf)
	id = uuid.NewString()
	tx, err := s.db.Begin()
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO owners (id, name) VALUES (?, ?)`, id, name); err != nil {
		return "", "", err
	}
	if _, err = tx.Exec(`INSERT INTO owner_tokens (token_hash, owner_id, created_at) VALUES (?, ?, ?)`, hashToken(token), id, now()); err != nil {
		return "", "", err
	}
	return id, token, tx.Commit()
}

// RejoinOwner checks whether the token in use belongs to a non-revoked additional user (the same person invited again). The token is not changed.
// The server owner (including a new owner after handover) is not a target.
func (s *Store) RejoinOwner(old string) (id string, ok bool, err error) {
	if old == "" {
		return "", false, nil
	}
	err = s.db.QueryRow(`SELECT t.owner_id FROM owner_tokens t JOIN owners o ON o.id = t.owner_id WHERE t.token_hash = ? AND o.revoked_at IS NULL`, hashToken(old)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && id == s.OwnerID()) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

// OwnerByToken resolves an additional user's token. The server-config token is not here (it resolves via OwnerID).
func (s *Store) OwnerByToken(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	var id string
	err := s.db.QueryRow(`SELECT owner_id FROM owner_tokens WHERE token_hash = ?`, hashToken(token)).Scan(&id)
	return id, err == nil
}

// Invitee is a user who joined by invitation. The token is not included.
type Invitee struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"createdAt"`
	Admin     bool   `json:"admin"` // admin rights given by the owner: invite people, approve joiners
	Avatar    string `json:"avatar,omitempty"`
}

// IsAdmin reports whether the user is the owner (first user) or a non-revoked user the owner made admin.
func (s *Store) IsAdmin(id string) bool {
	if id == s.OwnerID() {
		return true
	}
	var one int
	return s.db.QueryRow(`SELECT 1 FROM owners WHERE id = ? AND role = 'admin' AND revoked_at IS NULL`, id).Scan(&one) == nil
}

// Admins returns the owner and admins; used when notifying about someone trying to join.
func (s *Store) Admins() []string {
	out := []string{s.OwnerID()}
	rows, err := s.db.Query(`SELECT id FROM owners WHERE role = 'admin' AND revoked_at IS NULL AND id != ?`, out[0])
	if err != nil {
		return out
	}
	more, _ := scanStrings(rows)
	return append(out, more...)
}

// SetAdmin grants or revokes admin rights. The owner cannot be changed. ErrNotFound if the target does not exist.
func (s *Store) SetAdmin(id string, admin bool) error {
	if id == s.OwnerID() {
		return ErrNotFound
	}
	role := ""
	if admin {
		role = "admin"
	}
	res, err := s.db.Exec(`UPDATE owners SET role = ? WHERE id = ? AND revoked_at IS NULL`, role, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Invitees returns non-revoked invited users in join order.
func (s *Store) Invitees() ([]Invitee, error) {
	rows, err := s.db.Query(`SELECT o.id, o.name, min(t.created_at), o.role = 'admin', o.avatar FROM owners o JOIN owner_tokens t ON t.owner_id = o.id
		WHERE o.revoked_at IS NULL AND o.id != ? GROUP BY o.id ORDER BY min(t.created_at), o.id`, s.OwnerID())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invitee{}
	for rows.Next() {
		var v Invitee
		if err := rows.Scan(&v.ID, &v.Name, &v.CreatedAt, &v.Admin, &v.Avatar); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// RevokeOwner cuts an invited user's access: removes tokens, devices, and membership in others' rooms, but keeps that person's records.
// rooms are the room ids they left. ErrNotFound if there is no user to revoke.
func (s *Store) RevokeOwner(id string) (rooms []string, err error) {
	if id == s.OwnerID() {
		return nil, ErrNotFound
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE owners SET revoked_at = ?, role = '' WHERE id = ? AND revoked_at IS NULL`, now(), id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	rows, err := tx.Query(`SELECT room_id FROM room_members WHERE owner_id = ?`, id)
	if err != nil {
		return nil, err
	}
	if rooms, err = scanStrings(rows); err != nil {
		return nil, err
	}
	for _, q := range []string{
		`DELETE FROM owner_tokens WHERE owner_id = ?`,
		`DELETE FROM devices WHERE owner_id = ?`,
		`DELETE FROM room_members WHERE owner_id = ?`,
		`DELETE FROM list_prefs WHERE owner_id = ?1 AND kind = '` + ListRooms + `' AND target_id NOT IN (SELECT id FROM rooms WHERE owner_id = ?1)`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return nil, err
		}
	}
	err = tx.Commit()
	s.membersChanged()
	return rooms, err
}

func (s *Store) OwnerName(id string) string {
	var name string
	logLookup("OwnerName", s.db.QueryRow(`SELECT name FROM owners WHERE id = ?`, id).Scan(&name))
	return name
}

// OwnerAvatar is a person's picture. Empty means the first-letter default color.
func (s *Store) OwnerAvatar(id string) string {
	var v string
	s.db.QueryRow(`SELECT avatar FROM owners WHERE id = ?`, id).Scan(&v)
	return v
}

func (s *Store) SetOwnerAvatar(id, avatar string) error {
	_, err := s.db.Exec(`UPDATE owners SET avatar = ? WHERE id = ?`, avatar, id)
	return err
}

func (s *Store) RenameOwner(id, name string) error {
	_, err := s.db.Exec(`UPDATE owners SET name = ? WHERE id = ?`, name, id)
	return err
}

type Member struct {
	OwnerID string `json:"ownerId"`
	Name    string `json:"name"`
	Role    string `json:"role,omitempty"` // member: human posts only, agent: may also ask the owner's agents
	Avatar  string `json:"avatar,omitempty"`
}

const (
	RoleMember = "member"
	RoleAgent  = "agent"
)

// MemberRole returns the role for a member, else empty.
func (s *Store) MemberRole(roomID, ownerID string) string {
	var role string
	s.db.QueryRow(`SELECT role FROM room_members WHERE room_id = ? AND owner_id = ?`, roomID, ownerID).Scan(&role)
	return role
}

// SetMemberRole returns true if they were a member.
func (s *Store) SetMemberRole(roomID, ownerID, role string) (bool, error) {
	res, err := s.db.Exec(`UPDATE room_members SET role = ? WHERE room_id = ? AND owner_id = ?`, role, roomID, ownerID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CanAskInTask reports whether they are a member who may ask the agent in a room holding that task as posts. The caller checks ownership separately.
func (s *Store) CanAskInTask(taskID, ownerID string) bool {
	var one int
	return s.db.QueryRow(`SELECT 1 FROM room_members WHERE owner_id = ? AND role = 'agent' AND room_id IN (`+roomsOfTask+`) LIMIT 1`, ownerID, taskID).Scan(&one) == nil
}

func (s *Store) AddRoomMember(roomID, ownerID string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO room_members (room_id, owner_id, joined_at) VALUES (?, ?, ?)`, roomID, ownerID, now())
	s.membersChanged()
	return err
}

// RemoveRoomMember returns true if they were a member.
func (s *Store) RemoveRoomMember(roomID, ownerID string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM room_members WHERE room_id = ? AND owner_id = ?`, roomID, ownerID)
	s.membersChanged()
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		if err := forgetListTarget(s.db, ListRooms, roomID, ownerID); err != nil {
			return true, err
		}
	}
	return n > 0, nil
}

func (s *Store) IsRoomMember(roomID, ownerID string) bool {
	var one int
	return s.db.QueryRow(`SELECT 1 FROM room_members WHERE room_id = ? AND owner_id = ?`, roomID, ownerID).Scan(&one) == nil
}

// RoomMembers returns members in join order. The room owner is excluded. Pictures are not included (used by websocket room and /members; photos are heavy).
func (s *Store) RoomMembers(roomID string) ([]Member, error) {
	return s.roomMembers(roomID, false)
}

// RoomPeople is RoomMembers plus person pictures. Only GET /api/rooms/{id}/people uses it.
func (s *Store) RoomPeople(roomID string) ([]Member, error) {
	return s.roomMembers(roomID, true)
}

func (s *Store) roomMembers(roomID string, avatar bool) ([]Member, error) {
	pic := "''"
	if avatar {
		pic = "o.avatar"
	}
	rows, err := s.db.Query(`SELECT m.owner_id, o.name, m.role, `+pic+` FROM room_members m JOIN owners o ON o.id = m.owner_id
		WHERE m.room_id = ? ORDER BY m.joined_at, m.owner_id`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.OwnerID, &m.Name, &m.Role, &m.Avatar); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Rooms holding one task as posts. Applies the same condition as roomFeed, from the task side.
const roomsOfTask = `SELECT r.id FROM rooms r, tasks t WHERE t.id = ? AND r.owner_id = t.owner_id AND t.hidden_at IS NULL
	AND r.owner_id NOT IN (SELECT id FROM owners WHERE revoked_at IS NOT NULL)
	AND r.agent_id IN (SELECT id FROM agents WHERE removed_at IS NULL)
	AND (r.id = t.room_id OR (r.agent_id = t.agent_id AND t.routine_id != ''
	     AND t.routine_id IN (SELECT routine_id FROM room_routines WHERE room_id = r.id)))`

// TaskAudience returns people besides the task owner (ownerID) who must see the task (members of rooms holding it as posts).
// If the owner's rooms have no members at all, the DB is not queried.
func (s *Store) TaskAudience(ownerID, taskID string) ([]string, error) {
	if has, err := s.hasMembers(ownerID); err != nil || !has {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT DISTINCT owner_id FROM room_members WHERE room_id IN (`+roomsOfTask+`)`, taskID)
	if err != nil {
		return nil, err
	}
	return scanStrings(rows)
}

// hasMembers reports whether any of that owner's rooms has a member. If false, TaskAudience for that owner's tasks is empty.
func (s *Store) hasMembers(ownerID string) (bool, error) {
	s.shareMu.Lock()
	v, ok := s.shared[ownerID]
	gen := s.shareGen
	s.shareMu.Unlock()
	if ok {
		return v, nil
	}
	err := s.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM room_members m JOIN rooms r ON r.id = m.room_id WHERE r.owner_id = ?)`, ownerID).Scan(&v)
	if err != nil {
		return true, err
	}
	s.shareMu.Lock()
	if s.shareGen == gen {
		s.shared[ownerID] = v
	}
	s.shareMu.Unlock()
	return v, nil
}

// membersChanged is called after room_members changes (after commit).
func (s *Store) membersChanged() {
	s.shareMu.Lock()
	s.shareGen++
	clear(s.shared)
	s.shareMu.Unlock()
}

// CanSeeTask reports whether they are the owner or a member of a room holding that task as posts.
func (s *Store) CanSeeTask(taskID, ownerID string) (OwnedTask, bool, error) {
	t, err := s.Task(taskID)
	if err != nil {
		return t, false, err
	}
	if t.OwnerID == ownerID {
		return t, true, nil
	}
	var one int
	err = s.db.QueryRow(`SELECT 1 FROM room_members WHERE owner_id = ? AND room_id IN (`+roomsOfTask+`) LIMIT 1`, ownerID, taskID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return t, false, nil
	}
	return t, err == nil, err
}
