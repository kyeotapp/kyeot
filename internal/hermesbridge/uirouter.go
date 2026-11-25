package hermesbridge

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	pathpkg "path"
	"regexp"
	"strings"
	"sync"
	"time"
)

// UIRouter lets several bridges (Hermes profiles) on one computer share a single local address.
// default uses /ui/…; other profiles go to their bridge via /h/<key>/ui/…. An unknown key gives 404.
type UIRouter struct {
	addr string

	mu     sync.Mutex
	srv    *http.Server
	routes map[string]*uiRoute
}

type uiRoute struct {
	b *Bridge
	h http.Handler
}

// UIKeyPattern is the shape of a profile key.
var UIKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

func NewUIRouter(addr string) *UIRouter {
	return &UIRouter{addr: addr, routes: map[string]*uiRoute{}}
}

// attach puts b at the key slot and opens the address if not yet open. The returned function detaches only while the slot is still b.
func (r *UIRouter) attach(ctx context.Context, key string, profile bool, b *Bridge) (func(), error) {
	if (profile || key != "") && !UIKeyPattern.MatchString(key) { // keep a profile from taking the default slot
		return nil, fmt.Errorf("profile 화면 키 모양이 틀렸다")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.srv == nil {
		if err := r.listen(ctx); err != nil {
			return nil, err
		}
	}
	route := &uiRoute{b: b, h: b.uiHandler()}
	r.routes[key] = route
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.routes[key] == route {
			delete(r.routes, key)
		}
	}, nil
}

// listen is called holding r.mu. The opened address stays until Close.
func (r *UIRouter) listen(ctx context.Context) error {
	host, _, err := net.SplitHostPort(r.addr)
	if err != nil {
		return fmt.Errorf("UI 주소 %q: %w", r.addr, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("UI 주소는 루프백 IP 여야 한다: %q", r.addr)
	}
	ln, err := listenRetry(ctx, r.addr)
	if err != nil {
		return err
	}
	r.addr = ln.Addr().String()
	r.srv = &http.Server{Handler: localOnly(http.HandlerFunc(r.serve)), ReadHeaderTimeout: 5 * time.Second}
	go func(srv *http.Server) {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("UI address: %v", err)
		}
	}(r.srv)
	log.Printf("local address http://%s", r.addr)
	return nil
}

// Addr is the address actually opened; before opening, the configured value.
func (r *UIRouter) Addr() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.addr
}

// Close closes the address.
func (r *UIRouter) Close() {
	r.mu.Lock()
	srv := r.srv
	r.srv, r.routes = nil, map[string]*uiRoute{}
	r.mu.Unlock()
	if srv != nil {
		srv.Close()
	}
}

func (r *UIRouter) serve(w http.ResponseWriter, req *http.Request) {
	key, path := "", req.URL.Path
	if rest, ok := strings.CutPrefix(path, "/h/"); ok {
		key, path, _ = strings.Cut(rest, "/")
		path = "/" + path
		if !UIKeyPattern.MatchString(key) {
			writeUI(w, http.StatusNotFound, uiResult{Reason: "모르는 화면 주소다"})
			return
		}
	}
	r.mu.Lock()
	route := r.routes[key]
	r.mu.Unlock()
	if route == nil {
		writeUI(w, http.StatusNotFound, uiResult{Reason: "이 주소에 붙은 에이전트가 없다"})
		return
	}
	if path != pathpkg.Clean(path) || strings.Contains(strings.ToLower(req.URL.RawPath), "%2f") { // ServeMux would redirect a non-canonical path to default with 307
		writeUI(w, http.StatusNotFound, uiResult{Reason: "모르는 화면 주소다"})
		return
	}
	req2 := req.Clone(req.Context())
	req2.URL.Path, req2.URL.RawPath = path, ""
	route.h.ServeHTTP(w, req2)
}
