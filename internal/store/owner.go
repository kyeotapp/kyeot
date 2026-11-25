package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/google/uuid"
)

// Server owner seat and server closure. The owner id is data in server_state, not a fixed value.

const (
	stateOwner        = "owner_id"       // Owner if empty
	stateHost         = "host_owner"     // computer owner account entered via the server-config token. Owner if unset; empty means handed over and cut off
	stateHostToken    = "handover_token" // hash of the token written to file at handover. Started with another token means pair --rotate
	stateHandoverTo   = "handover_to"    // person who took over and has not seen the notice yet
	stateHandoverFrom = "handover_from"  // name of the person who handed over
	stateClosed       = "closed"         // "1" means the server was wiped and closed
	stateServerID     = "server_id"      // created once on first start, never changed
)

// ErrClosed means the server was wiped and closed.
var ErrClosed = errors.New("server closed")

func readState(db dbtx, key string) (string, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM server_state WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func writeState(db dbtx, key, value string) error {
	_, err := db.Exec(`INSERT INTO server_state (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// ensureServerID reads this server's unique id, creating it if absent.
func ensureServerID(db dbtx) (string, error) {
	id, err := readState(db, stateServerID)
	if err != nil || id != "" {
		return id, err
	}
	b := make([]byte, 16)
	rand.Read(b)
	id = hex.EncodeToString(b)
	return id, writeState(db, stateServerID, id)
}

// ServerID is this server's unique id.
func (s *Store) ServerID() string { return s.serverID }

// OwnerID is the current server owner.
func (s *Store) OwnerID() string { return s.owner.Load().(string) }

// HostOwner is the account the server-config owner token resolves to. After a handover and before rotate it is "", meaning nobody.
func (s *Store) HostOwner() string { return s.host.Load().(string) }

func (s *Store) loadOwners() error {
	owner, err := readState(s.db, stateOwner)
	if err != nil {
		return err
	}
	host, err := readState(s.db, stateHost)
	if err != nil {
		return err
	}
	if owner == "" {
		owner, host = Owner, Owner
	}
	s.owner.Store(owner)
	s.host.Store(host)
	return nil
}

// Closed reports whether the server was wiped and closed.
func (s *Store) Closed() bool {
	v, err := readState(s.db, stateClosed)
	logLookup("Closed", err)
	return v == "1"
}

// UserSummary is one person's record counts shown before deletion.
type UserSummary struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Admin bool   `json:"admin"`
	Rooms int    `json:"rooms"`
	Tasks int    `json:"tasks"`
}

// Others returns non-revoked users except the owner: admins first, then join order.
func (s *Store) Others() ([]UserSummary, error) {
	list, err := s.Invitees()
	if err != nil {
		return nil, err
	}
	out := make([]UserSummary, 0, len(list))
	for _, v := range list {
		u := UserSummary{ID: v.ID, Name: v.Name, Admin: v.Admin}
		u.Rooms, u.Tasks = s.counts(v.ID)
		out = append(out, u)
	}
	admins := out[:0:0]
	rest := []UserSummary{}
	for _, u := range out {
		if u.Admin {
			admins = append(admins, u)
		} else {
			rest = append(rest, u)
		}
	}
	return append(admins, rest...), nil
}

// counts returns the number of rooms they created and non-hidden conversations.
func (s *Store) counts(id string) (rooms, tasks int) {
	s.db.QueryRow(`SELECT count(*) FROM rooms WHERE owner_id = ?`, id).Scan(&rooms)
	s.db.QueryRow(`SELECT count(*) FROM tasks WHERE owner_id = ? AND hidden_at IS NULL`, id).Scan(&tasks)
	return
}

// MineSummary is the count of my records that would be deleted.
type MineSummary struct {
	Agents int `json:"agents"`
	Rooms  int `json:"rooms"`
	Tasks  int `json:"tasks"`
}

func (s *Store) Mine(id string) MineSummary {
	var m MineSummary
	s.db.QueryRow(`SELECT count(*) FROM agents WHERE owner_id = ? AND removed_at IS NULL`, id).Scan(&m.Agents)
	m.Rooms, m.Tasks = s.counts(id)
	return m
}

// HandOver gives the owner seat to heir and deletes the old owner's account, in one transaction.
// hostToken is the token newly written to the server config file; it resolves to no account.
// ErrNotFound if heir is the owner, does not exist, or is revoked.
func (s *Store) HandOver(heir, hostToken string) (DeletedOwner, error) {
	old := s.OwnerID()
	if heir == old || heir == GoneOwner || heir == "" {
		return DeletedOwner{}, ErrNotFound
	}
	tx, err := s.db.Begin()
	if err != nil {
		return DeletedOwner{}, err
	}
	defer tx.Rollback()
	var one int
	if err := tx.QueryRow(`SELECT 1 FROM owners o WHERE id = ? AND revoked_at IS NULL AND EXISTS (SELECT 1 FROM owner_tokens WHERE owner_id = o.id)`, heir).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return DeletedOwner{}, ErrNotFound
	} else if err != nil {
		return DeletedOwner{}, err
	}
	var oldName string
	tx.QueryRow(`SELECT name FROM owners WHERE id = ?`, old).Scan(&oldName)
	for _, kv := range [][2]string{{stateOwner, heir}, {stateHost, ""}, {stateHostToken, hashToken(hostToken)}, {stateHandoverTo, heir}, {stateHandoverFrom, oldName}} {
		if err := writeState(tx, kv[0], kv[1]); err != nil {
			return DeletedOwner{}, err
		}
	}
	if _, err := tx.Exec(`UPDATE owners SET role = '' WHERE id = ?`, heir); err != nil { // the owner is an admin without a role
		return DeletedOwner{}, err
	}
	d, err := deleteOwnerTx(tx, old)
	if err != nil {
		return d, err
	}
	if err := tx.Commit(); err != nil {
		return d, err
	}
	s.owner.Store(heir)
	s.host.Store("")
	s.membersChanged()
	return d, nil
}

// Reclaim creates a new owner account on the computer if the server-config token changed after a handover (pair --rotate).
// The current owner steps down to admin; account, conversations, and token are kept. If created, returns the id of the person who was owner.
func (s *Store) Reclaim(hostToken, name string) (demoted string, err error) {
	if s.HostOwner() != "" {
		return "", nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	given, err := readState(tx, stateHostToken)
	if err != nil || given == hashToken(hostToken) {
		return "", err
	}
	demoted = s.OwnerID()
	id := uuid.NewString()
	if _, err := tx.Exec(`INSERT INTO owners (id, name) VALUES (?, ?)`, id, name); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`UPDATE owners SET role = 'admin' WHERE id = ? AND revoked_at IS NULL`, demoted); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`DELETE FROM server_state WHERE key IN (?, ?)`, stateHostToken, stateHandoverTo); err != nil {
		return "", err
	}
	for _, kv := range [][2]string{{stateOwner, id}, {stateHost, id}} {
		if err := writeState(tx, kv[0], kv[1]); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	s.owner.Store(id)
	s.host.Store(id)
	s.membersChanged()
	return demoted, nil
}

// HandoverNotice returns the name of the person who handed over, if id took over as owner and has not seen the notice.
func (s *Store) HandoverNotice(id string) (from string, ok bool) {
	to, _ := readState(s.db, stateHandoverTo)
	if to == "" || to != id {
		return "", false
	}
	from, _ = readState(s.db, stateHandoverFrom)
	return from, true
}

// HandoverSeen records that the successor has seen the notice.
func (s *Store) HandoverSeen(id string) error {
	_, err := s.db.Exec(`DELETE FROM server_state WHERE key = ? AND value = ?`, stateHandoverTo, id)
	return err
}

// HandedOver reports whether this computer's account is not the owner (after handover, before rotate).
func (s *Store) HandedOver() bool { return s.HostOwner() != s.OwnerID() }

// WipeAll deletes everyone's records and leaves the server closed. Returns attachment ids to delete from disk.
func (s *Store) WipeAll() (uploads []string, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id FROM uploads`)
	if err != nil {
		return nil, err
	}
	if uploads, err = scanStrings(rows); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`PRAGMA defer_foreign_keys = ON`); err != nil {
		return nil, err
	}
	rows, err = tx.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name != 'server_state'`)
	if err != nil {
		return nil, err
	}
	tables, err := scanStrings(rows)
	if err != nil {
		return nil, err
	}
	for _, t := range tables {
		if _, err := tx.Exec(`DELETE FROM "` + t + `"`); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(`DELETE FROM server_state WHERE key != ?`, stateServerID); err != nil {
		return nil, err
	}
	if err := writeState(tx, stateClosed, "1"); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.membersChanged()
	return uploads, nil
}

// DeleteHost deletes only the computer owner account (kyeot uninstall --purge=mine). Others' records are kept,
// and the owner seat is cleared so the next start begins with an empty Owner. Does nothing if there is no computer account after a handover.
func (s *Store) DeleteHost() (DeletedOwner, error) {
	host := s.HostOwner()
	if host == "" {
		return DeletedOwner{}, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return DeletedOwner{}, err
	}
	defer tx.Rollback()
	d, err := deleteOwnerTx(tx, host)
	if err != nil {
		return d, err
	}
	if _, err := tx.Exec(`DELETE FROM server_state WHERE key IN (?, ?, ?, ?, ?)`, stateOwner, stateHost, stateHostToken, stateHandoverTo, stateHandoverFrom); err != nil {
		return d, err
	}
	if err := tx.Commit(); err != nil {
		return d, err
	}
	s.owner.Store(Owner)
	s.host.Store(Owner)
	s.membersChanged()
	return d, nil
}
