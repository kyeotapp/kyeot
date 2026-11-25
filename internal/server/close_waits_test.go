package server_test

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/server"
)

// While closing, the DB is not closed until in-flight requests (uploads) finish. New requests after that get 503.
func TestCloseWaitsForRunningRequests(t *testing.T) {
	h := decktest.NewWith(t, func(o *server.Options) { o.DBPath = filepath.Join(t.TempDir(), "k.db") })
	pr, pw := io.Pipe()
	got := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest("POST", h.URL+"/api/uploads?name=a", pr)
		req.Header.Set("Authorization", "Bearer "+decktest.OwnerToken)
		req.Header.Set("Content-Type", "text/plain")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			got <- 0
			return
		}
		res.Body.Close()
		got <- res.StatusCode
	}()
	pw.Write([]byte("first part"))
	time.Sleep(100 * time.Millisecond) // until the request reaches the handler
	closed := make(chan struct{})
	go func() { h.Close(); close(closed) }()
	time.Sleep(300 * time.Millisecond)
	select {
	case <-closed:
		t.Fatal("closed without waiting for the in-flight request")
	default:
	}
	pw.Write([]byte(strings.Repeat("b", 10)))
	pw.Close()
	if code := <-got; code != http.StatusCreated {
		t.Fatalf("upload finished while closing: %d", code)
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the request finished but the server did not close")
	}
}
