package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/store"
)

// Renaming a room is saved under the hub lock. While the lock is held the DB is unchanged (so save and delivery order match on every screen).
func TestRoomRenameSavesUnderHubLock(t *testing.T) {
	srv := newLocal(t)
	a, _, err := srv.store.CreateAgent(store.Owner, "Agent A")
	if err != nil {
		t.Fatal(err)
	}
	room, err := srv.store.CreateRoom(store.Owner, "Morning", a.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.hub.mu.Lock()
	done := make(chan int, 1)
	go func() {
		req := httptest.NewRequest("PATCH", "/api/rooms/"+room.ID, strings.NewReader(`{"name":"Evening"}`))
		req.Header.Set("Authorization", "Bearer "+testOwner)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		done <- rec.Code
	}()
	time.Sleep(200 * time.Millisecond)
	held, _, _ := srv.store.Room(room.ID)
	srv.hub.mu.Unlock()
	if held.Name != "Morning" {
		t.Fatalf("saved outside the hub lock: %q", held.Name)
	}
	if code := <-done; code != http.StatusOK {
		t.Fatalf("rename: %d", code)
	}
	if after, _, _ := srv.store.Room(room.ID); after.Name != "Evening" {
		t.Fatalf("not saved after the lock was released: %q", after.Name)
	}
}

// During shutdown, an open task is not turned into failed even when the 1-minute grace ends (the new process takes over).
func TestAgentGoneSkipsWhileClosing(t *testing.T) {
	srv := newLocal(t)
	task := func() string {
		a, _, err := srv.store.CreateAgent(store.Owner, "Agent A")
		if err != nil {
			t.Fatal(err)
		}
		tk, err := srv.store.CreateTask(store.Owner, a.ID, "Job", store.NewTask{})
		if err != nil {
			t.Fatal(err)
		}
		return tk.ID
	}
	status := func(id string) string {
		tk, err := srv.store.Task(id)
		if err != nil {
			t.Fatal(err)
		}
		return tk.State
	}
	live := task()
	srv.hub.agentGone(srv.mustTaskAgent(t, live))
	if s := status(live); s != protocol.TaskFailed {
		t.Fatalf("normally turned into failed: %s", s)
	}
	closing := task()
	srv.hub.mu.Lock()
	srv.hub.closing = true
	srv.hub.mu.Unlock()
	srv.hub.agentGone(srv.mustTaskAgent(t, closing))
	if s := status(closing); s != protocol.TaskRunning {
		t.Fatalf("changed the task during shutdown: %s", s)
	}
}

func (s *Server) mustTaskAgent(t *testing.T, taskID string) string {
	tk, err := s.store.Task(taskID)
	if err != nil {
		t.Fatal(err)
	}
	return tk.AgentID
}

// Close waits until the same-computer Hermes adapter goroutine finishes.
func TestCloseWaitsForLocalAdapter(t *testing.T) {
	srv, err := New(Options{DBPath: ":memory:", OwnerToken: testOwner, UploadDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var finished atomic.Bool
	var once sync.Once
	started := make(chan struct{})
	path := filepath.Join(t.TempDir(), "hermes-agent")
	srv.SetLocalHermes(LocalHermes{
		Name:   "Hermes",
		IDPath: path,
		Find:   func() (bool, bool) { return true, true },
		Probe:  func(context.Context) string { return "" },
		Start: func(ctx context.Context, token string) {
			once.Do(func() { close(started) })
			<-ctx.Done()
			time.Sleep(100 * time.Millisecond) // time it takes to finish
			finished.Store(true)
		},
	})
	if _, err := srv.LocalAgent("Hermes", path); err != nil {
		t.Fatal(err)
	}
	if ok, err := srv.ResumeLocalHermes(); err != nil || !ok {
		t.Fatalf("resume: %v %v", ok, err)
	}
	<-started
	srv.Close()
	if !finished.Load() {
		t.Fatal("Close did not wait for the adapter")
	}
}
