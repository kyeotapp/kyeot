package store

import "github.com/kyeotapp/kyeot/internal/protocol"

// RoutineHistoryKeep is how many quiet runs are kept per routine.
const RoutineHistoryKeep = 50

// RecordQuietRun records a run that ended with nothing to send. The same (agent, routine, run) only once.
func (s *Store) RecordQuietRun(agentID, routineID, runID string, at int64, status string) error {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO routine_history (agent_id, routine_id, run_id, at, status) VALUES (?, ?, ?, ?, ?)`,
		agentID, routineID, runID, at, status)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	_, err = s.db.Exec(`DELETE FROM routine_history WHERE agent_id = ?1 AND routine_id = ?2 AND run_id NOT IN
		(SELECT run_id FROM routine_history WHERE agent_id = ?1 AND routine_id = ?2 ORDER BY at DESC LIMIT ?3)`,
		agentID, routineID, RoutineHistoryKeep)
	return err
}

// RoutineHistory returns runs newest first.
func (s *Store) RoutineHistory(agentID, routineID string) ([]protocol.RoutineRun, error) {
	rows, err := s.db.Query(`SELECT at, status FROM routine_history WHERE agent_id = ? AND routine_id = ? ORDER BY at DESC`, agentID, routineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.RoutineRun{}
	for rows.Next() {
		var r protocol.RoutineRun
		if err := rows.Scan(&r.At, &r.Status); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
