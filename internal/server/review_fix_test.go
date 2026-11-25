package server_test

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/fakeagent"
	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/server"
)

func wsURL(h *decktest.Harness, path, token string) string {
	return "ws" + strings.TrimPrefix(h.URL, "http") + path + "?token=" + token
}

// closeCode reads until the server disconnects and returns the close code received.
func closeCode(t *testing.T, conn *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

func dialWS(t *testing.T, h *decktest.Harness, path, token string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(context.Background(), wsURL(h, path, token), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

func TestRevokeInvitedOwner(t *testing.T) {
	h := decktest.New(t)
	mine := h.CreateAgent("Agent A")
	tok2 := invite(t, h, "Bob")

	var friendAgent struct {
		Agent protocol.Agent
		Token string
	}
	if s := h.APIAs(tok2, "POST", "/api/agents", map[string]string{"name": "Bob bot"}, &friendAgent); s != http.StatusCreated {
		t.Fatalf("friend agent: %d", s)
	}
	var room protocol.Room
	h.API("POST", "/api/rooms", map[string]any{"name": "Contest", "agentId": mine.Agent.ID}, &room)
	var code struct{ Code string }
	h.API("POST", "/api/rooms/"+room.ID+"/invite", nil, &code)
	if s := h.APIAs(tok2, "POST", "/api/rooms/join", map[string]string{"code": code.Code}, nil); s != http.StatusOK {
		t.Fatalf("join: %d", s)
	}

	var list []map[string]any
	if s := h.API("GET", "/api/owners", nil, &list); s != http.StatusOK || len(list) != 1 || list[0]["name"] != "Bob" || list[0]["createdAt"] == nil {
		t.Fatalf("list: %d %v", s, list)
	}
	if _, leaks := list[0]["token"]; leaks {
		t.Fatal("list leaks token")
	}
	if s := h.APIAs(tok2, "GET", "/api/owners", nil, nil); s != http.StatusForbidden {
		t.Fatalf("member lists users: %d", s)
	}
	if s := h.APIAs(tok2, "DELETE", "/api/owners/me", nil, nil); s != http.StatusForbidden {
		t.Fatalf("member revokes: %d", s)
	}
	if s := h.API("DELETE", "/api/owners/me", nil, nil); s != http.StatusBadRequest {
		t.Fatalf("first owner revoked: %d", s)
	}

	client := dialWS(t, h, "/client", tok2)
	agentConn := dialWS(t, h, "/agent", friendAgent.Token)
	friendID := list[0]["id"].(string)
	if s := h.API("DELETE", "/api/owners/"+friendID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("revoke: %d", s)
	}
	if c := closeCode(t, client); c != 4003 {
		t.Fatalf("client close code %d", c)
	}
	if c := closeCode(t, agentConn); c != 4003 {
		t.Fatalf("agent close code %d", c)
	}
	if s := h.APIAs(tok2, "GET", "/api/agents", nil, nil); s != http.StatusUnauthorized {
		t.Fatalf("revoked token still works: %d", s)
	}
	if _, _, err := websocket.Dial(context.Background(), wsURL(h, "/client", tok2), nil); err == nil {
		t.Fatal("revoked token opens ws")
	}
	if _, err := fakeagent.Dial(context.Background(), h.URL, friendAgent.Token); err == nil {
		t.Fatal("revoked owner's agent still connects")
	}
	var members []map[string]any
	h.API("GET", "/api/rooms/"+room.ID+"/members", nil, &members)
	if len(members) != 0 {
		t.Fatalf("still a member: %v", members)
	}
	if h.API("GET", "/api/owners", nil, &list); len(list) != 0 {
		t.Fatalf("revoked still listed: %v", list)
	}
	if s := h.API("DELETE", "/api/owners/"+friendID, nil, nil); s != http.StatusNotFound {
		t.Fatalf("revoke twice: %d", s)
	}
}

// The close code must go out before cancel. Run many times to rule out luck.
func TestCloseCodesReachPeer(t *testing.T) {
	h := decktest.New(t)
	for i := 0; i < 10; i++ {
		a := h.CreateAgent("x")
		first := dialWS(t, h, "/agent", a.Token)
		time.Sleep(10 * time.Millisecond)
		second := dialWS(t, h, "/agent", a.Token)
		if c := closeCode(t, first); c != 4000 {
			t.Fatalf("round %d replace: %d", i, c)
		}
		var rot struct{ Token string }
		h.API("POST", "/api/agents/"+a.Agent.ID+"/token", nil, &rot)
		if c := closeCode(t, second); c != 4002 {
			t.Fatalf("round %d rotate: %d", i, c)
		}
		third := dialWS(t, h, "/agent", rot.Token)
		time.Sleep(10 * time.Millisecond)
		h.API("DELETE", "/api/agents/"+a.Agent.ID, nil, nil)
		if c := closeCode(t, third); c != 4001 {
			t.Fatalf("round %d unlink: %d", i, c)
		}
	}
}

func TestShutdownSendsGoingAway(t *testing.T) {
	srv, err := server.New(server.Options{DBPath: ":memory:", OwnerToken: decktest.OwnerToken, UploadDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	ts := hs.URL
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(ts, "http")+"/client?token="+decktest.OwnerToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	got := make(chan websocket.StatusCode, 1)
	go func() {
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				got <- websocket.CloseStatus(err)
				return
			}
		}
	}()
	time.Sleep(20 * time.Millisecond)
	srv.Close()
	if c := <-got; c != websocket.StatusGoingAway {
		t.Fatalf("shutdown code %d", c)
	}
}

func TestEmptyAllowNetsRejected(t *testing.T) {
	if _, err := server.New(server.Options{DBPath: ":memory:", OwnerToken: "x", AllowNets: []*net.IPNet{}}); err == nil {
		t.Fatal("empty AllowNets must be rejected")
	}
	for _, spec := range []string{"", " , ", ","} {
		if _, err := server.ParseNets(spec); err == nil {
			t.Fatalf("%q should fail", spec)
		}
	}
}

func TestUploadQuota(t *testing.T) {
	h := decktest.NewWith(t, func(o *server.Options) { o.UploadQuota = 10 })
	if s := h.Post("/api/uploads?name=a", "text/plain", []byte("123456"), nil); s != http.StatusCreated {
		t.Fatalf("first: %d", s)
	}
	if s := h.Post("/api/uploads?name=b", "text/plain", []byte("12345"), nil); s != http.StatusRequestEntityTooLarge {
		t.Fatalf("over quota: %d", s)
	}
	if s := h.Post("/api/uploads?name=c", "text/plain", []byte("1234"), nil); s != http.StatusCreated {
		t.Fatalf("fits: %d", s)
	}
}

func TestTranscribeHidesToolOutput(t *testing.T) {
	bin := t.TempDir()
	convert := filepath.Join(bin, "afconvert")
	os.WriteFile(convert, []byte("#!/bin/sh\necho /secret/path/model >&2\nexit 1\n"), 0o755)
	h := decktest.NewWith(t, func(o *server.Options) {
		o.WhisperModel, o.ConvertBin = "model.bin", convert
	})
	req, _ := http.NewRequest("POST", h.URL+"/api/transcribe", bytes.NewReader([]byte("AUDIO")))
	req.Header.Set("Authorization", "Bearer "+decktest.OwnerToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(res.Body)
	if res.StatusCode != http.StatusBadRequest || strings.Contains(buf.String(), "secret") {
		t.Fatalf("%d %s", res.StatusCode, buf.String())
	}
}

func TestDeviceStaysWithItsAccount(t *testing.T) {
	h := decktest.New(t)
	tok2 := invite(t, h, "Bob")
	dev := map[string]string{"token": strings.Repeat("ab", 32), "platform": "ios", "env": "sandbox"}
	if s := h.API("POST", "/api/devices", dev, nil); s != http.StatusNoContent {
		t.Fatalf("register: %d", s)
	}
	if s := h.APIAs(tok2, "POST", "/api/devices", dev, nil); s != http.StatusConflict { // tries to take another's device token
		t.Fatalf("other account: %d", s)
	}
	if s := h.API("DELETE", "/api/devices/"+dev["token"], nil, nil); s != http.StatusNoContent {
		t.Fatalf("forget: %d", s)
	}
	if s := h.APIAs(tok2, "POST", "/api/devices", dev, nil); s != http.StatusNoContent { // after removing the server, a different account on the same phone
		t.Fatalf("after forget: %d", s)
	}
	if s := h.APIAs(tok2, "DELETE", "/api/devices/"+dev["token"], nil, nil); s != http.StatusNoContent {
		t.Fatalf("forget 2: %d", s)
	}
	if s := h.API("POST", "/api/devices", dev, nil); s != http.StatusNoContent {
		t.Fatalf("re-register: %d", s)
	}
	dev["env"] = "production"
	if s := h.API("POST", "/api/devices", dev, nil); s != http.StatusNoContent {
		t.Fatalf("same owner update: %d", s)
	}
}
