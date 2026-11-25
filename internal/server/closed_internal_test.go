package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kyeotapp/kyeot/internal/store"
)

// Starting again on a DB closed by wiping the whole server starts neither the same-computer Hermes nor sensors.
func TestClosedServerStartsNothing(t *testing.T) {
	db := filepath.Join(t.TempDir(), "k.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.WipeAll(); err != nil {
		t.Fatal(err)
	}
	st.Close()
	srv, err := New(Options{DBPath: db, OwnerToken: "test-owner-token-0123456789", UploadDir: t.TempDir(), SensorDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	if srv.hub.sensorRefresh != nil {
		t.Fatal("a closed server started sensors")
	}
	f := setFakeLocal(t, srv)
	os.WriteFile(f.path, []byte("x\n"), 0o600) // even with traces of consent
	consented, err := srv.ResumeLocalHermes()
	if consented || err != nil || len(f.started) != 0 {
		t.Fatalf("a closed server attached Hermes: %v %v %d", consented, err, len(f.started))
	}
}
