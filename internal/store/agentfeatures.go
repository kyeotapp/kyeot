package store

import (
	"encoding/json"
	"slices"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

// AddAgentFeatures adds inferred features; true if changed. A learned feature is never removed.
func (s *Store) AddAgentFeatures(agentID string, feats ...string) (changed bool, agent protocol.Agent, err error) {
	agent, err = s.Agent(agentID)
	if err != nil {
		return false, agent, err
	}
	next := slices.Clone(agent.Features)
	for _, f := range feats {
		if f != "" && !slices.Contains(next, f) {
			next = append(next, f)
		}
	}
	if len(next) == len(agent.Features) {
		return false, agent, nil
	}
	slices.Sort(next)
	b, _ := json.Marshal(next)
	if _, err := s.db.Exec(`UPDATE agents SET features = ? WHERE id = ?`, string(b), agentID); err != nil {
		return false, agent, err
	}
	agent.Features = next
	return true, agent, nil
}

// LearnedRoutine is a routine learned from routine.run; used to attach routines of agents that send no report to a room.
type LearnedRoutine struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	LastRunAt int64  `json:"lastRunAt"`
}

func (s *Store) LearnRoutine(agentID, routineID, name string, at int64) error {
	if name == "" {
		name = routineID
	}
	_, err := s.db.Exec(`INSERT INTO agent_routines (agent_id, routine_id, name, last_run_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(agent_id, routine_id) DO UPDATE SET name = excluded.name, last_run_at = max(last_run_at, excluded.last_run_at)`,
		agentID, routineID, name, at)
	return err
}

// LearnedRoutines returns routines, most recently run first.
func (s *Store) LearnedRoutines(agentID string) ([]LearnedRoutine, error) {
	rows, err := s.db.Query(`SELECT routine_id, name, last_run_at FROM agent_routines WHERE agent_id = ? ORDER BY last_run_at DESC, routine_id`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LearnedRoutine{}
	for rows.Next() {
		var r LearnedRoutine
		if err := rows.Scan(&r.ID, &r.Name, &r.LastRunAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
