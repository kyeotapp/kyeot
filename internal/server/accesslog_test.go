package server_test

import (
	"bytes"
	"context"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/server"
)

type lockedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitLine returns the first log line matching re, failing after a few seconds.
func (b *lockedBuf) waitLine(t *testing.T, re string) string {
	t.Helper()
	rx := regexp.MustCompile(re)
	deadline := time.Now().Add(decktest.WaitLimit)
	for time.Now().Before(deadline) {
		for _, l := range strings.Split(b.String(), "\n") {
			if rx.MatchString(l) {
				return l
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no log line matching %q in:\n%s", re, b.String())
	return ""
}

func do(t *testing.T, method, url, token, ua string) int {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestAccessLogLinesHaveNoSecrets(t *testing.T) {
	var out lockedBuf
	h := decktest.NewWith(t, func(o *server.Options) { o.AccessLog = &out })
	agent := h.CreateAgent("Logged")

	do(t, "GET", h.URL+"/api/agents?token=QUERYSECRET&x=1", decktest.OwnerToken, "Kyeot/42 CFNetwork/3826 Darwin/25.0.0")
	line := out.waitLine(t, `^access \S+Z http owner=[^-\s]\S{0,7} GET /api/agents 200 \d+ms ip=127\.0\.0\.1 ua="Kyeot/42 Darwin/25\.0\.0"$`)
	if strings.Contains(line, "owner=-") {
		t.Fatalf("owner not recorded: %s", line)
	}

	do(t, "GET", h.URL+"/api/agents", "wrong-token-SECRET", "")
	out.waitLine(t, `http owner=- GET /api/agents 401 `)

	do(t, "DELETE", h.URL+"/api/devices/DEVICESECRET", decktest.OwnerToken, "")
	out.waitLine(t, `DELETE /api/devices/\{token\} `)

	do(t, "GET", h.URL+"/api/x%0Aaccess%20forged", decktest.OwnerToken, "")
	out.waitLine(t, `GET /api/x_access_forged 404 `)

	do(t, "GET", h.URL+"/api/c1%C2%85%E2%80%AEx", decktest.OwnerToken, "")
	out.waitLine(t, `GET /api/c1__x 404 `)

	do(t, "GET", h.URL+"/version", "", "Mozilla/5.0 (Macintosh) AppleWebKit/605 Version/18 Safari/605")
	out.waitLine(t, `GET /version \d+ \d+ms ip=\S+ ua="browser Safari"$`)

	ctx, cancel := context.WithTimeout(context.Background(), decktest.WaitLimit)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.URL, "http")+"/client?token="+decktest.OwnerToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	out.waitLine(t, `ws-open owner=[^-\s]\S{0,7} GET /client 101 `)
	conn.Close(websocket.StatusNormalClosure, "")
	out.waitLine(t, `ws-close owner=[^-\s]\S{0,7} GET /client dur=\S+ `)

	h.StartFakeAgent(agent.Token)
	out.waitLine(t, `ws-open owner=[^-\s]\S{0,7} agent=`+regexp.QuoteMeta(agent.Agent.ID[:8])+` GET /agent 101 `)

	all := out.String()
	for _, secret := range []string{decktest.OwnerToken, agent.Token, "QUERYSECRET", "SECRET", "DEVICESECRET", "?"} {
		if strings.Contains(all, secret) {
			t.Fatalf("log contains %q:\n%s", secret, all)
		}
	}
	for _, l := range strings.Split(strings.TrimSpace(all), "\n") {
		if !strings.HasPrefix(l, "access ") {
			t.Fatalf("line split or forged: %q", l)
		}
	}
}
