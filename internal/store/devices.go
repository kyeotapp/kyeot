package store

import "errors"

// ErrDeviceTaken means the device token is registered to another user.
var ErrDeviceTaken = errors.New("device registered to another user")

// Device is a device that receives push; the token is an APNs device token (hex).
type Device struct {
	Token    string
	Platform string
	Env      string
}

// SaveDevice registers a device. A token registered to another user is left alone and returns ErrDeviceTaken; it moves only after that user unregisters.
func (s *Store) SaveDevice(ownerID string, d Device) error {
	res, err := s.db.Exec(`INSERT INTO devices (token, owner_id, platform, env, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(token) DO UPDATE SET platform = excluded.platform, env = excluded.env, updated_at = excluded.updated_at
		WHERE devices.owner_id = excluded.owner_id`,
		d.Token, ownerID, d.Platform, d.Env, now())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrDeviceTaken
	}
	return nil
}

// DeleteDevice deletes only that owner's device.
func (s *Store) DeleteDevice(ownerID, token string) error {
	_, err := s.db.Exec(`DELETE FROM devices WHERE token = ? AND owner_id = ?`, token, ownerID)
	return err
}

func (s *Store) Devices(ownerID string) ([]Device, error) {
	rows, err := s.db.Query(`SELECT token, platform, env FROM devices WHERE owner_id = ? ORDER BY updated_at DESC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.Token, &d.Platform, &d.Env); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
