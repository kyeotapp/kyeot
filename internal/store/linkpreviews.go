package store

import (
	"database/sql"
	"errors"
)

// Link preview cache. The same URL gives the same answer to anyone, so it is not split per person.

// LinkPreview is a stored preview; ErrNotFound if none.
func (s *Store) LinkPreview(url string) (json string, fetchedAt int64, ok bool, err error) {
	err = s.rdb.QueryRow(`SELECT json, fetched_at, ok FROM link_previews WHERE url = ?`, url).Scan(&json, &fetchedAt, &ok)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

func (s *Store) PutLinkPreview(url, json string, fetchedAt int64, ok bool) error {
	_, err := s.db.Exec(`INSERT INTO link_previews (url, json, fetched_at, ok) VALUES (?, ?, ?, ?)
		ON CONFLICT (url) DO UPDATE SET json = excluded.json, fetched_at = excluded.fetched_at, ok = excluded.ok`, url, json, fetchedAt, ok)
	return err
}

// PruneLinkPreviews deletes rows fetched before `before` (Unix seconds).
func (s *Store) PruneLinkPreviews(before int64) error {
	_, err := s.db.Exec(`DELETE FROM link_previews WHERE fetched_at < ?`, before)
	return err
}
