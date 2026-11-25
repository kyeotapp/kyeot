package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/store"
)

func TestSweepDropsUnattachedUploads(t *testing.T) {
	srv := newLocal(t)
	dir := srv.uploadDir()
	os.MkdirAll(dir, 0o700)
	stale, _ := srv.store.CreateUpload(store.Owner, "a", "text/plain", 1)
	used, _ := srv.store.CreateUpload(store.Owner, "b", "text/plain", 1)
	for _, id := range []string{stale.ID, used.ID} {
		os.WriteFile(filepath.Join(dir, id), []byte("x"), 0o600)
	}
	srv.store.AttachUploads("", []protocol.Attachment{used})
	srv.sweepUploads(time.Now().Add(-time.Hour))
	if _, _, err := srv.store.Upload(stale.ID); err != nil {
		t.Fatal("young uploads stay")
	}
	srv.sweepUploads(time.Now().Add(time.Hour))
	if _, _, err := srv.store.Upload(stale.ID); err == nil {
		t.Fatal("stale row should be gone")
	}
	if _, err := os.Stat(filepath.Join(dir, stale.ID)); !os.IsNotExist(err) {
		t.Fatal("stale file should be gone")
	}
	if _, _, err := srv.store.Upload(used.ID); err != nil {
		t.Fatal("attached upload must stay")
	}
	if _, err := os.Stat(filepath.Join(dir, used.ID)); err != nil {
		t.Fatal("attached file must stay")
	}
}

func TestTranscribeConcurrencyLimit(t *testing.T) {
	srv, err := New(Options{DBPath: ":memory:", OwnerToken: testOwner, UploadDir: t.TempDir(), WhisperModel: "m"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	for range maxTranscribes {
		srv.transcribing <- struct{}{}
	}
	if code, _ := localCall(t, srv, "POST", "/api/transcribe", testOwner); code != http.StatusTooManyRequests {
		t.Fatalf("over limit: %d", code)
	}
}

func TestLocalHermesStatusNotBlockedByProbe(t *testing.T) {
	srv := newLocal(t)
	release := make(chan struct{})
	entered := make(chan struct{})
	srv.SetLocalHermes(LocalHermes{
		Name:   "Hermes",
		IDPath: filepath.Join(t.TempDir(), "id"),
		Find:   func() (bool, bool) { return true, true },
		Probe: func(ctx context.Context) string {
			close(entered)
			<-release
			return "unreachable"
		},
		Start: func(ctx context.Context, token string) {},
	})
	done := make(chan struct{})
	go func() { localCall(t, srv, "POST", "/api/local-hermes/connect", testOwner); close(done) }()
	<-entered
	got := make(chan int, 1)
	go func() { code, _ := localCall(t, srv, "GET", "/api/local-hermes", testOwner); got <- code }()
	select {
	case code := <-got:
		if code != http.StatusOK {
			t.Fatalf("status: %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("status blocked behind probe")
	}
	close(release)
	<-done
}
