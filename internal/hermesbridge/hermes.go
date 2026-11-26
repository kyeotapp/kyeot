package hermesbridge

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// hermes is a client for /v1/runs of the Hermes gateway api_server.
type hermes struct {
	base  string
	key   string
	http  *http.Client
	idle  time.Duration // SSE silence limit; Hermes sends a keepalive every 10s
	retry time.Duration // resume interval
}

const (
	postTimeout   = 30 * time.Second
	streamIdle    = 30 * time.Second
	streamRetries = 3
)

type hermesEvent struct {
	Event       string          `json:"event"`
	RunID       string          `json:"run_id"`
	Tool        string          `json:"tool"`
	Preview     string          `json:"preview"`
	ToolCallID  string          `json:"tool_call_id"`
	Duration    float64         `json:"duration"`
	Error       any             `json:"error"`
	Delta       string          `json:"delta"`
	Output      string          `json:"output"`
	Command     string          `json:"command"`
	Desc        string          `json:"description"`
	PatternKey  string          `json:"pattern_key"`
	PatternKeys []string        `json:"pattern_keys"`
	Choices     []string        `json:"choices"`
	RequestID   string          `json:"request_id"`
	PromptID    string          `json:"prompt_id"` // Hermes 0.22 session-authority events name the request id this way
	Question    string          `json:"question"`
	Generation  json.RawMessage `json:"execution_generation"`
	Goal        string          `json:"goal"`
	Summary     string          `json:"summary"`
	ExitReason  string          `json:"turn_exit_reason"`
	SubagentID  string          `json:"subagent_id"`
	Status      string          `json:"status"`
}

// requestID is the id to answer this prompt with.
func (ev hermesEvent) requestID() string {
	if ev.RequestID != "" {
		return ev.RequestID
	}
	return ev.PromptID
}

func (h *hermes) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var buf io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		buf = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.base+path, buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return h.http.Do(req)
}

// post returns the status code and the start of the body as an error unless the status is 2xx.
func (h *hermes) post(ctx context.Context, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, postTimeout)
	defer cancel()
	res, err := h.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	if res.StatusCode/100 != 2 {
		return &statusError{res.StatusCode, strings.TrimSpace(string(raw[:min(len(raw), 300)]))}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string { return fmt.Sprintf("hermes %d: %s", e.code, e.body) }

// The instructions of startRun apply to this run only and are not stored; empty means not sent.
func (h *hermes) startRun(ctx context.Context, input, sessionID, instructions string) (string, error) {
	var out struct {
		RunID string `json:"run_id"`
	}
	body := map[string]string{"input": input, "session_id": sessionID}
	if instructions != "" {
		body["instructions"] = instructions
	}
	err := h.post(ctx, "/v1/runs", body, &out)
	if err == nil && out.RunID == "" {
		err = fmt.Errorf("hermes: run_id missing")
	}
	return out.RunID, err
}

func (h *hermes) steer(ctx context.Context, runID, text string) error {
	return h.post(ctx, "/v1/runs/"+runID+"/steer", map[string]string{"text": text}, nil)
}

// approve answers an approval. With session_authority Hermes needs exactly request_id, execution_generation and choice;
// older Hermes sends no generation and reads only choice and request_id.
func (h *hermes) approve(ctx context.Context, runID, choice, requestID string, generation json.RawMessage) error {
	body := map[string]any{"choice": choice}
	if requestID != "" {
		body["request_id"] = requestID
	}
	if len(generation) > 0 && string(generation) != "null" {
		body["execution_generation"] = generation
	}
	return h.post(ctx, "/v1/runs/"+runID+"/approval", body, nil)
}

// clarify answers a clarify question. execution_generation is returned exactly as received.
func (h *hermes) clarify(ctx context.Context, runID, requestID string, generation json.RawMessage, answer string) error {
	body := map[string]any{"request_id": requestID, "answer": answer, "execution_generation": generation}
	if len(generation) == 0 {
		body["execution_generation"] = nil
	}
	return h.post(ctx, "/v1/runs/"+runID+"/clarify", body, nil)
}

func (h *hermes) stop(ctx context.Context, runID string) error {
	return h.post(ctx, "/v1/runs/"+runID+"/stop", struct{}{}, nil)
}

// events passes the data lines of the SSE stream one by one; it stops when fn returns false.
// If the stream drops or stays silent while idle, it resumes a few times from the last seq.
func (h *hermes) events(ctx context.Context, runID string, fn func(hermesEvent) bool) error {
	last := -1
	var err error
	for tries := 0; tries <= streamRetries; tries++ {
		if tries > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(cmp.Or(h.retry, time.Second) * time.Duration(tries)):
			}
		}
		var progressed, stop bool
		progressed, stop, err = h.eventsOnce(ctx, runID, &last, fn)
		var se *statusError
		if stop || ctx.Err() != nil || errors.As(err, &se) {
			return err
		}
		if progressed {
			tries = 0
		}
	}
	return err
}

func (h *hermes) eventsOnce(ctx context.Context, runID string, last *int, fn func(hermesEvent) bool) (progressed, stop bool, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	idle := cmp.Or(h.idle, streamIdle)
	timer := time.AfterFunc(idle, cancel)
	defer timer.Stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.base+"/v1/runs/"+runID+"/events", nil)
	if err != nil {
		return false, true, err
	}
	req.Header.Set("Authorization", "Bearer "+h.key)
	if *last >= 0 {
		req.Header.Set("Last-Event-ID", strconv.Itoa(*last))
	}
	res, err := h.http.Do(req)
	if err != nil {
		return false, false, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false, true, &statusError{res.StatusCode, "events"}
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	seq := -1
	for sc.Scan() {
		timer.Reset(idle)
		line := sc.Text()
		if id, ok := strings.CutPrefix(line, "id: "); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(id)); err == nil {
				seq = n
			}
			continue
		}
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev hermesEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			logOnce("hermes-event", "could not read Hermes events: %v", err)
			continue
		}
		progressed = true
		if seq >= 0 {
			*last, seq = seq, -1
		}
		if !fn(ev) {
			return true, true, nil
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return progressed, false, fmt.Errorf("Hermes 이벤트가 %s 동안 없다", idle)
		}
		return progressed, false, err
	}
	return progressed, false, io.ErrUnexpectedEOF
}

// ErrBadKey means Hermes rejected the key.
var ErrBadKey = errors.New("hermes: API_SERVER_KEY 가 맞지 않는다")

// Ping reaches the Hermes API with the authenticated GET /v1/models and checks the key.
func Ping(ctx context.Context, cfg Config) error {
	h := &hermes{base: strings.TrimRight(cfg.HermesURL, "/"), key: cfg.HermesKey, http: &http.Client{}}
	res, err := h.do(ctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return err
	}
	res.Body.Close()
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return ErrBadKey
	case res.StatusCode/100 != 2:
		return &statusError{res.StatusCode, "models"}
	}
	return nil
}
