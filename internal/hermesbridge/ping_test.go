package hermesbridge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPing(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	ctx := context.Background()
	if err := Ping(ctx, Config{HermesURL: ts.URL + "/", HermesKey: "good"}); err != nil {
		t.Fatalf("good key: %v", err)
	}
	if err := Ping(ctx, Config{HermesURL: ts.URL, HermesKey: "bad"}); !errors.Is(err, ErrBadKey) {
		t.Fatalf("bad key: %v", err)
	}
	ts.Close()
	if err := Ping(ctx, Config{HermesURL: ts.URL, HermesKey: "good"}); err == nil || errors.Is(err, ErrBadKey) {
		t.Fatalf("closed server: %v", err)
	}
}
