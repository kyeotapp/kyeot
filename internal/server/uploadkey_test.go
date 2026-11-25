package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/kyeotapp/kyeot/internal/server"
)

// Restart the server on the same DB and attachment folder, changing only the token and attachment key.
func TestUploadKeySurvivesTokenRotation(t *testing.T) {
	dir := t.TempDir()
	const oldToken, newToken = "old-owner-token-0123456789", "new-owner-token-0123456789"
	start := func(token string, key []byte) string {
		srv, err := server.New(server.Options{DBPath: filepath.Join(dir, "k.db"), UploadDir: filepath.Join(dir, "uploads"), OwnerToken: token, UploadKey: key})
		if err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(srv.Handler())
		t.Cleanup(func() { ts.Close(); srv.Close() })
		return ts.URL
	}
	get := func(base, path, token string) int {
		req, _ := http.NewRequest("GET", base+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	// Version without a key file: derive the key from the token
	base := start(oldToken, nil)
	req, _ := http.NewRequest("POST", base+"/api/uploads?name=a.txt", bytes.NewReader([]byte("hello")))
	req.Header.Set("Authorization", "Bearer "+oldToken)
	req.Header.Set("Content-Type", "text/plain")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var up struct{ URL string }
	json.NewDecoder(res.Body).Decode(&up)
	res.Body.Close()
	if res.StatusCode != http.StatusCreated || up.URL == "" {
		t.Fatalf("upload: %d", res.StatusCode)
	}

	// When first creating the key file, use the key derived from the old token, with a new token
	base = start(newToken, server.LegacyUploadKey(oldToken))
	if s := get(base, up.URL, ""); s != http.StatusOK {
		t.Fatalf("old signed URL must still open after token reissue: %d", s)
	}
	if s := get(base, "/api/agents", oldToken); s != http.StatusUnauthorized {
		t.Fatalf("old token must be rejected: %d", s)
	}
	if s := get(base, "/api/agents", newToken); s != http.StatusOK {
		t.Fatalf("new token must pass: %d", s)
	}

	// After changing the attachment key, old URLs are blocked
	base = start(newToken, bytes.Repeat([]byte{7}, 32))
	if s := get(base, up.URL, ""); s != http.StatusNotFound {
		t.Fatalf("old URL must be rejected after attachment key change: %d", s)
	}
}
