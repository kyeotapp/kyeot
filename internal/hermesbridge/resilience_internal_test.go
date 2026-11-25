package hermesbridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/cron"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

func TestEventsIdleTimeoutResumesFromLastSeq(t *testing.T) {
	var mu sync.Mutex
	var lastIDs []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lastIDs = append(lastIDs, r.Header.Get("Last-Event-ID"))
		n := len(lastIDs)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		if n == 1 {
			fmt.Fprint(w, "id: 0\ndata: {\"event\":\"message.delta\",\"delta\":\"one\"}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done() // half-dead connection: nothing arrives
			return
		}
		fmt.Fprint(w, "id: 1\ndata: {\"event\":\"run.completed\",\"output\":\"done\"}\n\n")
	}))
	t.Cleanup(func() { ts.CloseClientConnections(); ts.Close() })

	h := &hermes{base: ts.URL, http: &http.Client{}, idle: 150 * time.Millisecond, retry: 10 * time.Millisecond}
	var got []string
	done := make(chan error, 1)
	go func() {
		done <- h.events(context.Background(), "run_1", func(ev hermesEvent) bool {
			got = append(got, ev.Event)
			return ev.Event != "run.completed"
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("events: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SSE stayed silent but did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(got, ",") != "message.delta,run.completed" || len(lastIDs) != 2 || lastIDs[0] != "" || lastIDs[1] != "0" {
		t.Fatalf("events %v, Last-Event-ID %q", got, lastIDs)
	}
}

func TestEventsGivesUpAfterRetries(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, ": keepalive\n\n")
	}))
	defer ts.Close()
	h := &hermes{base: ts.URL, http: &http.Client{}, idle: 100 * time.Millisecond, retry: 5 * time.Millisecond}
	err := h.events(context.Background(), "run_1", func(hermesEvent) bool { return true })
	if err == nil || calls != streamRetries+1 {
		t.Fatalf("err %v calls %d", err, calls)
	}
}

func TestSessionReportsRevokedToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer ts.Close()
	b := New(Config{DeckURL: ts.URL, AgentToken: "x"})
	if err := b.session(context.Background()); !errors.Is(err, ErrAgentRevoked) {
		t.Fatalf("err: %v", err)
	}
}

func TestCronRejectsOutOfRange(t *testing.T) {
	for _, expr := range []string{"0 9 * * -1", "0 9 * * 8", "0 9 * * 1,-1"} {
		_ = cron.Text(expr) // must not panic
	}
	if _, err := cron.Field("0-2000000000", 0, 59); err == nil {
		t.Fatal("accepted a huge range")
	}
	if _, err := cron.Field("5-1", 0, 59); err == nil {
		t.Fatal("accepted a reversed range")
	}
	if runs := cron.Runs("0 9 * * 99", time.Now(), time.Now().Add(48*time.Hour)); runs != nil {
		t.Fatalf("runs: %v", runs)
	}
}

func TestClipText(t *testing.T) {
	s := strings.Repeat("가", 100)
	got := clipText(s, 10)
	if !strings.HasPrefix(got, "가가가") || !strings.HasSuffix(got, "…(잘림)") || strings.ContainsRune(got, '�') {
		t.Fatalf("clip: %q", got)
	}
	if clipText("짧다", 10) != "짧다" {
		t.Fatal("cut a short text")
	}
}

func TestDownloadRefusesTooBig(t *testing.T) {
	body := strings.Repeat("x", maxUpload+1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
	defer ts.Close()
	b := New(Config{DeckURL: ts.URL})
	dir := t.TempDir()
	if _, err := b.download(context.Background(), dir, protocol.Attachment{ID: "abcdefgh1", Name: "big.bin", URL: "/api/uploads/a", Size: maxUpload + 1}); !errors.Is(err, errTooBig) {
		t.Fatalf("declared size: %v", err)
	}
	if _, err := b.download(context.Background(), dir, protocol.Attachment{ID: "abcdefgh2", Name: "liar.bin", URL: "/api/uploads/b", Size: 10}); !errors.Is(err, errTooBig) {
		t.Fatalf("actual size: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a truncated file remained: %v", entries)
	}
}

func TestAttachNoteHidesURL(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	ts.Close()
	b := New(Config{DeckURL: ts.URL, HermesHome: t.TempDir()})
	note := b.attachNote(context.Background(), []protocol.Attachment{{ID: "abcdefgh", Name: "a.pdf", URL: "/api/uploads/x?sig=secret"}})
	if !strings.Contains(note, "받지 못함") || strings.Contains(note, "sig=") || strings.Contains(note, "http") {
		t.Fatalf("note: %q", note)
	}
}

func TestCleanUploadsRemovesOld(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "kyeot-uploads")
	os.MkdirAll(dir, 0o700)
	old, fresh := filepath.Join(dir, "old"), filepath.Join(dir, "fresh")
	os.WriteFile(old, []byte("x"), 0o600)
	os.WriteFile(fresh, []byte("x"), 0o600)
	past := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(old, past, past)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	New(Config{HermesHome: home}).cleanUploads(ctx)
	if _, err := os.Stat(old); err == nil {
		t.Fatal("an old attachment remained")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("deleted a new attachment")
	}
}

func TestScriptOutsideScriptsFolderIsNotRead(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "secret.py"), []byte(`"""secret"""`), 0o600)
	if info := scriptInfo(home, "../secret.py"); info != nil {
		t.Fatalf("info: %+v", info)
	}
	os.MkdirAll(filepath.Join(home, "cron"), 0o700)
	os.WriteFile(filepath.Join(home, "cron", "jobs.json"), []byte(`{"jobs":[{"id":"j","script":"../secret.py","no_agent":true,"schedule":{"kind":"cron","expr":"0 9 * * -1"}}]}`), 0o600)
	r := BuildReport(home, time.Now())
	if len(r.Routines) != 1 || r.Routines[0].Script != "" || r.Routines[0].Info != nil {
		t.Fatalf("routine: %+v", r.Routines)
	}
}

func TestReadTextIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.py")
	os.WriteFile(path, []byte(strings.Repeat("a", 1<<20)), 0o600)
	if n := len(readText(path)); n != 256<<10 {
		t.Fatalf("read %d", n)
	}
}
