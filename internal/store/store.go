package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

const Owner = "me"

// SchemaVersion is the DB schema number (PRAGMA user_version). Bump by 1 when tables, columns, indexes, or data fixes change.
const SchemaVersion = 9

var ErrNotFound = errors.New("not found")

// ErrNewerDB means the DB was made by a newer version than this one. It is neither opened nor touched.
var ErrNewerDB = errors.New("이 데이터는 더 새 판 곁이 만들었다. kyeot 을 업데이트한다")

const schema = `
CREATE TABLE IF NOT EXISTS owners (id TEXT PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS agents (
  id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL REFERENCES owners(id),
  name TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  state TEXT NOT NULL DEFAULT 'offline',
  last_seen INTEGER,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL REFERENCES owners(id),
  agent_id TEXT NOT NULL REFERENCES agents(id),
  title TEXT NOT NULL,
  state TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id TEXT NOT NULL REFERENCES tasks(id),
  source TEXT NOT NULL,
  type TEXT NOT NULL,
  payload TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS events_by_task ON events(task_id, id);
CREATE TABLE IF NOT EXISTS rooms (
  id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL REFERENCES owners(id),
  name TEXT NOT NULL,
  agent_id TEXT NOT NULL REFERENCES agents(id),
  routine_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS room_routines (
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  routine_id TEXT NOT NULL,
  position INTEGER NOT NULL,
  PRIMARY KEY (room_id, routine_id)
);
CREATE TABLE IF NOT EXISTS ui_screens (
  agent_id TEXT NOT NULL REFERENCES agents(id),
  screen_id TEXT NOT NULL,
  version INTEGER NOT NULL,
  spec TEXT NOT NULL,
  removed INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (agent_id, screen_id, version)
);
CREATE TABLE IF NOT EXISTS ui_parts (
  agent_id TEXT NOT NULL REFERENCES agents(id),
  part_id TEXT NOT NULL,
  version INTEGER NOT NULL,
  spec TEXT NOT NULL,
  removed INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (agent_id, part_id, version)
);
CREATE TABLE IF NOT EXISTS ui_layouts (
  agent_id TEXT PRIMARY KEY REFERENCES agents(id),
  tabs TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS ui_data (
  agent_id TEXT NOT NULL REFERENCES agents(id),
  name TEXT NOT NULL,
  value TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (agent_id, name)
);
CREATE TABLE IF NOT EXISTS owner_tokens (
  token_hash TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL REFERENCES owners(id),
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS room_members (
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  owner_id TEXT NOT NULL REFERENCES owners(id),
  joined_at INTEGER NOT NULL,
  PRIMARY KEY (room_id, owner_id)
);
CREATE TABLE IF NOT EXISTS room_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  owner_id TEXT NOT NULL REFERENCES owners(id),
  text TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS room_messages_by_room ON room_messages(room_id, id);
CREATE TABLE IF NOT EXISTS room_message_reactions (
  message_id INTEGER NOT NULL REFERENCES room_messages(id) ON DELETE CASCADE,
  owner_id TEXT NOT NULL REFERENCES owners(id),
  emoji TEXT NOT NULL,
  PRIMARY KEY (message_id, owner_id, emoji)
);
CREATE TABLE IF NOT EXISTS room_reports (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  owner_id TEXT NOT NULL,
  room_id TEXT NOT NULL,
  handler_id TEXT NOT NULL,
  target_id TEXT NOT NULL,
  message_id INTEGER NOT NULL DEFAULT 0,
  message_text TEXT NOT NULL DEFAULT '',
  reason TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  actions TEXT NOT NULL DEFAULT '',
  handled_at INTEGER
);
CREATE INDEX IF NOT EXISTS room_reports_by_handler ON room_reports(handler_id, id);
CREATE TABLE IF NOT EXISTS blocks (
  owner_id TEXT NOT NULL,
  blocked_id TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (owner_id, blocked_id)
);
CREATE TABLE IF NOT EXISTS agent_routines (
  agent_id TEXT NOT NULL REFERENCES agents(id),
  routine_id TEXT NOT NULL,
  name TEXT NOT NULL,
  last_run_at INTEGER NOT NULL,
  PRIMARY KEY (agent_id, routine_id)
);
CREATE TABLE IF NOT EXISTS routine_history (
  agent_id TEXT NOT NULL REFERENCES agents(id),
  routine_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  at INTEGER NOT NULL,
  status TEXT NOT NULL,
  PRIMARY KEY (agent_id, routine_id, run_id)
);
CREATE TABLE IF NOT EXISTS devices (
  token TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL,
  platform TEXT NOT NULL,
  env TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS list_groups (
  id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  name TEXT NOT NULL,
  position INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS server_state (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS list_prefs (
  owner_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  target_id TEXT NOT NULL,
  pin INTEGER,
  group_id TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (owner_id, kind, target_id)
);
CREATE TABLE IF NOT EXISTS link_previews (
  url TEXT PRIMARY KEY,
  json TEXT NOT NULL,
  fetched_at INTEGER NOT NULL,
  ok INTEGER NOT NULL
);
`

// Adds only the columns missing from an already created DB.
var addColumns = []struct{ table, column, def string }{
	{"tasks", "room_id", "TEXT NOT NULL DEFAULT ''"},
	{"tasks", "routine_id", "TEXT NOT NULL DEFAULT ''"},
	{"tasks", "run_id", "TEXT NOT NULL DEFAULT ''"},
	{"agents", "report", "TEXT"},
	{"agents", "report_at", "INTEGER"},
	{"agents", "avatar", "TEXT NOT NULL DEFAULT ''"},
	{"list_prefs", "group_pos", "INTEGER NOT NULL DEFAULT 0"},
	{"agents", "info", "TEXT NOT NULL DEFAULT ''"},
	{"agents", "removed_at", "INTEGER"},
	{"tasks", "hidden_at", "INTEGER"},
	{"ui_layouts", "hidden", "TEXT NOT NULL DEFAULT '[]'"},
	{"ui_layouts", "locked", "TEXT NOT NULL DEFAULT '[]'"},
	{"ui_layouts", "off_order", "TEXT NOT NULL DEFAULT '[]'"},
	{"tasks", "title_source", "TEXT NOT NULL DEFAULT 'auto'"},
	{"room_members", "role", "TEXT NOT NULL DEFAULT 'member'"},
	{"tasks", "asked_by", "TEXT NOT NULL DEFAULT ''"},
	{"owners", "revoked_at", "INTEGER"},
	{"owners", "role", "TEXT NOT NULL DEFAULT ''"},
	{"agents", "features", "TEXT NOT NULL DEFAULT '[]'"},
	{"ui_data", "warnings", "TEXT NOT NULL DEFAULT '[]'"},
	{"uploads", "task_id", "TEXT NOT NULL DEFAULT ''"},
	{"uploads", "room_message_id", "INTEGER NOT NULL DEFAULT 0"},
	{"room_messages", "quote_id", "INTEGER NOT NULL DEFAULT 0"},
	{"room_messages", "quote_text", "TEXT NOT NULL DEFAULT ''"},
	{"room_messages", "edited_at", "INTEGER"},
	{"room_messages", "attachments", "TEXT NOT NULL DEFAULT '[]'"},
	{"owners", "avatar", "TEXT NOT NULL DEFAULT ''"},
	{"owners", "terms_version", "TEXT NOT NULL DEFAULT ''"},
	{"owners", "terms_at", "INTEGER"},
	{"rooms", "filter_off", "INTEGER NOT NULL DEFAULT 0"},
	{"room_messages", "removed_at", "INTEGER"},
}

// Indexes: pending approvals, room feed, lookups per agent, room, and member
var indexes = []string{
	`CREATE INDEX IF NOT EXISTS events_by_task_type ON events(task_id, type, id)`,
	`CREATE INDEX IF NOT EXISTS tasks_by_owner_state ON tasks(owner_id, state)`,
	`CREATE INDEX IF NOT EXISTS tasks_by_owner_created ON tasks(owner_id, created_at)`,
	`CREATE INDEX IF NOT EXISTS tasks_by_agent_state ON tasks(agent_id, state)`,
	`CREATE INDEX IF NOT EXISTS tasks_by_room ON tasks(room_id)`,
	`CREATE INDEX IF NOT EXISTS agents_by_owner ON agents(owner_id)`,
	`CREATE INDEX IF NOT EXISTS rooms_by_owner ON rooms(owner_id)`,
	`CREATE INDEX IF NOT EXISTS rooms_by_agent ON rooms(agent_id)`,
	`CREATE INDEX IF NOT EXISTS room_members_by_owner ON room_members(owner_id)`,
}

// dbtx is what *sql.DB and *sql.Tx share. Migrations run inside a transaction.
type dbtx interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func migrate(db dbtx) error {
	for _, c := range addColumns {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, c.table, c.column).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec(`ALTER TABLE ` + c.table + ` ADD COLUMN ` + c.column + ` ` + c.def); err != nil {
				return err
			}
		}
	}
	if err := migrateUploadsUsed(db); err != nil {
		return err
	}
	if err := uniqueRuns(db); err != nil {
		return err
	}
	for _, q := range indexes {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	// rooms.routine_id always equals the first routine, so a removed routine does not come back on a re-run
	if _, err := db.Exec(`INSERT OR IGNORE INTO room_routines (room_id, routine_id, position) SELECT id, routine_id, 0 FROM rooms WHERE routine_id != ''`); err != nil {
		return err
	}
	rows, err := db.Query(`SELECT id FROM agents WHERE avatar = ''`)
	if err != nil {
		return err
	}
	ids, err := scanStrings(rows)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := db.Exec(`UPDATE agents SET avatar = ? WHERE id = ?`, DefaultAvatar(id), id); err != nil {
			return err
		}
	}
	return nil
}

// uniqueRuns creates the unique index on run ids. Of duplicate runs only the earliest keeps its id (rows are not deleted).
func uniqueRuns(db dbtx) error {
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'tasks_by_run'`).Scan(&n); err != nil || n > 0 {
		return err
	}
	res, err := db.Exec(`UPDATE tasks SET run_id = '' WHERE run_id != '' AND rowid NOT IN
		(SELECT min(rowid) FROM tasks WHERE run_id != '' GROUP BY agent_id, routine_id, run_id)`)
	if err != nil {
		return err
	}
	if k, _ := res.RowsAffected(); k > 0 {
		log.Printf("store: 겹친 루틴 실행 %d개의 run_id 를 비웠다", k)
	}
	_, err = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS tasks_by_run ON tasks(agent_id, routine_id, run_id) WHERE run_id != ''`)
	return err
}

// scanStrings reads all rows of a one-column result and closes it.
func scanStrings(rows *sql.Rows) ([]string, error) {
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

type Store struct {
	db  *sql.DB // writes, and reads called mixed with writes. One connection
	rdb *sql.DB // read-only REST queries. Separate multiple connections for a file DB; same as db for an in-memory DB

	shareMu  sync.Mutex
	shareGen uint64          // rises on every member change; if it changed during a lookup, the result is not cached
	shared   map[string]bool // owner -> does that owner's room have a member

	owner    atomic.Value // server owner id: Owner at first, the successor after a handover
	host     atomic.Value // account of the server-config token: "" after handover, the new owner after rotate
	serverID string
	path     string // path of a regular-file DB; "" for in-memory or URI
}

// OwnedTask is a task with owner_id added, used for ownership checks.
type OwnedTask struct {
	protocol.Task
	OwnerID string
}

type AgentAuth struct {
	ID      string
	OwnerID string
}

func now() int64 { return time.Now().UnixMilli() }

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// dsn applies foreign keys and lock waiting to every connection, so they stay on when a connection reopens.
func dsn(path string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // so ":memory:" is not created separately per connection
	var ver int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil {
		db.Close()
		return nil, err
	}
	if ver > SchemaVersion { // finish before writing
		db.Close()
		return nil, fmt.Errorf("%w (%s: 구조 %d, 이 판은 %d)", ErrNewerDB, path, ver, SchemaVersion)
	}
	if ver < SchemaVersion && !inMemory(path) {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master`).Scan(&n); err != nil {
			db.Close()
			return nil, err
		}
		if n > 0 && !plainFile(path) {
			log.Printf("store: %s is not a regular file path, so the schema is changed %d → %d without a backup", path, ver, SchemaVersion)
		} else if n > 0 {
			name, err := backup(db, path, ver)
			if err != nil {
				db.Close()
				return nil, fmt.Errorf("구조를 바꾸기 전 백업에 실패해 DB 를 그대로 두었다(디스크 공간과 폴더 권한을 확인한다): %w", err)
			}
			log.Printf("DB schema %d → %d. Backup before the change: %s — if something goes wrong, stop the server and restore this file as %s", ver, SchemaVersion, name, filepath.Base(path))
		}
	}
	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil { // cannot be changed inside a transaction
		db.Close()
		return nil, err
	}
	if err := upgrade(db); err != nil {
		db.Close()
		return nil, err
	}
	if plainFile(path) {
		if err := privateFiles(path); err != nil {
			log.Printf("store: could not set DB file permissions to 600: %v", err)
		}
	}
	ownerID, err := readState(db, stateOwner)
	if err != nil {
		db.Close()
		return nil, err
	}
	if ownerID == "" { // server never handed over: the first user Owner is the owner
		if _, err := db.Exec(`INSERT OR IGNORE INTO owners (id, name) VALUES (?, ?)`, Owner, Owner); err != nil {
			db.Close()
			return nil, err
		}
	}
	serverID, err := ensureServerID(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	rdb := db
	if !inMemory(path) { // WAL, so read connections do not wait for writes
		if rdb, err = sql.Open("sqlite", dsn(path)+"&_pragma=query_only(1)"); err != nil {
			db.Close()
			return nil, err
		}
		rdb.SetMaxOpenConns(4)
	}
	st := &Store{db: db, rdb: rdb, shared: map[string]bool{}, serverID: serverID}
	if plainFile(path) {
		st.path = path
	}
	if err := st.loadOwners(); err != nil {
		st.Close()
		return nil, err
	}
	return st, nil
}

// upgrade runs everything from creating tables to writing the version in one transaction. If it fails midway, nothing changes.
func upgrade(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(schema + ";" + uploadsTable); err != nil {
		return err
	}
	if err := migrate(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, SchemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

func inMemory(path string) bool {
	return path == ":memory:" || strings.Contains(path, "mode=memory")
}

func (s *Store) Close() error {
	if s.rdb != s.db {
		s.rdb.Close()
	}
	return s.db.Close()
}

func (s *Store) CreateAgent(ownerID, name string) (protocol.Agent, string, error) {
	buf := make([]byte, 32)
	rand.Read(buf)
	token := base64.RawURLEncoding.EncodeToString(buf)
	id := uuid.NewString()
	_, err := s.db.Exec(`INSERT INTO agents (id, owner_id, name, token_hash, created_at, avatar) VALUES (?, ?, ?, ?, ?, ?)`,
		id, ownerID, name, hashToken(token), now(), DefaultAvatar(id))
	if err != nil {
		return protocol.Agent{}, "", err
	}
	agent, err := s.Agent(id)
	return agent, token, err
}

// Presets are the default character names in the app's assets/avatars. Default assignment is tied to this length, so it is not extended.
var Presets = []string{"robot", "cat", "dog", "fox", "panda", "penguin", "frog", "rabbit", "bear", "owl"}

// MotionPresets are the G kinds in the app's iconDrafts.ts (moving characters). Selectable only; not used for default assignment.
var MotionPresets = []string{"maltipooG", "mackerelG", "calicoG", "cheeseG", "fluffyG", "beagleG", "shibaG", "corgiG", "jindoG", "pomG", "rabbitG", "penguinG", "catRoundG", "bearG", "puppyG", "pandaG", "frogG", "foxG", "tuxedoG", "dogRoundG", "owlG", "robotG"}

// Pickable reports whether the name is one the user can pick.
func Pickable(name string) bool {
	return slices.Contains(Presets, name) || slices.Contains(MotionPresets, name)
}

// PersonColors are the first-letter color names of person pictures (color:<name>). Must match the app's personColors.
var PersonColors = []string{"blue", "orange", "green", "purple", "pink", "yellow", "teal", "gray"}

// IsPhoto reports whether it is a jpeg/png data URL of at most 200KB. Shared by agent and person pictures.
func IsPhoto(v string) bool {
	return (strings.HasPrefix(v, "data:image/jpeg;base64,") || strings.HasPrefix(v, "data:image/png;base64,")) && len(v) <= 200_000
}

// PersonAvatarOK reports whether the value is acceptable as a person picture. Empty resets to the first-letter default color.
func PersonAvatarOK(v string) bool {
	color, isColor := strings.CutPrefix(v, "color:")
	return v == "" || (isColor && slices.Contains(PersonColors, color)) || IsPhoto(v)
}

// DefaultAvatar picks one default character from the agent id. The same id always gives the same character.
func DefaultAvatar(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "preset:" + Presets[int(sum[0])%len(Presets)]
}

func (s *Store) SetAvatar(agentID, avatar string) (protocol.Agent, error) {
	if _, err := s.db.Exec(`UPDATE agents SET avatar = ? WHERE id = ?`, avatar, agentID); err != nil {
		return protocol.Agent{}, err
	}
	return s.Agent(agentID)
}

// HideTask removes the conversation from the list. History stays in the DB.
func (s *Store) HideTask(taskID string) error {
	if _, err := s.db.Exec(`UPDATE tasks SET hidden_at = ? WHERE id = ?`, now(), taskID); err != nil {
		return err
	}
	return forgetListTarget(s.db, ListTasks, taskID, "")
}

// UnlinkAgent unregisters the connection: invalidates the token and removes it from the list. Conversation history stays in the DB.
func (s *Store) UnlinkAgent(agentID string) error {
	_, err := s.db.Exec(`UPDATE agents SET removed_at = ?, state = 'offline', token_hash = 'unlinked:' || id WHERE id = ?`, now(), agentID)
	return err
}

// AgentRemoved reports whether the agent was unregistered. ErrNotFound if the id does not exist.
func (s *Store) AgentRemoved(id string) (bool, error) {
	var removed sql.NullInt64
	err := s.db.QueryRow(`SELECT removed_at FROM agents WHERE id = ?`, id).Scan(&removed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return removed.Valid, err
}

// RotateToken gives a live agent a new token. The old token is invalid immediately.
func (s *Store) RotateToken(id string) (string, error) {
	buf := make([]byte, 32)
	rand.Read(buf)
	token := base64.RawURLEncoding.EncodeToString(buf)
	res, err := s.db.Exec(`UPDATE agents SET token_hash = ? WHERE id = ? AND removed_at IS NULL`, hashToken(token), id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	return token, nil
}

func (s *Store) AgentByToken(token string) (AgentAuth, error) {
	var a AgentAuth
	err := s.db.QueryRow(`SELECT a.id, a.owner_id FROM agents a JOIN owners o ON o.id = a.owner_id
		WHERE a.token_hash = ? AND a.removed_at IS NULL AND o.revoked_at IS NULL`, hashToken(token)).Scan(&a.ID, &a.OwnerID)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

const agentCols = `id, name, state, last_seen, avatar, info, features`

func scanAgent(row interface{ Scan(...any) error }) (protocol.Agent, error) {
	var a protocol.Agent
	var info, features string
	err := row.Scan(&a.ID, &a.Name, &a.State, &a.LastSeen, &a.Avatar, &info, &features)
	if err == nil && features != "" {
		json.Unmarshal([]byte(features), &a.Features)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err == nil && info != "" {
		var i protocol.AgentInfo
		if json.Unmarshal([]byte(info), &i) == nil {
			a.Info = &i
		}
	}
	return a, err
}

// SetAgentInfo stores the kind and model the agent announced in hello.
func (s *Store) SetAgentInfo(agentID string, info protocol.AgentInfo) (protocol.Agent, error) {
	b, err := json.Marshal(info)
	if err != nil {
		return protocol.Agent{}, err
	}
	if _, err := s.db.Exec(`UPDATE agents SET info = ? WHERE id = ?`, string(b), agentID); err != nil {
		return protocol.Agent{}, err
	}
	return s.Agent(agentID)
}

func (s *Store) Agent(id string) (protocol.Agent, error) {
	return scanAgent(s.db.QueryRow(`SELECT `+agentCols+` FROM agents WHERE id = ?`, id))
}

// AgentCount is the number of that person's non-unregistered agents.
func (s *Store) AgentCount(ownerID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE owner_id = ? AND removed_at IS NULL`, ownerID).Scan(&n)
	return n, err
}

func (s *Store) Agents(ownerID string) ([]protocol.Agent, error) {
	rows, err := s.db.Query(`SELECT `+agentCols+` FROM agents WHERE owner_id = ? AND removed_at IS NULL ORDER BY created_at`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AgentOwner is the person who attached the agent. Error if the agent is unregistered.
func (s *Store) AgentOwner(agentID string) (string, error) {
	var owner string
	err := s.db.QueryRow(`SELECT owner_id FROM agents WHERE id = ? AND removed_at IS NULL`, agentID).Scan(&owner)
	return owner, err
}

func (s *Store) AgentOwnedBy(agentID, ownerID string) bool {
	var one int
	return s.db.QueryRow(`SELECT 1 FROM agents WHERE id = ? AND owner_id = ? AND removed_at IS NULL`, agentID, ownerID).Scan(&one) == nil
}

// MarkAgentsOffline sets all agents to disconnected. Called at server start; live connections reattach and become idle.
func (s *Store) MarkAgentsOffline() error {
	_, err := s.db.Exec(`UPDATE agents SET state = ?`, protocol.AgentOffline)
	return err
}

func (s *Store) SetAgentState(id, state string) (protocol.Agent, error) {
	if _, err := s.db.Exec(`UPDATE agents SET state = ?, last_seen = ? WHERE id = ?`, state, now(), id); err != nil {
		return protocol.Agent{}, err
	}
	return s.Agent(id)
}

const taskCols = `id, agent_id, title, state, created_at, updated_at, owner_id, room_id, routine_id, asked_by`

func scanTask(row interface{ Scan(...any) error }) (OwnedTask, error) {
	var t OwnedTask
	err := row.Scan(&t.ID, &t.AgentID, &t.Title, &t.State, &t.CreatedAt, &t.UpdatedAt, &t.OwnerID, &t.RoomID, &t.RoutineID, &t.AskedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// Empty fields of NewTask get defaults: a fresh ID, status running, time now.
type NewTask struct {
	ID, RoomID, RoutineID, RunID, State string
	AskedBy                             string // the room member, if the task was started by a member calling the owner's agent
	At                                  int64
}

func (s *Store) CreateTask(ownerID, agentID, title string, o NewTask) (OwnedTask, error) {
	if o.ID == "" {
		o.ID = uuid.NewString()
	}
	if o.State == "" {
		o.State = protocol.TaskRunning
	}
	if o.At == 0 {
		o.At = now()
	}
	_, err := s.db.Exec(`INSERT INTO tasks (id, owner_id, agent_id, title, state, created_at, updated_at, room_id, routine_id, run_id, asked_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.ID, ownerID, agentID, title, o.State, o.At, o.At, o.RoomID, o.RoutineID, o.RunID, o.AskedBy)
	if err != nil {
		return OwnedTask{}, err
	}
	return s.Task(o.ID)
}

func (s *Store) RoutineRunExists(agentID, routineID, runID string) bool {
	var one int
	return s.db.QueryRow(`SELECT 1 FROM tasks WHERE agent_id = ? AND routine_id = ? AND run_id = ? AND run_id != ''`, agentID, routineID, runID).Scan(&one) == nil
}

// CreateRoutineRun inserts a routine run post and its first message in one transaction.
func (s *Store) CreateRoutineRun(ownerID, agentID, title string, o NewTask, payload json.RawMessage) (OwnedTask, protocol.Event, error) {
	o.ID = cmpOr(o.ID, uuid.NewString())
	tx, err := s.db.Begin()
	if err != nil {
		return OwnedTask{}, protocol.Event{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO tasks (id, owner_id, agent_id, title, state, created_at, updated_at, room_id, routine_id, run_id, asked_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.ID, ownerID, agentID, title, o.State, o.At, o.At, o.RoomID, o.RoutineID, o.RunID, o.AskedBy); err != nil {
		return OwnedTask{}, protocol.Event{}, err
	}
	t := now()
	res, err := tx.Exec(`INSERT INTO events (task_id, source, type, payload, created_at) VALUES (?, 'agent', 'message', ?, ?)`, o.ID, string(payload), t)
	if err != nil {
		return OwnedTask{}, protocol.Event{}, err
	}
	if _, err := tx.Exec(`UPDATE tasks SET updated_at = ? WHERE id = ?`, t, o.ID); err != nil {
		return OwnedTask{}, protocol.Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return OwnedTask{}, protocol.Event{}, err
	}
	id, _ := res.LastInsertId()
	e, err := scanEvent(s.db.QueryRow(`SELECT `+eventCols+` FROM events WHERE id = ?`, id))
	if err != nil {
		return OwnedTask{}, e, err
	}
	task, err := s.Task(o.ID)
	return task, e, err
}

func cmpOr(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

// FirstAgentText returns the task's first agent message and whether the user has already spoken.
func (s *Store) FirstAgentText(taskID string) (text string, userSpoke bool) {
	s.db.QueryRow(`SELECT json_extract(payload, '$.text') FROM events WHERE task_id = ? AND source = 'agent' AND type = 'message' ORDER BY id LIMIT 1`, taskID).Scan(&text)
	var one int
	userSpoke = s.db.QueryRow(`SELECT 1 FROM events WHERE task_id = ? AND source = 'user' LIMIT 1`, taskID).Scan(&one) == nil
	return text, userSpoke
}

// EventText returns an event's kind and text within a task. ok is false if none.
func (s *Store) EventText(taskID string, eventID int64) (typ, text string, ok bool) {
	var t sql.NullString
	err := s.db.QueryRow(`SELECT type, json_extract(payload, '$.text') FROM events WHERE id = ? AND task_id = ?`, eventID, taskID).Scan(&typ, &t)
	return typ, t.String, err == nil
}

// ReactionOn reports whether that reaction is currently on for the message. Reactions accumulate as on/off events and the last one wins.
func (s *Store) ReactionOn(taskID string, target int64, emoji string) bool {
	var on sql.NullBool
	s.db.QueryRow(`SELECT json_extract(payload, '$.on') FROM events
		WHERE task_id = ? AND type = 'reaction' AND json_extract(payload, '$.target') = ? AND json_extract(payload, '$.emoji') = ?
		ORDER BY id DESC LIMIT 1`, taskID, target, emoji).Scan(&on)
	return on.Valid && on.Bool
}

func (s *Store) SaveReport(agentID string, report json.RawMessage) error {
	_, err := s.db.Exec(`UPDATE agents SET report = ?, report_at = ? WHERE id = ?`, string(report), now(), agentID)
	return err
}

func (s *Store) Report(agentID string) (json.RawMessage, int64) {
	var raw sql.NullString
	var at sql.NullInt64
	s.db.QueryRow(`SELECT report, report_at FROM agents WHERE id = ?`, agentID).Scan(&raw, &at)
	if !raw.Valid {
		return nil, 0
	}
	return json.RawMessage(raw.String), at.Int64
}

// Room feed: tasks posted directly to the room plus runs of routines registered to the room. Args: owner, room id, room agent, room id.
const roomFeed = `owner_id = ? AND hidden_at IS NULL AND (room_id = ? OR (agent_id = ? AND routine_id != '' AND routine_id IN (SELECT routine_id FROM room_routines WHERE room_id = ?)))`

// LastUserEvent is the event number of the user's last message in that conversation; 0 if none.
func (s *Store) LastUserEvent(taskID string) int64 {
	var id int64
	s.db.QueryRow(`SELECT id FROM events WHERE task_id = ? AND type IN ('task.start', 'user_message') ORDER BY id DESC LIMIT 1`, taskID).Scan(&id)
	return id
}

// liveRoom excludes rooms of unregistered agents and revoked users.
const liveRoom = `agent_id IN (SELECT id FROM agents WHERE removed_at IS NULL)
	AND owner_id NOT IN (SELECT id FROM owners WHERE revoked_at IS NOT NULL)`

func (s *Store) Task(id string) (OwnedTask, error) {
	return scanTask(s.db.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id = ?`, id))
}

func (s *Store) Tasks(ownerID, agentID string) ([]protocol.Task, error) {
	q := `SELECT ` + taskCols + ` FROM tasks WHERE owner_id = ? AND hidden_at IS NULL AND agent_id IN (SELECT id FROM agents WHERE removed_at IS NULL)`
	args := []any{ownerID}
	if agentID != "" {
		q += ` AND agent_id = ?`
		args = append(args, agentID)
	}
	rows, err := s.rdb.Query(q+` ORDER BY updated_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t.Task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	prefs, err := s.listPrefMap(ownerID, ListTasks)
	if err != nil {
		return nil, err
	}
	for i := range out {
		p := prefs[out[i].ID]
		out[i].Pinned, out[i].GroupID = p.Pinned, p.GroupID
	}
	return out, nil
}

// Who set the title: first message (auto), agent (agent), user (user)
const (
	TitleAuto  = "auto"
	TitleAgent = "agent"
	TitleUser  = "user"
)

// SetTaskTitle changes the title and reports whether it changed. An agent cannot overwrite a user-set title, and deleted conversations are not changed.
func (s *Store) SetTaskTitle(id, title, source string) (bool, error) {
	q := `UPDATE tasks SET title = ?, title_source = ? WHERE id = ? AND hidden_at IS NULL AND (title != ? OR title_source != ?)`
	if source != TitleUser {
		q += ` AND title_source != 'user'`
	}
	res, err := s.db.Exec(q, title, source, id, title, source)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// TitleSource is who set the title; empty string if the conversation does not exist.
func (s *Store) TitleSource(id string) string {
	var src string
	s.db.QueryRow(`SELECT title_source FROM tasks WHERE id = ?`, id).Scan(&src)
	return src
}

func (s *Store) SetTaskState(id, state string) (protocol.Task, error) {
	if _, err := s.db.Exec(`UPDATE tasks SET state = ?, updated_at = ? WHERE id = ?`, state, now(), id); err != nil {
		return protocol.Task{}, err
	}
	t, err := s.Task(id)
	return t.Task, err
}

const eventCols = `id, task_id, source, type, payload, created_at`

func scanEvent(row interface{ Scan(...any) error }) (protocol.Event, error) {
	var e protocol.Event
	var payload string
	err := row.Scan(&e.ID, &e.TaskID, &e.Source, &e.Type, &payload, &e.CreatedAt)
	e.Payload = json.RawMessage(payload)
	return e, err
}

func (s *Store) AddEvent(taskID, source, typ string, payload json.RawMessage) (protocol.Event, error) {
	e, _, err := s.AddEventWithState(taskID, source, typ, payload, "")
	return e, err
}

// AddEventWithState records an event and bumps the task time. If state is given, the task state is changed too and returned. One commit.
func (s *Store) AddEventWithState(taskID, source, typ string, payload json.RawMessage, state string) (protocol.Event, protocol.Task, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return protocol.Event{}, protocol.Task{}, err
	}
	defer tx.Rollback()
	t := now()
	res, err := tx.Exec(`INSERT INTO events (task_id, source, type, payload, created_at) VALUES (?, ?, ?, ?, ?)`,
		taskID, source, typ, string(payload), t)
	if err != nil {
		return protocol.Event{}, protocol.Task{}, err
	}
	if state == "" {
		_, err = tx.Exec(`UPDATE tasks SET updated_at = ? WHERE id = ?`, t, taskID)
	} else {
		_, err = tx.Exec(`UPDATE tasks SET state = ?, updated_at = ? WHERE id = ?`, state, t, taskID)
	}
	if err != nil {
		return protocol.Event{}, protocol.Task{}, err
	}
	id, _ := res.LastInsertId()
	e, err := scanEvent(tx.QueryRow(`SELECT `+eventCols+` FROM events WHERE id = ?`, id))
	if err != nil {
		return e, protocol.Task{}, err
	}
	var task OwnedTask
	if state != "" {
		if task, err = scanTask(tx.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id = ?`, taskID)); err != nil {
			return e, task.Task, err
		}
	}
	return e, task.Task, tx.Commit()
}

func queryEvents(db *sql.DB, q string, args ...any) ([]protocol.Event, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SearchEvents searches my messages and agent replies, newest first, up to limit. Deleted conversations and unregistered agents are excluded.
func (s *Store) SearchEvents(ownerID, q string, limit int) ([]protocol.Event, error) {
	like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
	return queryEvents(s.rdb, `SELECT e.id, e.task_id, e.source, e.type, e.payload, e.created_at
FROM events e JOIN tasks t ON t.id = e.task_id
WHERE t.owner_id = ? AND t.hidden_at IS NULL AND t.agent_id IN (SELECT id FROM agents WHERE removed_at IS NULL)
  AND e.type IN ('task.start', 'user_message', 'message')
  AND json_extract(e.payload, '$.text') LIKE ? ESCAPE '\'
ORDER BY e.id DESC LIMIT ?`, ownerID, like, limit)
}

func (s *Store) Events(taskID string) ([]protocol.Event, error) {
	return queryEvents(s.rdb, `SELECT `+eventCols+` FROM events WHERE task_id = ? ORDER BY id`, taskID)
}

// PendingApprovals returns approval requests not yet answered in waiting tasks.
func (s *Store) PendingApprovals(ownerID string) ([]protocol.Event, error) {
	return queryEvents(s.db, pendingApprovalsQuery, ownerID)
}

const pendingApprovalsQuery = `
SELECT e.id, e.task_id, e.source, e.type, e.payload, e.created_at
FROM events e JOIN tasks t ON t.id = e.task_id
WHERE t.owner_id = ? AND e.type = 'approval' AND t.state = 'waiting'
  AND t.agent_id IN (SELECT id FROM agents WHERE removed_at IS NULL)
  AND NOT ` + approvalClosed + `
ORDER BY e.id`

// approvalClosed: a later result or newer request with e's id closes e (ids can repeat in a task).
const approvalClosed = `EXISTS (
    SELECT 1 FROM events r
    WHERE r.task_id = e.task_id AND r.type IN ('approval_result', 'approval') AND r.id > e.id
      AND json_extract(r.payload, '$.id') = json_extract(e.payload, '$.id'))`

// LastAgentText is the text of the task's last agent message; empty string if none.
func (s *Store) LastAgentText(taskID string) string {
	var text sql.NullString
	logLookup("LastAgentText", s.db.QueryRow(`SELECT json_extract(payload, '$.text') FROM events WHERE task_id = ? AND source = 'agent' AND type = 'message' ORDER BY id DESC LIMIT 1`, taskID).Scan(&text))
	return text.String
}
