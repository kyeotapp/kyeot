// Package decktest is a test helper that starts the kyeot server and drives the API and the screen websocket.
package decktest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/kyeotapp/kyeot/internal/fakeagent"
	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/server"
)

const (
	OwnerToken    = "test-owner-token-0123456789"
	AllowedOrigin = "http://localhost:8081"
)

type Harness struct {
	t     *testing.T
	URL   string
	close func()
}

func New(t *testing.T) *Harness {
	t.Helper()
	return NewWith(t, nil)
}

// NewWith starts the server with modified options. The upload folder is always a test temp folder.
func NewWith(t *testing.T, tweak func(*server.Options)) *Harness {
	t.Helper()
	opts := server.Options{DBPath: ":memory:", OwnerToken: OwnerToken, AllowedOrigins: []string{AllowedOrigin}, UploadDir: t.TempDir()}
	if tweak != nil {
		tweak(&opts)
	}
	srv, err := server.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	h := &Harness{t: t, URL: ts.URL}
	h.close = sync.OnceFunc(func() {
		srv.Close()
		ts.Close()
	})
	t.Cleanup(h.close)
	h.AgreeTerms(OwnerToken) // posting in a shared room requires terms agreement; tests of the no-agreement case check it separately
	return h
}

// AgreeTerms makes the token's user agree to the terms now. A missing or wrong token doesn't stop the test.
func (h *Harness) AgreeTerms(token string) {
	h.APIAs(token, "POST", "/api/me/terms", map[string]string{"version": server.TermsVersion}, nil)
}

// Close shuts the server down first. Tests that restart on the same DB stop the old server first (in reality too, one server per DB).
func (h *Harness) Close() { h.close() }

func (h *Harness) API(method, path string, body any, out any) int {
	h.t.Helper()
	return h.APIAs(OwnerToken, method, path, body, out)
}

func (h *Harness) APIAs(token, method, path string, body any, out any) int {
	h.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, h.URL+path, &buf)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

// Post sends the body as-is (file upload, recording). It returns the status code and response body.
func (h *Harness) Post(path, contentType string, body []byte, out any) int {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, h.URL+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+OwnerToken)
	req.Header.Set("Content-Type", contentType)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

type Created struct {
	Agent protocol.Agent `json:"agent"`
	Token string         `json:"token"`
}

func (h *Harness) CreateAgent(name string) Created {
	var c Created
	if s := h.API("POST", "/api/agents", map[string]string{"name": name}, &c); s != http.StatusCreated {
		h.t.Fatalf("create agent: %d", s)
	}
	return c
}

func (h *Harness) StartFakeAgent(token string) *fakeagent.Agent {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	a, err := fakeagent.Dial(ctx, h.URL, token)
	if err != nil {
		h.t.Fatal(err)
	}
	go a.Run(ctx)
	h.t.Cleanup(func() { a.Close(); cancel() })
	return a
}

// client is the screen-side websocket. It buffers received messages and waits until a condition matches.
type Client struct {
	msgs chan protocol.ClientMessage
}

func (h *Harness) ConnectClient() *Client {
	h.t.Helper()
	return h.ConnectClientAs(OwnerToken)
}

func (h *Harness) ConnectClientAs(token string) *Client {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.URL, "http")+"/client?token="+token, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	c := &Client{msgs: make(chan protocol.ClientMessage, 256)}
	go func() {
		for {
			var m protocol.ClientMessage
			if wsjson.Read(ctx, conn, &m) != nil {
				return
			}
			c.msgs <- m
		}
	}()
	h.t.Cleanup(func() { conn.CloseNow(); cancel() })
	// the server starts reading and sends the pong only after registering with the hub
	pctx, pcancel := context.WithTimeout(ctx, WaitLimit)
	defer pcancel()
	if err := conn.Ping(pctx); err != nil {
		h.t.Fatal(err)
	}
	return c
}

// WaitLimit is the wait limit for a condition. It ends as soon as the condition passes, so it is generous (-race parallel load).
const WaitLimit = 15 * time.Second

// WaitFor discards non-matching messages. Use WaitAll for several conditions in no fixed order.
func (h *Harness) WaitFor(c *Client, what string, pred func(protocol.ClientMessage) bool) {
	h.t.Helper()
	h.WaitAll(c, what, pred)
}

// WaitAll waits until each condition has matched one message. Order doesn't matter, and conditions must not overlap.
func (h *Harness) WaitAll(c *Client, what string, preds ...func(protocol.ClientMessage) bool) {
	h.t.Helper()
	left := slices.Clone(preds)
	timeout := time.After(WaitLimit)
	for len(left) > 0 {
		select {
		case m := <-c.msgs:
			if i := slices.IndexFunc(left, func(p func(protocol.ClientMessage) bool) bool { return p(m) }); i >= 0 {
				left = slices.Delete(left, i, i+1)
			}
		case <-timeout:
			h.t.Fatalf("timeout waiting for %s (%d left)", what, len(left))
		}
	}
}

// Quiet checks that no message matching the condition arrives for d.
func (h *Harness) Quiet(c *Client, what string, d time.Duration, pred func(protocol.ClientMessage) bool) {
	h.t.Helper()
	timeout := time.After(d)
	for {
		select {
		case m := <-c.msgs:
			if pred(m) {
				h.t.Fatalf("unexpected %s: %+v", what, m)
			}
		case <-timeout:
			return
		}
	}
}

func AgentState(id, state string) func(protocol.ClientMessage) bool {
	return func(m protocol.ClientMessage) bool {
		return m.Type == "agent" && m.Agent.ID == id && m.Agent.State == state
	}
}

func TaskState(id, state string) func(protocol.ClientMessage) bool {
	return func(m protocol.ClientMessage) bool {
		return m.Type == "task" && m.Task.ID == id && m.Task.State == state
	}
}
