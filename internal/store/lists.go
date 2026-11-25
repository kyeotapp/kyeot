package store

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/google/uuid"
)

// List view settings (pins, groups) are per person. Targets are conversations (task) and rooms (room).
const (
	ListTasks = "task"
	ListRooms = "room"
)

var ErrBadList = errors.New("bad list")

type ListGroup struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Position int    `json:"position"`
}

type ListItem struct {
	ID      string `json:"id"`
	Pinned  *int   `json:"pinned"`
	GroupID string `json:"groupId"`
	// GroupPos is the order within the group (smaller is higher); ties go by recency
	GroupPos int `json:"groupPos"`
}

type ListPrefs struct {
	Groups []ListGroup `json:"groups"`
	Items  []ListItem  `json:"items"`
}

func validKind(kind string) bool { return kind == ListTasks || kind == ListRooms }

// ListPrefs returns that person's groups (in order) and the targets that are pinned or grouped.
func (s *Store) ListPrefs(owner, kind string) (ListPrefs, error) {
	out := ListPrefs{Groups: []ListGroup{}, Items: []ListItem{}}
	rows, err := s.db.Query(`SELECT id, name, position FROM list_groups WHERE owner_id = ? AND kind = ? ORDER BY position, id`, owner, kind)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var g ListGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Position); err != nil {
			rows.Close()
			return out, err
		}
		out.Groups = append(out.Groups, g)
	}
	rows.Close()
	rows, err = s.db.Query(`SELECT target_id, pin, group_id, group_pos FROM list_prefs WHERE owner_id = ? AND kind = ? AND (pin IS NOT NULL OR group_id != '') ORDER BY pin IS NULL, pin, target_id`, owner, kind)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var it ListItem
		var pin sql.NullInt64
		if err := rows.Scan(&it.ID, &pin, &it.GroupID, &it.GroupPos); err != nil {
			return out, err
		}
		if pin.Valid {
			n := int(pin.Int64)
			it.Pinned = &n
		}
		out.Items = append(out.Items, it)
	}
	return out, rows.Err()
}

// listPrefMap maps target id to settings; used to attach fields to Tasks and Rooms responses.
func (s *Store) listPrefMap(owner, kind string) (map[string]ListItem, error) {
	p, err := s.ListPrefs(owner, kind)
	if err != nil {
		return nil, err
	}
	m := make(map[string]ListItem, len(p.Items))
	for _, it := range p.Items {
		m[it.ID] = it
	}
	return m, nil
}

// SetPinned pins (appended last) or unpins. If already pinned, the order is kept.
func (s *Store) SetPinned(owner, kind, id string, on bool) error {
	if !on {
		_, err := s.db.Exec(`UPDATE list_prefs SET pin = NULL WHERE owner_id = ? AND kind = ? AND target_id = ?`, owner, kind, id)
		return err
	}
	_, err := s.db.Exec(`INSERT INTO list_prefs (owner_id, kind, target_id, pin)
		VALUES (?1, ?2, ?3, (SELECT coalesce(max(pin), -1) + 1 FROM list_prefs WHERE owner_id = ?1 AND kind = ?2))
		ON CONFLICT (owner_id, kind, target_id) DO UPDATE SET pin = coalesce(pin, excluded.pin)`, owner, kind, id)
	return err
}

// SetPinOrder makes the pin list exactly ids, in that order. Omitted ones are unpinned.
func (s *Store) SetPinOrder(owner, kind string, ids []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE list_prefs SET pin = NULL WHERE owner_id = ? AND kind = ?`, owner, kind); err != nil {
		return err
	}
	for i, id := range ids {
		if _, err := tx.Exec(`INSERT INTO list_prefs (owner_id, kind, target_id, pin) VALUES (?, ?, ?, ?)
			ON CONFLICT (owner_id, kind, target_id) DO UPDATE SET pin = excluded.pin`, owner, kind, id, i); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetItemGroup puts the target in a group; an empty groupID removes it. ErrNotFound if it is not that person's group of that kind.
func (s *Store) SetItemGroup(owner, kind, id, groupID string) error {
	if groupID != "" {
		if _, err := s.listGroup(owner, kind, groupID); err != nil {
			return err
		}
	}
	_, err := s.db.Exec(`INSERT INTO list_prefs (owner_id, kind, target_id, group_id, group_pos) VALUES (?1, ?2, ?3, ?4,
		(SELECT coalesce(max(group_pos), -1) + 1 FROM list_prefs WHERE owner_id = ?1 AND kind = ?2 AND group_id = ?4 AND ?4 != ''))
		ON CONFLICT (owner_id, kind, target_id) DO UPDATE SET group_id = excluded.group_id,
		group_pos = CASE WHEN list_prefs.group_id = excluded.group_id THEN list_prefs.group_pos ELSE excluded.group_pos END`, owner, kind, id, groupID)
	return err
}

// SetGroupItems puts ids in that group in that order (ones from other groups are moved in). Items already in the group stay after them.
func (s *Store) SetGroupItems(owner, kind, groupID string, ids []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var one int
	if err := tx.QueryRow(`SELECT 1 FROM list_groups WHERE id = ? AND owner_id = ? AND kind = ?`, groupID, owner, kind).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE list_prefs SET group_pos = group_pos + ? WHERE owner_id = ? AND kind = ? AND group_id = ?`, len(ids), owner, kind, groupID); err != nil {
		return err
	}
	for i, id := range ids {
		if _, err := tx.Exec(`INSERT INTO list_prefs (owner_id, kind, target_id, group_id, group_pos) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (owner_id, kind, target_id) DO UPDATE SET group_id = excluded.group_id, group_pos = excluded.group_pos`, owner, kind, id, groupID, i); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) listGroup(owner, kind, id string) (ListGroup, error) {
	var g ListGroup
	err := s.db.QueryRow(`SELECT id, name, position FROM list_groups WHERE id = ? AND owner_id = ? AND kind = ?`, id, owner, kind).Scan(&g.ID, &g.Name, &g.Position)
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	return g, err
}

func (s *Store) CreateListGroup(owner, kind, name string) (ListGroup, error) {
	g := ListGroup{ID: uuid.NewString(), Name: strings.TrimSpace(name)}
	err := s.db.QueryRow(`INSERT INTO list_groups (id, owner_id, kind, name, position)
		VALUES (?1, ?2, ?3, ?4, (SELECT coalesce(max(position), -1) + 1 FROM list_groups WHERE owner_id = ?2 AND kind = ?3))
		RETURNING position`, g.ID, owner, kind, g.Name).Scan(&g.Position)
	return g, err
}

func (s *Store) RenameListGroup(owner, kind, id, name string) (ListGroup, error) {
	res, err := s.db.Exec(`UPDATE list_groups SET name = ? WHERE id = ? AND owner_id = ? AND kind = ?`, strings.TrimSpace(name), id, owner, kind)
	if err != nil {
		return ListGroup{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ListGroup{}, ErrNotFound
	}
	return s.listGroup(owner, kind, id)
}

// DeleteListGroup deletes the group and leaves its targets ungrouped (pins are kept).
func (s *Store) DeleteListGroup(owner, kind, id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM list_groups WHERE id = ? AND owner_id = ? AND kind = ?`, id, owner, kind)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`UPDATE list_prefs SET group_id = '' WHERE owner_id = ? AND kind = ? AND group_id = ?`, owner, kind, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetGroupOrder reorders groups to ids. ids must be all of that person's groups of that kind.
func (s *Store) SetGroupOrder(owner, kind string, ids []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM list_groups WHERE owner_id = ? AND kind = ?`, owner, kind).Scan(&n); err != nil {
		return err
	}
	if n != len(ids) {
		return ErrBadList
	}
	for i, id := range ids {
		res, err := tx.Exec(`UPDATE list_groups SET position = ? WHERE id = ? AND owner_id = ? AND kind = ?`, i, id, owner, kind)
		if err != nil {
			return err
		}
		if k, _ := res.RowsAffected(); k == 0 {
			return ErrNotFound
		}
	}
	return tx.Commit()
}

// forgetListTarget removes a vanished target from everyone's settings; only that person's if owner is given.
func forgetListTarget(db dbtx, kind, id, owner string) error {
	q, args := `DELETE FROM list_prefs WHERE kind = ? AND target_id = ?`, []any{kind, id}
	if owner != "" {
		q += ` AND owner_id = ?`
		args = append(args, owner)
	}
	_, err := db.Exec(q, args...)
	return err
}

// OwnsVisibleTask reports whether it is that person's non-deleted conversation.
func (s *Store) OwnsVisibleTask(owner, id string) bool {
	var one int
	return s.db.QueryRow(`SELECT 1 FROM tasks WHERE id = ? AND owner_id = ? AND hidden_at IS NULL`, id, owner).Scan(&one) == nil
}
