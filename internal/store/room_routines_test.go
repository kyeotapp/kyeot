package store_test

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kyeotapp/kyeot/internal/store"
)

func reopen(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

func routinesOf(t *testing.T, s *store.Store, id string) []string {
	t.Helper()
	r, _, err := s.Room(id)
	if err != nil {
		t.Fatal(err)
	}
	return r.RoutineIDs
}

func TestRoomRoutineMigrationIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.db")
	s := reopen(t, path)
	a, _, _ := s.CreateAgent(store.Owner, "Agent A")
	s.Close()

	// room made by an old server: only rooms.routine_id is set and room_routines is empty
	raw, _ := sql.Open("sqlite", path)
	if _, err := raw.Exec(`INSERT INTO rooms (id, owner_id, name, agent_id, routine_id, created_at) VALUES ('r1', ?, 'Contest', ?, 'job1', 1)`, store.Owner, a.ID); err != nil {
		t.Fatal(err)
	}
	raw.Exec(`DELETE FROM room_routines`)
	raw.Close()

	for i := 0; i < 3; i++ {
		s = reopen(t, path)
		if got := routinesOf(t, s, "r1"); !slices.Equal(got, []string{"job1"}) {
			t.Fatalf("open %d: %v", i, got)
		}
		s.Close()
	}
	raw, _ = sql.Open("sqlite", path)
	var n int
	raw.QueryRow(`SELECT count(*) FROM room_routines WHERE room_id = 'r1'`).Scan(&n)
	raw.Close()
	if n != 1 {
		t.Fatalf("%d rows migrated", n)
	}

	// a user-changed list does not come back from the old column after reopening
	s = reopen(t, path)
	if r, err := s.SetRoomRoutines("r1", []string{"b", "c"}); err != nil || r.RoutineID != "b" {
		t.Fatalf("set: %+v %v", r, err)
	}
	s.Close()
	s = reopen(t, path)
	if got := routinesOf(t, s, "r1"); !slices.Equal(got, []string{"b", "c"}) {
		t.Fatalf("after reopen: %v", got)
	}
	s.SetRoomRoutines("r1", []string{})
	s.Close()
	s = reopen(t, path)
	defer s.Close()
	if r, _, _ := s.Room("r1"); len(r.RoutineIDs) != 0 || r.RoutineID != "" {
		t.Fatalf("emptied: %+v", r)
	}
}

func TestRoomFeedCollectsAllRoutines(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, _, _ := s.CreateAgent(store.Owner, "Agent A")
	b, _, _ := s.CreateAgent(store.Owner, "Agent B")
	room, err := s.CreateRoom(store.Owner, "News", a.ID, []string{"news", "weather"})
	if err != nil || !slices.Equal(room.RoutineIDs, []string{"news", "weather"}) || room.RoutineID != "news" {
		t.Fatalf("create: %+v %v", room, err)
	}
	for i, x := range []struct{ agent, routine string }{{a.ID, "news"}, {a.ID, "weather"}, {a.ID, "other"}, {b.ID, "news"}} {
		if _, err := s.CreateTask(store.Owner, x.agent, x.routine, store.NewTask{RoutineID: x.routine, RunID: string(rune('a' + i))}); err != nil {
			t.Fatal(err)
		}
	}
	posts, _ := s.RoomPosts(room, store.Owner, 50)
	var got []string
	for _, p := range posts {
		got = append(got, p.Task.AgentID+"/"+p.Task.RoutineID)
	}
	slices.Sort(got)
	if want := []string{a.ID + "/news", a.ID + "/weather"}; !slices.Equal(got, want) {
		t.Fatalf("feed %v, want %v", got, want)
	}
	if _, err := s.DeleteRoom(room.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestRoomPostReplies(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, _, _ := s.CreateAgent(store.Owner, "Agent A")
	room, _ := s.CreateRoom(store.Owner, "News", a.ID, []string{"news"})
	msg := json.RawMessage(`{"text":"x"}`)
	add := func(task, source, typ string) {
		t.Helper()
		if _, err := s.AddEvent(task, source, typ, msg); err != nil {
			t.Fatal(err)
		}
	}
	mine, _ := s.CreateTask(store.Owner, a.ID, "Tomorrow weather?", store.NewTask{RoomID: room.ID})
	add(mine.ID, "user", "task.start")
	add(mine.ID, "agent", "tool.started") // tool runs are not replies
	add(mine.ID, "agent", "message")
	add(mine.ID, "user", "user_message")
	add(mine.ID, "agent", "message")
	run, _ := s.CreateTask(store.Owner, a.ID, "news", store.NewTask{RoutineID: "news", RunID: "r1"})
	add(run.ID, "agent", "message") // routine results are the body
	quiet, _ := s.CreateTask(store.Owner, a.ID, "news", store.NewTask{RoutineID: "news", RunID: "r2"})
	add(quiet.ID, "agent", "message")
	add(quiet.ID, "user", "user_message")
	add(quiet.ID, "agent", "message")

	posts, _ := s.RoomPosts(room, store.Owner, 50)
	got := map[string]int{}
	for _, p := range posts {
		got[p.Task.ID] = p.Replies
	}
	if got[mine.ID] != 3 || got[run.ID] != 0 || got[quiet.ID] != 2 {
		t.Fatalf("replies: mine=%d run=%d quiet=%d", got[mine.ID], got[run.ID], got[quiet.ID])
	}
}

// The thread card shows the first agent reply (routine result). preview stays the last reply for old apps.
func TestRoomPostFirstAndPreview(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, _, _ := s.CreateAgent(store.Owner, "Agent A")
	room, _ := s.CreateRoom(store.Owner, "Vacant", a.ID, []string{"watch"})
	run, _ := s.CreateTask(store.Owner, a.ID, "watch", store.NewTask{RoutineID: "watch", RunID: "r1"})
	for _, e := range []struct{ src, typ, text string }{{"agent", "message", "The watch failed 3 times in a row"}, {"user", "user_message", "What is the cause?"}, {"agent", "message", "The stream dropped"}} {
		if _, err := s.AddEvent(run.ID, e.src, e.typ, json.RawMessage(`{"text":"`+e.text+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	posts, _ := s.RoomPosts(room, store.Owner, 50)
	if len(posts) != 1 || posts[0].First != "The watch failed 3 times in a row" || posts[0].Preview != "The stream dropped" {
		t.Fatalf("first=%q preview=%q", posts[0].First, posts[0].Preview)
	}
}
