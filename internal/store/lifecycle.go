package store

import (
	"database/sql"
	"errors"
	"log"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

// OpenTasks returns the agent's unfinished (running, waiting) tasks; the server closes them when the agent disconnects.
func (s *Store) OpenTasks(agentID string) ([]OwnedTask, error) {
	return s.openTasks(`AND agent_id = ?`, agentID)
}

// AllOpenTasks returns in-progress (running, waiting) tasks of all agents.
func (s *Store) AllOpenTasks() ([]OwnedTask, error) {
	return s.openTasks(``)
}

func (s *Store) openTasks(where string, args ...any) ([]OwnedTask, error) {
	rows, err := s.db.Query(`SELECT `+taskCols+` FROM tasks WHERE state IN (?, ?) `+where+` ORDER BY created_at`,
		append([]any{protocol.TaskRunning, protocol.TaskWaiting}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OwnedTask
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// LastEventSource is who left the task's last event (user, agent); empty if none.
// LastEventID is the task's last event number; 0 if none.
func (s *Store) LastEventID(taskID string) int64 {
	var id sql.NullInt64
	logLookup("LastEventID", s.db.QueryRow(`SELECT MAX(id) FROM events WHERE task_id = ?`, taskID).Scan(&id))
	return id.Int64
}

func (s *Store) LastEventSource(taskID string) string {
	var src sql.NullString
	logLookup("LastEventSource", s.db.QueryRow(`SELECT source FROM events WHERE task_id = ? ORDER BY id DESC LIMIT 1`, taskID).Scan(&src))
	return src.String
}

// UnansweredApprovals counts approvals in the task not yet answered, regardless of task state.
func (s *Store) UnansweredApprovals(taskID string) int {
	var n int
	s.db.QueryRow(`SELECT count(*) FROM events e WHERE e.task_id = ? AND e.type = 'approval'
		AND NOT `+approvalClosed, taskID).Scan(&n)
	return n
}

// SameApprovalPending reports whether an unanswered approval with the same id and content already exists; filters approvals resent on reconnect.
func (s *Store) SameApprovalPending(taskID, id string, payload []byte) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM events e WHERE e.task_id = ? AND e.type = 'approval'
		AND json_extract(e.payload, '$.id') = ? AND e.payload = ? AND NOT `+approvalClosed, taskID, id, string(payload)).Scan(&n)
	return n > 0, err
}

// logLookup logs DB errors of lookups that return an empty value; a missing row is not an error.
func logLookup(what string, err error) {
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Printf("store %s: %v", what, err)
	}
}
