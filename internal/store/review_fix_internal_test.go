package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

func openFile(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "k.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestForeignKeysOnEveryConnection(t *testing.T) {
	s, _ := openFile(t)
	s.db.SetMaxIdleConns(0) // new connection per query
	for i := 0; i < 3; i++ {
		var on, busy int
		s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&on)
		s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busy)
		if on != 1 || busy != 5000 {
			t.Fatalf("conn %d: foreign_keys=%d busy_timeout=%d", i, on, busy)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO tasks (id, owner_id, agent_id, title, state, created_at, updated_at) VALUES ('t', 'me', 'nope', 'x', 'done', 1, 1)`); err == nil {
		t.Fatal("dangling agent_id accepted")
	}
}

func plan(t *testing.T, s *Store, q string, args ...any) string {
	t.Helper()
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a, b, c int
		var d string
		rows.Scan(&a, &b, &c, &d)
		out = append(out, d)
	}
	return strings.Join(out, "; ")
}

func TestHotQueriesUseIndexes(t *testing.T) {
	s, _ := openFile(t)
	if p := plan(t, s, pendingApprovalsQuery, Owner); strings.Contains(p, "SCAN e") {
		t.Fatalf("PendingApprovals scans events: %s", p)
	}
	if p := plan(t, s, `SELECT 1 FROM tasks WHERE agent_id = ? AND routine_id = ? AND run_id = ? AND run_id != ''`, "a", "r", "x"); !strings.Contains(p, "tasks_by_run") {
		t.Fatalf("RoutineRunExists: %s", p)
	}
	if p := plan(t, s, `SELECT id FROM tasks WHERE `+roomFeed+` ORDER BY created_at DESC LIMIT 200`, Owner, "r", "a", "r"); !strings.Contains(p, "tasks_by_owner_created") {
		t.Fatalf("RoomPosts: %s", p)
	}
}

func TestRoomPostsSameWithIndex(t *testing.T) {
	s, _ := openFile(t)
	ag, _, _ := s.CreateAgent(Owner, "a")
	room, _ := s.CreateRoom(Owner, "r", ag.ID, []string{"r1"})
	for i := 0; i < 300; i++ {
		o := NewTask{At: int64(1000 + i), State: protocol.TaskDone}
		switch i % 3 {
		case 0:
			o.RoutineID, o.RunID = "r1", fmt.Sprint("run", i)
		case 1:
			o.RoomID = room.ID
		}
		task, err := s.CreateTask(Owner, ag.ID, "x", o)
		if err != nil {
			t.Fatal(err)
		}
		s.AddEvent(task.ID, "agent", "message", []byte(fmt.Sprintf(`{"text":"m%d"}`, i)))
	}
	r, _, _ := s.Room(room.ID)
	with, err := s.RoomPosts(r, Owner, 100)
	if err != nil {
		t.Fatal(err)
	}
	s.db.Exec(`DROP INDEX tasks_by_owner_created`)
	without, _ := s.RoomPosts(r, Owner, 100)
	if len(with) != 100 || !reflect.DeepEqual(with, without) {
		t.Fatalf("results differ: %d vs %d", len(with), len(without))
	}
}

func TestRoutineRunIsAtomic(t *testing.T) {
	s, _ := openFile(t)
	ag, _, _ := s.CreateAgent(Owner, "a")
	s.db.Exec(`CREATE TRIGGER no_events BEFORE INSERT ON events BEGIN SELECT RAISE(FAIL, 'boom'); END`)
	if _, _, err := s.CreateRoutineRun(Owner, ag.ID, "t", NewTask{RoutineID: "r", RunID: "1", State: protocol.TaskDone, At: 1}, []byte(`{"text":"x"}`)); err == nil {
		t.Fatal("expected failure")
	}
	if s.RoutineRunExists(ag.ID, "r", "1") {
		t.Fatal("task without its message must not stay")
	}
	s.db.Exec(`DROP TRIGGER no_events`)
	task, e, err := s.CreateRoutineRun(Owner, ag.ID, "t", NewTask{RoutineID: "r", RunID: "1", State: protocol.TaskDone, At: 1}, []byte(`{"text":"x"}`))
	if err != nil || e.TaskID != task.ID || e.Type != "message" || !s.RoutineRunExists(ag.ID, "r", "1") {
		t.Fatalf("retry: %v %+v", err, e)
	}
}

func TestDeleteRoomIsAtomic(t *testing.T) {
	s, _ := openFile(t)
	ag, _, _ := s.CreateAgent(Owner, "a")
	room, _ := s.CreateRoom(Owner, "r", ag.ID, nil)
	id, _, _ := s.CreateOwner("Bob")
	s.AddRoomMember(room.ID, id)
	s.db.Exec(`CREATE TRIGGER keep_room BEFORE DELETE ON rooms BEGIN SELECT RAISE(FAIL, 'boom'); END`)
	if _, err := s.DeleteRoom(room.ID); err == nil {
		t.Fatal("expected failure")
	}
	if !s.IsRoomMember(room.ID, id) {
		t.Fatal("members removed though the room stayed")
	}
}

func TestUIReadsReportDBErrors(t *testing.T) {
	s, _ := openFile(t)
	s.Close()
	if _, err := s.Screens("a"); err == nil {
		t.Fatal("Screens hid the error")
	}
	if _, _, err := s.Tabs("a"); err == nil {
		t.Fatal("Tabs hid the error")
	}
	if _, err := s.Data("a"); err == nil {
		t.Fatal("Data hid the error")
	}
	if _, err := s.TaskAudience("o", "t"); err == nil {
		t.Fatal("TaskAudience hid the error")
	}
}

func TestDuplicateRunsDoNotBlockOpen(t *testing.T) {
	s, path := openFile(t)
	ag, _, _ := s.CreateAgent(Owner, "a")
	s.db.Exec(`DROP INDEX tasks_by_run`)
	for _, id := range []string{"t1", "t2"} {
		if _, err := s.CreateTask(Owner, ag.ID, "x", NewTask{ID: id, RoutineID: "r", RunID: "same", State: protocol.TaskDone, At: 1}); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	again, err := Open(path)
	if err != nil {
		t.Fatalf("open with duplicate runs: %v", err)
	}
	defer again.Close()
	var n, kept int
	again.db.QueryRow(`SELECT count(*), sum(run_id = 'same') FROM tasks`).Scan(&n, &kept)
	if n != 2 || kept != 1 {
		t.Fatalf("rows=%d kept=%d", n, kept)
	}
	var first string
	again.db.QueryRow(`SELECT id FROM tasks WHERE run_id = 'same'`).Scan(&first)
	if first != "t1" {
		t.Fatalf("earliest should keep the run: %s", first)
	}
}

func TestSaveDeviceKeepsOtherOwnersToken(t *testing.T) {
	s, _ := openFile(t)
	other, _, _ := s.CreateOwner("Bob")
	d := Device{Token: "aa", Platform: "ios", Env: "sandbox"}
	if err := s.SaveDevice(Owner, d); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveDevice(other, d); !errors.Is(err, ErrDeviceTaken) { // tries to take someone else's token
		t.Fatalf("err %v", err)
	}
	if ds, _ := s.Devices(other); len(ds) != 0 {
		t.Fatal("another user took someone else's device")
	}
	if ds, _ := s.Devices(Owner); len(ds) != 1 {
		t.Fatal("the original owner's device disappeared")
	}
	s.DeleteDevice(Owner, d.Token) // unregistered when the server is removed
	if err := s.SaveDevice(other, d); err != nil {
		t.Fatalf("after unregistering, the new account can register: %v", err)
	}
	s.DeleteDevice(other, d.Token)
	if err := s.SaveDevice(Owner, d); err != nil {
		t.Fatal(err)
	}
	d.Env = "production"
	if err := s.SaveDevice(Owner, d); err != nil {
		t.Fatal(err)
	}
	if ds, _ := s.Devices(Owner); len(ds) != 1 || ds[0].Env != "production" {
		t.Fatalf("update: %+v", ds)
	}
}

func TestUploadsUsedBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ag, _, _ := s.CreateAgent(Owner, "a")
	attached, _ := s.CreateUpload(Owner, "a", "text/plain", 1)
	loose, _ := s.CreateUpload(Owner, "b", "text/plain", 1)
	task, _ := s.CreateTask(Owner, ag.ID, "x", NewTask{})
	s.AddEvent(task.ID, "user", "task.start", []byte(`{"attachments":[{"id":"`+attached.ID+`"}]}`))
	if _, err := s.db.Exec(`ALTER TABLE uploads DROP COLUMN used_at`); err != nil { // imitate an old DB
		t.Fatal(err)
	}
	s.Close()
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ids, err := s.StaleUploads(now() + 1)
	if err != nil || len(ids) != 1 || ids[0] != loose.ID {
		t.Fatalf("stale: %v %v", ids, err)
	}
}

func TestUploadBytesCountsOnlyUnsent(t *testing.T) {
	s, _ := openFile(t)
	a, err := s.CreateUpload(Owner, "a.png", "image/png", 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUpload(Owner, "b.png", "image/png", 50); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.UploadBytes(Owner); n != 150 {
		t.Fatalf("before send: %d", n)
	}
	if err := s.AttachUploads("", []protocol.Attachment{a}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.UploadBytes(Owner); n != 50 { // sent attachments leave the quota
		t.Fatalf("after send: %d", n)
	}
}

// Attached files in an old DB (no task_id) stay fetchable by the owner's agents; newly attached files only by that task's agent.
func TestUploadTaskBoundaryAfterMigration(t *testing.T) {
	s, path := openFile(t)
	a1, _, _ := s.CreateAgent(Owner, "a1")
	a2, _, _ := s.CreateAgent(Owner, "a2")
	other, _, _ := s.CreateOwner("Carol")
	a3, _, _ := s.CreateAgent(other, "a3")
	old, _ := s.CreateUpload(Owner, "old", "text/plain", 1)
	if _, err := s.db.Exec(`UPDATE uploads SET used_at = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE uploads DROP COLUMN task_id`); err != nil { // imitate an old DB
		t.Fatal(err)
	}
	s.Close()
	var err error
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	auth := func(a protocol.Agent, owner string) AgentAuth { return AgentAuth{ID: a.ID, OwnerID: owner} }
	if !s.AgentCanFetchUpload(old.ID, auth(a1, Owner)) || !s.AgentCanFetchUpload(old.ID, auth(a2, Owner)) {
		t.Fatal("legacy attached upload: owner's agents must still fetch")
	}
	if s.AgentCanFetchUpload(old.ID, auth(a3, other)) {
		t.Fatal("legacy upload leaked to another owner's agent")
	}
	fresh, _ := s.CreateUpload(Owner, "new", "text/plain", 1)
	if s.AgentCanFetchUpload(fresh.ID, auth(a1, Owner)) {
		t.Fatal("unsent upload fetched by agent")
	}
	task, _ := s.CreateTask(Owner, a1.ID, "x", NewTask{})
	if err := s.AttachUploads(task.ID, []protocol.Attachment{fresh, old}); !errors.Is(err, ErrUploadUsed) {
		t.Fatalf("re-attaching a legacy upload: %v", err)
	}
	if s.AgentCanFetchUpload(fresh.ID, auth(a1, Owner)) {
		t.Fatal("rejected batch must not attach any upload")
	}
	if err := s.AttachUploads(task.ID, []protocol.Attachment{fresh}); err != nil {
		t.Fatal(err)
	}
	if !s.AgentCanFetchUpload(fresh.ID, auth(a1, Owner)) || s.AgentCanFetchUpload(fresh.ID, auth(a2, Owner)) {
		t.Fatal("attached upload: only the task's agent")
	}
	if !s.AgentCanFetchUpload(old.ID, auth(a2, Owner)) {
		t.Fatal("re-attaching a legacy upload must not narrow it")
	}
}

// An old DB also gets the index at start, and task lookups use it.
func TestIndexesCreatedOnOldDB(t *testing.T) {
	s, path := openFile(t)
	names := []string{"tasks_by_agent_state", "tasks_by_room", "agents_by_owner", "rooms_by_owner", "rooms_by_agent", "room_members_by_owner"}
	for _, n := range names {
		if _, err := s.db.Exec(`DROP INDEX ` + n); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	var err error
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, n := range names {
		var k int
		s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, n).Scan(&k)
		if k != 1 {
			t.Errorf("index %s missing", n)
		}
	}
	var plan, detail string
	var id, parent, unused int
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN SELECT id FROM tasks WHERE agent_id = ? AND state = ?`, "a", "running")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		rows.Scan(&id, &parent, &unused, &detail)
		plan += detail + ";"
	}
	rows.Close()
	if !strings.Contains(plan, "tasks_by_agent_state") {
		t.Fatalf("plan: %s", plan)
	}
}

// If saving the state fails, the event is not kept either (one transaction).
func TestAddEventWithStateIsAtomic(t *testing.T) {
	s, _ := openFile(t)
	ag, _, _ := s.CreateAgent(Owner, "a")
	task, _ := s.CreateTask(Owner, ag.ID, "x", NewTask{})
	if _, err := s.db.Exec(`CREATE TRIGGER no_state BEFORE UPDATE OF state ON tasks BEGIN SELECT RAISE(ABORT, 'no'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AddEventWithState(task.ID, "agent", "task.end", []byte(`{}`), "done"); err == nil {
		t.Fatal("state failure hidden")
	}
	if ev, _ := s.Events(task.ID); len(ev) != 0 {
		t.Fatalf("event kept after failed state: %d", len(ev))
	}
	s.db.Exec(`DROP TRIGGER no_state`)
	e, got, err := s.AddEventWithState(task.ID, "agent", "task.end", []byte(`{}`), "done")
	if err != nil || got.State != "done" || e.TaskID != task.ID {
		t.Fatalf("%+v %+v %v", e, got, err)
	}
}

// A file DB's read connection cannot write, does not wait even with a write transaction open, and sees committed writes immediately.
func TestReadPoolOnFileDB(t *testing.T) {
	s, _ := openFile(t)
	if s.rdb == s.db {
		t.Fatal("file DB should have a separate read pool")
	}
	if _, err := s.rdb.Exec(`INSERT INTO owners (id, name) VALUES ('x', 'x')`); err == nil {
		t.Fatal("read pool accepted a write")
	}
	ag, _, _ := s.CreateAgent(Owner, "a")
	task, _ := s.CreateTask(Owner, ag.ID, "x", NewTask{})
	if _, err := s.AddEvent(task.ID, "agent", "message", []byte(`{"text":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	if ev, err := s.Events(task.ID); err != nil || len(ev) != 1 {
		t.Fatalf("read after write: %d %v", len(ev), err)
	}
	if ts, err := s.Tasks(Owner, ag.ID); err != nil || len(ts) != 1 {
		t.Fatalf("tasks after write: %d %v", len(ts), err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE tasks SET title = 'y' WHERE id = ?`, task.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.Events(task.ID); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read waited for the open write transaction")
	}
}

func TestMemoryDBSharesOneConnection(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.rdb != s.db {
		t.Fatal(":memory: must stay on one connection")
	}
}
