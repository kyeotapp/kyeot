package store

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

type ScreenRow struct {
	ID        string          `json:"id"`
	Version   int             `json:"version"`
	Spec      json.RawMessage `json:"spec"`
	UpdatedAt int64           `json:"updatedAt"`
}

// PutScreen stacks a new version and returns its number. A previously deleted screen comes back.
func (s *Store) PutScreen(agentID, screenID string, spec json.RawMessage) (int, error) {
	var v int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM ui_screens WHERE agent_id = ? AND screen_id = ?`, agentID, screenID).Scan(&v); err != nil {
		return 0, err
	}
	v++
	_, err := s.db.Exec(`INSERT INTO ui_screens (agent_id, screen_id, version, spec, created_at) VALUES (?, ?, ?, ?, ?)`, agentID, screenID, v, string(spec), now())
	return v, err
}

func (s *Store) RemoveScreen(agentID, screenID string) error {
	_, err := s.db.Exec(`UPDATE ui_screens SET removed = 1 WHERE agent_id = ? AND screen_id = ?`, agentID, screenID)
	return err
}

// Screens returns the latest version of each non-deleted screen.
func (s *Store) Screens(agentID string) ([]ScreenRow, error) {
	return scanScreens(s.db.Query(`SELECT screen_id, version, spec, created_at FROM ui_screens u
		WHERE agent_id = ? AND removed = 0 AND version = (SELECT MAX(version) FROM ui_screens WHERE agent_id = u.agent_id AND screen_id = u.screen_id)
		ORDER BY created_at`, agentID))
}

func (s *Store) ScreenVersions(agentID, screenID string) ([]ScreenRow, error) {
	return scanScreens(s.db.Query(`SELECT screen_id, version, spec, created_at FROM ui_screens WHERE agent_id = ? AND screen_id = ? ORDER BY version DESC LIMIT 20`, agentID, screenID))
}

func scanScreens(rows *sql.Rows, err error) ([]ScreenRow, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScreenRow{}
	for rows.Next() {
		var r ScreenRow
		var spec string
		if err := rows.Scan(&r.ID, &r.Version, &spec, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Spec = json.RawMessage(spec)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Tabs returns the tab layout and whether it was ever set.
func (s *Store) Tabs(agentID string) ([]string, bool, error) {
	var raw string
	err := s.db.QueryRow(`SELECT tabs FROM ui_layouts WHERE agent_id = ?`, agentID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return []string{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	tabs := []string{}
	json.Unmarshal([]byte(raw), &tabs)
	return tabs, true, nil
}

// Hidden returns screens hidden from both the tab bar and home. Not deleted.
func (s *Store) Hidden(agentID string) []string { return s.layoutList(agentID, "hidden") }

func (s *Store) SetHidden(agentID string, ids []string) error {
	return s.setLayoutList(agentID, "hidden", ids)
}

// Locked returns screens locked against deletion.
func (s *Store) Locked(agentID string) []string { return s.layoutList(agentID, "locked") }

func (s *Store) SetLocked(agentID string, ids []string) error {
	return s.setLayoutList(agentID, "locked", ids)
}

// OffOrder is the order of screens outside the tabs. Screens not listed go last in the app.
func (s *Store) OffOrder(agentID string) []string { return s.layoutList(agentID, "off_order") }

func (s *Store) SetOffOrder(agentID string, ids []string) error {
	return s.setLayoutList(agentID, "off_order", ids)
}

// col is one of hidden, locked, off_order. Callers pass only fixed literals.
func (s *Store) layoutList(agentID, col string) []string {
	var raw string
	ids := []string{}
	if s.db.QueryRow(`SELECT `+col+` FROM ui_layouts WHERE agent_id = ?`, agentID).Scan(&raw) == nil {
		json.Unmarshal([]byte(raw), &ids)
	}
	return ids
}

func (s *Store) setLayoutList(agentID, col string, ids []string) error {
	raw, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO ui_layouts (agent_id, tabs, `+col+`, updated_at) VALUES (?, '[]', ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET `+col+` = excluded.`+col+`, updated_at = excluded.updated_at`, agentID, string(raw), now())
	return err
}

func (s *Store) SetTabs(agentID string, tabs []string) error {
	raw, err := json.Marshal(tabs)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO ui_layouts (agent_id, tabs, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET tabs = excluded.tabs, updated_at = excluded.updated_at`, agentID, string(raw), now())
	return err
}

type DataRow struct {
	Value     json.RawMessage        `json:"value"`
	UpdatedAt int64                  `json:"updatedAt"`
	Warnings  []protocol.DataWarning `json:"warnings,omitempty"`
}

// PutData replaces the whole bundle. Warnings are replaced too (cleared if none).
func (s *Store) PutData(agentID, name string, value json.RawMessage, warnings []protocol.DataWarning) error {
	w, err := json.Marshal(warnings)
	if err != nil || warnings == nil {
		w = []byte("[]")
	}
	_, err = s.db.Exec(`INSERT INTO ui_data (agent_id, name, value, updated_at, warnings) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(agent_id, name) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at, warnings = excluded.warnings`, agentID, name, string(value), now(), string(w))
	return err
}

func (s *Store) RemoveData(agentID, name string) error {
	_, err := s.db.Exec(`DELETE FROM ui_data WHERE agent_id = ? AND name = ?`, agentID, name)
	return err
}

// Data returns all of the agent's bundles; an empty map if none.
func (s *Store) Data(agentID string) (map[string]DataRow, error) {
	rows, err := s.db.Query(`SELECT name, value, updated_at, warnings FROM ui_data WHERE agent_id = ?`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]DataRow{}
	for rows.Next() {
		var name, value, warnings string
		var r DataRow
		if err := rows.Scan(&name, &value, &r.UpdatedAt, &warnings); err != nil {
			return nil, err
		}
		r.Value = json.RawMessage(value)
		json.Unmarshal([]byte(warnings), &r.Warnings)
		out[name] = r
	}
	return out, rows.Err()
}

// Assembled parts; versioned by stacking, like screens.

func (s *Store) PutPart(agentID, partID string, spec json.RawMessage) (int, error) {
	var v int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM ui_parts WHERE agent_id = ? AND part_id = ?`, agentID, partID).Scan(&v); err != nil {
		return 0, err
	}
	v++
	_, err := s.db.Exec(`INSERT INTO ui_parts (agent_id, part_id, version, spec, created_at) VALUES (?, ?, ?, ?, ?)`, agentID, partID, v, string(spec), now())
	return v, err
}

func (s *Store) RemovePart(agentID, partID string) error {
	_, err := s.db.Exec(`UPDATE ui_parts SET removed = 1 WHERE agent_id = ? AND part_id = ?`, agentID, partID)
	return err
}

// Parts returns the latest version of each non-deleted part.
func (s *Store) Parts(agentID string) ([]ScreenRow, error) {
	return scanScreens(s.db.Query(`SELECT part_id, version, spec, created_at FROM ui_parts u
		WHERE agent_id = ? AND removed = 0 AND version = (SELECT MAX(version) FROM ui_parts WHERE agent_id = u.agent_id AND part_id = u.part_id)
		ORDER BY created_at`, agentID))
}

func (s *Store) PartVersions(agentID, partID string) ([]ScreenRow, error) {
	return scanScreens(s.db.Query(`SELECT part_id, version, spec, created_at FROM ui_parts WHERE agent_id = ? AND part_id = ? ORDER BY version DESC LIMIT 20`, agentID, partID))
}
