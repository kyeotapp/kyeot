package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Access log: one line per request, and one line each for websocket open and close.
// Never logs tokens, query strings, bodies, or headers other than a short User-Agent.

type accessKey struct{}

type accessRec struct {
	mu    sync.Mutex
	owner string
	agent string
}

func (a *accessRec) who() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := "owner=" + short(a.owner)
	if a.agent != "" {
		out += " agent=" + short(a.agent)
	}
	return out
}

func short(id string) string {
	if id == "" {
		return "-"
	}
	if len(id) > 8 {
		id = id[:8]
	}
	return clean(id, 8)
}

// noteOwner records who the request belongs to, for the access log.
func noteOwner(r *http.Request, owner, agent string) {
	a, _ := r.Context().Value(accessKey{}).(*accessRec)
	if a == nil {
		return
	}
	a.mu.Lock()
	if owner != "" {
		a.owner = owner
	}
	if agent != "" {
		a.agent = agent
	}
	a.mu.Unlock()
}

type accessWriter struct {
	http.ResponseWriter
	status   int
	upgraded bool
	hijacked func()
}

func (w *accessWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *accessWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *accessWriter) ReadFrom(src io.Reader) (int64, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return io.Copy(w.ResponseWriter, src)
}

func (w *accessWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *accessWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *accessWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	c, rw, err := hj.Hijack()
	if err == nil {
		w.status = http.StatusSwitchingProtocols
		w.upgraded = true
		if w.hijacked != nil {
			w.hijacked()
			w.hijacked = nil
		}
	}
	return c, rw, err
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	out := s.opts.AccessLog
	var mu sync.Mutex
	write := func(line string) {
		mu.Lock()
		defer mu.Unlock()
		io.WriteString(out, line+"\n")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &accessRec{}
		r = r.WithContext(context.WithValue(r.Context(), accessKey{}, rec))
		method, path := clean(r.Method, 10), logPath(r.URL.Path)
		tail := fmt.Sprintf("ip=%s ua=%q", clientIP(r.RemoteAddr), uaSummary(r.UserAgent()))
		line := func(kind, result string) string {
			return fmt.Sprintf("access %s %s %s %s %s %s %s", time.Now().UTC().Format(time.RFC3339), kind, rec.who(), method, path, result, tail)
		}
		aw := &accessWriter{ResponseWriter: w}
		aw.hijacked = func() { write(line("ws-open", "101")) }
		defer func() {
			took := time.Since(start)
			if aw.upgraded {
				write(line("ws-close", "dur="+took.Round(time.Second).String()))
				return
			}
			status := aw.status
			if p := recover(); p != nil {
				write(line("http", fmt.Sprintf("500 %dms", took.Milliseconds())))
				panic(p)
			}
			if status == 0 {
				status = http.StatusOK
			}
			write(line("http", fmt.Sprintf("%d %dms", status, took.Milliseconds())))
		}()
		next.ServeHTTP(aw, r)
	})
}

// logPath drops path segments that carry a secret.
func logPath(p string) string {
	if rest, ok := strings.CutPrefix(p, "/api/devices/"); ok && rest != "" {
		p = "/api/devices/{token}"
	}
	return clean(p, 200)
}

func clientIP(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	return clean(host, 64)
}

// uaSummary keeps the product token and the OS token, e.g. "Kyeot/12 Darwin/25.0.0".
func uaSummary(ua string) string {
	fields := strings.Fields(ua)
	if len(fields) == 0 {
		return "-"
	}
	out := fields[0]
	for _, f := range fields[1:] {
		if strings.HasPrefix(f, "Darwin/") || strings.HasPrefix(f, "Android") || strings.HasPrefix(f, "okhttp/") {
			out += " " + f
			break
		}
	}
	if strings.HasPrefix(out, "Mozilla/") {
		out = "browser"
		for _, b := range []string{"Edg/", "Chrome/", "Firefox/", "Safari/"} {
			if strings.Contains(ua, b) {
				out = "browser " + strings.TrimSuffix(b, "/")
				break
			}
		}
	}
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

// clean caps length and replaces spaces and control characters so fields stay split by spaces.
func clean(s string, max int) string {
	if len(s) > max {
		s = s[:max]
	}
	return strings.Map(func(r rune) rune {
		if r <= ' ' || r == '"' || unicode.IsControl(r) || !unicode.IsPrint(r) {
			return '_'
		}
		return r
	}, s)
}
