package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/store"
)

// Sweeping unattached uploads does not delete files attached to room person posts.
func TestSweepKeepsRoomTalkFiles(t *testing.T) {
	srv := newLocal(t)
	dir := srv.uploadDir()
	os.MkdirAll(dir, 0o700)
	ag, _, _ := srv.store.CreateAgent(store.Owner, "Agent A")
	room, err := srv.store.CreateRoom(store.Owner, "Room", ag.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	onTalk, _ := srv.store.CreateUpload(store.Owner, "a.png", "image/png", 1)
	loose, _ := srv.store.CreateUpload(store.Owner, "b.png", "image/png", 1)
	for _, id := range []string{onTalk.ID, loose.ID} {
		os.WriteFile(filepath.Join(dir, id), []byte("x"), 0o600)
	}
	if _, err := srv.store.AddRoomMessage(room.ID, store.Owner, "", nil, []protocol.Attachment{onTalk}); err != nil {
		t.Fatal(err)
	}
	srv.sweepUploads(time.Now().Add(time.Hour))
	if _, _, err := srv.store.Upload(onTalk.ID); err != nil {
		t.Fatal("room talk upload row must stay")
	}
	if _, err := os.Stat(filepath.Join(dir, onTalk.ID)); err != nil {
		t.Fatal("room talk file must stay")
	}
	if _, err := os.Stat(filepath.Join(dir, loose.ID)); !os.IsNotExist(err) {
		t.Fatal("loose file should be swept")
	}
}

// Even with a valid signature, an expired URL can't fetch.
func TestSignedUploadURLExpires(t *testing.T) {
	srv := newLocal(t)
	os.MkdirAll(srv.uploadDir(), 0o700)
	a, _ := srv.store.CreateUpload(store.Owner, "a.png", "image/png", 1)
	os.WriteFile(filepath.Join(srv.uploadDir(), a.ID), []byte("x"), 0o600)
	at := func(exp int64) string {
		return "/api/uploads/" + a.ID + "?exp=" + strconv.FormatInt(exp, 10) + "&sig=" + srv.sign(a.ID, exp)
	}
	if code, _ := localCall(t, srv, "GET", at(time.Now().Add(time.Minute).Unix()), ""); code != http.StatusOK {
		t.Fatalf("fresh: %d", code)
	}
	if code, _ := localCall(t, srv, "GET", at(time.Now().Add(-time.Second).Unix()), ""); code != http.StatusNotFound {
		t.Fatalf("expired: %d", code)
	}
}
