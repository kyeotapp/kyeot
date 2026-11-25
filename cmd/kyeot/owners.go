package main

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"strings"

	"github.com/kyeotapp/kyeot/internal/store"
)

// otherUser is someone other than the computer's owner who uses this server.
type otherUser struct {
	name         string
	rooms, tasks int
}

// hostInfo is what terminal commands read from the server DB. Read-only even while the server runs.
type hostInfo struct {
	handedTo string // current owner name if handed over and not yet rotated
	others   []otherUser
}

// readHostInfo opens the DB read-only. Empty values if there is no DB.
func readHostInfo(dbPath string) (hostInfo, error) {
	var h hostInfo
	if dbPath == "" {
		return h, nil
	}
	if _, err := os.Stat(dbPath); errors.Is(err, fs.ErrNotExist) {
		return h, nil
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return h, err
	}
	defer db.Close()
	state := map[string]string{}
	if rows, err := db.Query(`SELECT key, value FROM server_state`); err == nil { // old DBs have no such table
		for rows.Next() {
			var k, v string
			rows.Scan(&k, &v)
			state[k] = v
		}
		rows.Close()
	} else if !strings.Contains(err.Error(), "no such table") {
		return h, err
	}
	host, handed := state["host_owner"]
	if state["owner_id"] == "" {
		host, handed = store.Owner, false
	}
	if handed && host == "" {
		db.QueryRow(`SELECT name FROM owners WHERE id = ?`, state["owner_id"]).Scan(&h.handedTo)
	}
	rows, err := db.Query(`SELECT o.name,
		(SELECT count(*) FROM rooms WHERE owner_id = o.id),
		(SELECT count(*) FROM tasks WHERE owner_id = o.id AND hidden_at IS NULL)
		FROM owners o WHERE o.revoked_at IS NULL AND o.id != ? AND EXISTS (SELECT 1 FROM owner_tokens WHERE owner_id = o.id)
		ORDER BY o.name`, host)
	if err != nil {
		return h, err
	}
	defer rows.Close()
	for rows.Next() {
		var u otherUser
		if err := rows.Scan(&u.name, &u.rooms, &u.tasks); err != nil {
			return h, err
		}
		h.others = append(h.others, u)
	}
	return h, rows.Err()
}
