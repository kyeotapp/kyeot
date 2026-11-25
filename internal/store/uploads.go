package store

import (
	"database/sql"
	"errors"

	"github.com/google/uuid"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

const uploadsTable = `CREATE TABLE IF NOT EXISTS uploads (
  id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL REFERENCES owners(id),
  name TEXT NOT NULL,
  mime TEXT NOT NULL,
  size INTEGER NOT NULL,
  created_at INTEGER NOT NULL
)`

// CreateUpload records an uploaded file's info and returns its id. The caller stores the file itself under that id.
func (s *Store) CreateUpload(ownerID, name, mime string, size int64) (protocol.Attachment, error) {
	a := protocol.Attachment{ID: uuid.NewString(), Name: name, Mime: mime, Size: size}
	_, err := s.db.Exec(`INSERT INTO uploads (id, owner_id, name, mime, size, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		a.ID, ownerID, a.Name, a.Mime, a.Size, now())
	return a, err
}

// Upload looks up file info and owner by id.
func (s *Store) Upload(id string) (protocol.Attachment, string, error) {
	var a protocol.Attachment
	var owner string
	err := s.db.QueryRow(`SELECT id, name, mime, size, owner_id FROM uploads WHERE id = ?`, id).Scan(&a.ID, &a.Name, &a.Mime, &a.Size, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		return a, "", ErrNotFound
	}
	return a, owner, err
}

func (s *Store) DeleteUpload(id string) error {
	_, err := s.db.Exec(`DELETE FROM uploads WHERE id = ?`, id)
	return err
}

// UploadBytes is the total size of files that user uploaded and has not yet attached to a post.
func (s *Store) UploadBytes(ownerID string) (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT coalesce(sum(size), 0) FROM uploads WHERE owner_id = ? AND used_at IS NULL`, ownerID).Scan(&n) // attached ones are not counted
	return n, err
}

// UploadTotalBytes is the total size of all files that user uploaded (including attached ones).
func (s *Store) UploadTotalBytes(ownerID string) (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT coalesce(sum(size), 0) FROM uploads WHERE owner_id = ?`, ownerID).Scan(&n)
	return n, err
}

// AttachUploads takes attached files out of cleanup and records the task they attach to. If any file is already attached, changes nothing and returns ErrUploadUsed.
func (s *Store) AttachUploads(taskID string, atts []protocol.Attachment) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	at := now()
	for _, a := range atts {
		res, err := tx.Exec(`UPDATE uploads SET used_at = ?, task_id = ? WHERE id = ? AND used_at IS NULL`, at, taskID, a.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrUploadUsed
		}
	}
	return tx.Commit()
}

// DetachUploads undoes AttachUploads. Only files attached to that task become unattached again.
func (s *Store) DetachUploads(taskID string, atts []protocol.Attachment) error {
	for _, a := range atts {
		if _, err := s.db.Exec(`UPDATE uploads SET used_at = NULL, task_id = '' WHERE id = ? AND task_id = ?`, a.ID, taskID); err != nil {
			return err
		}
	}
	return nil
}

// AgentCanFetchUpload reports whether an agent token may download that file.
// Files attached to a task: only that task's agent; attached files from the old version without task_id: the owner's agents; unattached files and room human post files: nobody.
func (s *Store) AgentCanFetchUpload(id string, a AgentAuth) bool {
	var owner, taskID, taskAgent string
	var used, roomTalk bool
	err := s.db.QueryRow(`SELECT u.owner_id, u.task_id, u.used_at IS NOT NULL, u.room_message_id != 0, coalesce(t.agent_id, '')
		FROM uploads u LEFT JOIN tasks t ON t.id = u.task_id WHERE u.id = ?`, id).Scan(&owner, &taskID, &used, &roomTalk, &taskAgent)
	switch {
	case err != nil || !used || roomTalk:
		return false
	case taskID != "":
		return taskAgent == a.ID
	default:
		return owner == a.OwnerID
	}
}

// StaleUploads returns ids of files uploaded before `before` (milliseconds) and attached to no post.
func (s *Store) StaleUploads(before int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM uploads WHERE used_at IS NULL AND created_at < ?`, before)
	if err != nil {
		return nil, err
	}
	return scanStrings(rows)
}

// migrateUploadsUsed marks files already attached to posts when the used_at column is first created.
func migrateUploadsUsed(db dbtx) error {
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('uploads') WHERE name = 'used_at'`).Scan(&n); err != nil || n > 0 {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE uploads ADD COLUMN used_at INTEGER`); err != nil {
		return err
	}
	_, err := db.Exec(`UPDATE uploads SET used_at = created_at WHERE EXISTS (SELECT 1 FROM events e WHERE instr(e.payload, uploads.id) > 0)
		OR EXISTS (SELECT 1 FROM ui_screens x WHERE instr(x.spec, uploads.id) > 0)
		OR EXISTS (SELECT 1 FROM ui_data d WHERE instr(d.value, uploads.id) > 0)`)
	return err
}
