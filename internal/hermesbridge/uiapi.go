package hermesbridge

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket/wsjson"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

// Local address: where Hermes, which cannot speak the deck protocol, uploads screens with curl.
// Deck messages carry no request number, so one is sent at a time and its reply awaited (uiMu).

const uiBodyLimit = 512 << 10 // a 256KB bundle plus its name and quotes

type uiResult struct {
	OK     bool   `json:"ok"`
	ID     string `json:"id,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// ListenUI opens the local address at cfg.UIAddr and serves until ctx ends. Loopback addresses only.
// Returns the address actually opened (the chosen port when port 0 is given).
func (b *Bridge) ListenUI(ctx context.Context) (string, error) {
	host, _, err := net.SplitHostPort(b.cfg.UIAddr)
	if err != nil {
		return "", fmt.Errorf("UI 주소 %q: %w", b.cfg.UIAddr, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return "", fmt.Errorf("UI 주소는 루프백 IP 여야 한다: %q", b.cfg.UIAddr)
	}
	ln, err := listenRetry(ctx, b.cfg.UIAddr)
	if err != nil {
		return "", err
	}
	srv := &http.Server{Handler: localOnly(b.uiHandler()), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("UI address: %v", err)
		}
	}()
	b.mu.Lock()
	b.uiListening = true
	b.mu.Unlock()
	log.Printf("local address http://%s", ln.Addr())
	return ln.Addr().String(), nil
}

// listenRetry retries for up to 2s if the port is not free yet; on reconnect the new adapter opens before the old one has closed.
func listenRetry(ctx context.Context, addr string) (net.Listener, error) {
	for try := 0; ; try++ {
		ln, err := net.Listen("tcp", addr)
		if err == nil || !errors.Is(err, syscall.EADDRINUSE) || try == 40 {
			return ln, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// localOnly blocks browser requests (with Origin) and non-loopback Host, and accepts only JSON for POST, so a web page cannot change screens behind the user's back.
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if r.Header.Get("Origin") != "" || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
			writeUI(w, http.StatusForbidden, uiResult{Reason: "이 맥 안의 프로그램만 부를 수 있다"})
			return
		}
		if r.Method == http.MethodPost {
			if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
				writeUI(w, http.StatusUnsupportedMediaType, uiResult{Reason: "Content-Type: application/json 이 필요하다"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (b *Bridge) uiHandler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /ui/catalog", b.uiCatalog)
	m.HandleFunc("GET /ui", func(w http.ResponseWriter, r *http.Request) {
		reply, status, reason := b.uiRequest(protocol.AgentMessage{Type: "ui.list"}, "ui.state", "")
		if status != http.StatusOK {
			writeUI(w, status, uiResult{Reason: reason})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(reply.UI)
	})
	m.HandleFunc("POST /ui/put", func(w http.ResponseWriter, r *http.Request) {
		raw, ok := readBody(w, r)
		if !ok {
			return
		}
		var spec map[string]any
		if json.Unmarshal(raw, &spec) != nil || spec == nil {
			writeUI(w, http.StatusBadRequest, uiResult{Reason: "본문은 설계도 JSON 객체여야 한다"})
			return
		}
		id, _ := spec["id"].(string)
		b.uiAck(w, protocol.AgentMessage{Type: "ui.put", Screen: raw, Place: r.URL.Query().Get("place")}, id)
	})
	m.HandleFunc("POST /ui/remove", func(w http.ResponseWriter, r *http.Request) {
		raw, ok := readBody(w, r)
		if !ok {
			return
		}
		var body struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &body) != nil || body.ID == "" {
			writeUI(w, http.StatusBadRequest, uiResult{Reason: `본문은 {"id": "화면 id"}`})
			return
		}
		b.uiAck(w, protocol.AgentMessage{Type: "ui.remove", ID: body.ID}, body.ID)
	})
	m.HandleFunc("GET /ui/screens/{id}/versions", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		reply, status, reason := b.uiRequest(protocol.AgentMessage{Type: "ui.versions", ID: id}, "ui.ack", id)
		if status != http.StatusOK {
			writeUI(w, status, uiResult{Reason: reason})
			return
		}
		if reply.OK == nil || !*reply.OK {
			status := http.StatusBadRequest
			if strings.HasPrefix(reply.Reason, "no such screen") {
				status = http.StatusNotFound
			}
			writeUI(w, status, uiResult{ID: id, Reason: reply.Reason})
			return
		}
		if len(reply.UI) == 0 {
			writeUI(w, http.StatusBadGateway, uiResult{ID: id, Reason: "the deck answered without a version list"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(reply.UI)
	})
	m.HandleFunc("POST /ui/screens/{id}/revert", func(w http.ResponseWriter, r *http.Request) {
		raw, ok := readBody(w, r)
		if !ok {
			return
		}
		var body struct {
			Version int `json:"version"`
		}
		if json.Unmarshal(raw, &body) != nil || body.Version < 1 {
			writeUI(w, http.StatusBadRequest, uiResult{Reason: `body is {"version": N}, a number from GET /ui/screens/<id>/versions`})
			return
		}
		id := r.PathValue("id")
		b.uiAck(w, protocol.AgentMessage{Type: "ui.revert", ID: id, To: body.Version}, id)
	})
	m.HandleFunc("POST /ui/part/put", func(w http.ResponseWriter, r *http.Request) {
		raw, ok := readBody(w, r)
		if !ok {
			return
		}
		var part map[string]any
		if json.Unmarshal(raw, &part) != nil || part == nil {
			writeUI(w, http.StatusBadRequest, uiResult{Reason: "본문은 조립 부품 JSON 객체여야 한다"})
			return
		}
		id, _ := part["id"].(string)
		b.uiAck(w, protocol.AgentMessage{Type: "ui.part.put", Part: raw}, id)
	})
	m.HandleFunc("POST /ui/part/remove", func(w http.ResponseWriter, r *http.Request) {
		raw, ok := readBody(w, r)
		if !ok {
			return
		}
		var body struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &body) != nil || body.ID == "" {
			writeUI(w, http.StatusBadRequest, uiResult{Reason: `본문은 {"id": "조립 부품 id"}`})
			return
		}
		b.uiAck(w, protocol.AgentMessage{Type: "ui.part.remove", ID: body.ID}, body.ID)
	})
	m.HandleFunc("POST /ui/layout", func(w http.ResponseWriter, r *http.Request) {
		raw, ok := readBody(w, r)
		if !ok {
			return
		}
		var body struct {
			Tabs []string `json:"tabs"`
		}
		if json.Unmarshal(raw, &body) != nil || len(body.Tabs) == 0 { // an empty array is dropped by omitempty and the deck sees it as a format error
			writeUI(w, http.StatusBadRequest, uiResult{Reason: `본문은 {"tabs": ["화면 id", ...]}, 한 개 이상`})
			return
		}
		b.uiAck(w, protocol.AgentMessage{Type: "ui.layout", Tabs: body.Tabs}, "")
	})
	m.HandleFunc("POST /data/put", func(w http.ResponseWriter, r *http.Request) {
		raw, ok := readBody(w, r)
		if !ok {
			return
		}
		var body struct {
			Name string          `json:"name"`
			Data json.RawMessage `json:"data"`
		}
		var obj map[string]any
		if json.Unmarshal(raw, &body) != nil || body.Name == "" || json.Unmarshal(body.Data, &obj) != nil || obj == nil {
			writeUI(w, http.StatusBadRequest, uiResult{Reason: `본문은 {"name": "묶음 이름", "data": {JSON 객체}}`})
			return
		}
		b.uiAck(w, protocol.AgentMessage{Type: "data.put", Name: body.Name, Data: body.Data}, body.Name)
	})
	m.HandleFunc("POST /data/remove", func(w http.ResponseWriter, r *http.Request) {
		raw, ok := readBody(w, r)
		if !ok {
			return
		}
		var body struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &body) != nil || body.Name == "" {
			writeUI(w, http.StatusBadRequest, uiResult{Reason: `본문은 {"name": "묶음 이름"}`})
			return
		}
		b.uiAck(w, protocol.AgentMessage{Type: "data.remove", Name: body.Name}, body.Name)
	})
	return m
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, uiBodyLimit))
	if err != nil {
		writeUI(w, http.StatusRequestEntityTooLarge, uiResult{Reason: "본문이 너무 크다"})
		return nil, false
	}
	return raw, true
}

func writeUI(w http.ResponseWriter, status int, res uiResult) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(res)
}

// uiAck sends ui.put/remove/layout and data.put/remove and relays ui.ack over HTTP. A rejection is 400 with the deck's reason.
func (b *Bridge) uiAck(w http.ResponseWriter, msg protocol.AgentMessage, id string) {
	reply, status, reason := b.uiRequest(msg, "ui.ack", id)
	if status != http.StatusOK {
		writeUI(w, status, uiResult{Reason: reason})
		return
	}
	res := uiResult{OK: reply.OK != nil && *reply.OK, ID: reply.ID, Reason: reply.Reason}
	if !res.OK {
		status = http.StatusBadRequest
	}
	writeUI(w, status, res)
}

// uiSeq numbers ui.ack requests so a late ack from a timed-out request cannot answer the next one.
var uiSeq atomic.Uint64

// uiRequest sends msg to the deck and waits for a reply of type want. Only one runs at a time.
// An ack with a different id is dropped so the next request does not take a late reply to an earlier timed-out one.
func (b *Bridge) uiRequest(msg protocol.AgentMessage, want, id string) (protocol.ServerMessage, int, string) {
	b.uiMu.Lock()
	defer b.uiMu.Unlock()

	if want == "ui.ack" && msg.Req == "" {
		msg.Req = fmt.Sprintf("ui-%d", uiSeq.Add(1))
	}
	wait := make(chan protocol.ServerMessage, 1)
	b.mu.Lock()
	conn := b.conn
	if conn != nil {
		b.uiWait = wait
	}
	b.mu.Unlock()
	if conn == nil {
		return protocol.ServerMessage{}, http.StatusServiceUnavailable, "덱에 연결돼 있지 않다"
	}
	defer func() {
		b.mu.Lock()
		if b.uiWait == wait {
			b.uiWait = nil
		}
		b.mu.Unlock()
	}()

	timeout := cmp.Or(b.cfg.UITimeout, 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := wsjson.Write(ctx, conn, msg); err != nil {
		return protocol.ServerMessage{}, http.StatusServiceUnavailable, "덱 전송 실패: " + err.Error()
	}
	for {
		select {
		case reply := <-wait:
			if reply.Type == want && (want != "ui.ack" || reply.Req == msg.Req || (reply.Req == "" && (reply.ID == id || reply.ID == ""))) {
				return reply, http.StatusOK, ""
			}
		case <-ctx.Done():
			return protocol.ServerMessage{}, http.StatusGatewayTimeout, fmt.Sprintf("덱이 %s 안에 답하지 않았다", timeout)
		}
	}
}

// uiReply hands ui.ack and ui.state from the read goroutine to the waiting request; dropped if nobody waits.
func (b *Bridge) uiReply(msg protocol.ServerMessage) {
	b.mu.Lock()
	wait := b.uiWait
	b.mu.Unlock()
	if wait == nil {
		return
	}
	select {
	case wait <- msg:
	default:
	}
}

// uiCatalog returns the deck catalog as is. It is static, so independent of the websocket connection.
func (b *Bridge) uiCatalog(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), cmp.Or(b.cfg.UITimeout, 5*time.Second))
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(b.cfg.DeckURL, "/")+"/ui/catalog", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		writeUI(w, http.StatusBadGateway, uiResult{Reason: "덱 카탈로그를 못 받았다: " + err.Error()})
		return
	}
	defer res.Body.Close()
	w.Header().Set("Content-Type", cmp.Or(res.Header.Get("Content-Type"), "application/json"))
	w.WriteHeader(res.StatusCode)
	io.Copy(w, res.Body)
}
