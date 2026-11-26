package server

import (
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kyeotapp/kyeot/internal/linkpreview"
	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/push"
	"github.com/kyeotapp/kyeot/internal/sensor/disk"
	"github.com/kyeotapp/kyeot/internal/sensor/netwatch"
	"github.com/kyeotapp/kyeot/internal/sensor/system"
	"github.com/kyeotapp/kyeot/internal/store"
)

type Options struct {
	DBPath     string
	OwnerToken string
	// Owner token file. On ownership handover the server writes the new token here. If empty, handover is refused
	OwnerTokenFile string
	// Attachment URL signing key. If empty, LegacyUploadKey(OwnerToken)
	UploadKey    []byte
	PingInterval time.Duration
	// If an agent stays gone this long after disconnecting, its running tasks are closed as failed. 0 means 1 minute.
	// Not closed at once, so a bridge that drops briefly and reconnects can still deliver queued replies
	AgentGrace time.Duration
	// Origin of the browser app, e.g. "http://localhost:8081"
	AllowedOrigins []string
	// Source networks to accept. nil means no restriction; an empty list is rejected by New. Build with ParseNets
	AllowNets []*net.IPNet
	// Attachment folder. If empty, uploads/ next to the DB
	UploadDir string
	// Per-user total of attachments not yet sent (bytes). 0 means 2GB
	UploadQuota int64
	// Per-user attachment total, including those attached to messages (bytes). 0 means 20GB
	UploadTotalQuota int64
	// App JS update folder. If empty, updates/ next to the DB
	UpdateDir string
	// Daily backup folder. If empty, backups/ next to the DB
	BackupDir string
	// Speech recognition. If the model is empty, /api/transcribe returns 501
	WhisperModel string
	WhisperBin   string
	ConvertBin   string
	// Phone notifications. nil means none are sent
	Push push.Sender
	// Server version. "dev" makes /version return 503
	Version string
	// Sensor record folder. If empty, the server does not measure network or disk
	SensorDir     string
	SensorProbe   netwatch.Prober                     // for tests
	DiskMeasure   func() (disk.Usage, error)          // for tests
	SystemMeasure func(context.Context) system.Sample // for tests
	SensorStale   time.Duration                       // bundle age that triggers re-aggregation on a screen request. 0 means 1 minute
	SensorWait    time.Duration                       // how long a refresh request waits for a new bundle. 0 means 15 seconds
	// File to write the "time header" when a same-PC proxy setup is detected. If empty, only logged
	ProxyMarkFile string
	// Link preview image folder. If empty, link-previews/ next to the DB
	LinkPreviewDir     string
	LinkPreviewFetcher *linkpreview.Fetcher // for tests. nil means the real internet
	// Access log destination (KYEOT_ACCESS_LOG). nil means off
	AccessLog io.Writer
	// Newest-release file checked once at start and every LatestEvery (KYEOT_UPDATE_CHECK). Empty means off. Must be https
	LatestURL    string
	LatestEvery  time.Duration // 0 means 24 hours
	LatestClient *http.Client  // for tests (transport only)
}

type Server struct {
	opts     Options
	store    *store.Store
	hub      *hub
	ctx      context.Context
	cancel   context.CancelFunc
	conns    sync.WaitGroup
	reqMu    sync.RWMutex   // guards stopping and reqs.Add together
	reqs     sync.WaitGroup // in-flight HTTP requests. Close waits for them before closing the DB
	stopping bool
	backupMu sync.Mutex // serializes the daily backup and the backup cleanup on server close
	mux      *http.ServeMux
	pairs    pairStore
	// User invite and room share codes. Same rules as pairing codes (6 digits, 10 minutes, single use, wrong-guess limit)
	ownerInvites pairStore
	roomInvites  pairStore
	joins        joinStore // people waiting for admin approval (in memory, 30 minutes)
	local        localHermes
	bg           sync.WaitGroup
	transcribing chan struct{}
	proxy        proxySeen
	ownerTok     atomic.Value // owner token from server config. Changes on ownership handover
	closed       atomic.Bool  // the whole server was deleted and closed
	leaveMu      sync.Mutex   // serializes ownership handover and server close, as one unit from token file swap to DB
	previews     *linkPreviews
	latest       atomic.Pointer[string] // newest released version seen by latestLoop
}

func New(opts Options) (*Server, error) {
	if opts.PingInterval == 0 {
		opts.PingInterval = 30 * time.Second
	}
	if opts.AllowNets != nil && len(opts.AllowNets) == 0 {
		return nil, errors.New("AllowNets: 빈 목록")
	}
	if opts.UploadQuota == 0 {
		opts.UploadQuota = defaultUploadQuota
	}
	if opts.UploadTotalQuota == 0 {
		opts.UploadTotalQuota = defaultUploadTotalQuota
	}
	if len(opts.UploadKey) == 0 {
		opts.UploadKey = LegacyUploadKey(opts.OwnerToken)
	}
	if opts.ProxyMarkFile != "" {
		if mark, err := ReadProxyMark(opts.ProxyMarkFile); mark != "" {
			log.Printf("warning: saw a same-PC proxy signal in the previous run (%s). %s", mark, ProxyWarning)
			os.Remove(opts.ProxyMarkFile) // rewritten if seen again in this run
		} else if err != nil {
			log.Printf("proxy mark file: %v", err)
		}
	}
	st, err := store.Open(opts.DBPath)
	if err != nil {
		return nil, err
	}
	if demoted, err := st.Reclaim(opts.OwnerToken, cmp.Or(hostName(), "주인")); err != nil {
		st.Close()
		return nil, err
	} else if demoted != "" {
		log.Printf("owner token changed after a handover, so a new owner account was created on this computer. %.8s became an admin", demoted)
	}
	if err := st.MarkAgentsOffline(); err != nil { // state left by the previous process. Connected agents will write idle again soon
		st.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{opts: opts, store: st, hub: newHub(st), ctx: ctx, cancel: cancel, mux: http.NewServeMux(), transcribing: make(chan struct{}, maxTranscribes)}
	s.ownerTok.Store(opts.OwnerToken)
	s.closed.Store(st.Closed())
	s.hub.push, s.hub.pushCtx = opts.Push, ctx
	s.hub.grace = cmp.Or(opts.AgentGrace, time.Minute)
	s.hub.closeLeftovers()
	s.hub.sign = s.signEvent
	s.hub.signFile = s.fileURL
	s.routes()
	s.uploadRoutes()
	s.deviceRoutes()
	s.updateRoutes()
	s.pairRoutes()
	s.localHermesRoutes()
	s.ownerRoutes()
	s.versionRoutes()
	s.sensorRoutes()
	s.linkPreviewRoutes()
	if !s.closed.Load() { // a closed server doesn't pile measurements into an empty DB
		s.startSensors()
	}
	s.sweepUploads(time.Now().Add(-staleUpload))
	s.bg.Add(2)
	go s.sweepLoop()
	go s.backupLoop()
	if opts.LatestURL != "" && releaseVersion.MatchString(opts.Version) && !s.closed.Load() {
		s.bg.Add(1)
		go s.latestLoop()
	}
	return s, nil
}

func (s *Server) Handler() http.Handler {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.reqMu.RLock()
		if s.stopping {
			s.reqMu.RUnlock()
			http.Error(w, "server shutting down", http.StatusServiceUnavailable)
			return
		}
		s.reqs.Add(1)
		s.reqMu.RUnlock()
		defer s.reqs.Done()
		s.noteProxy(r)
		if !s.allowed(r.RemoteAddr) {
			http.Error(w, "forbidden network", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && slices.Contains(s.opts.AllowedOrigins, origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, "+pairHeader+", X-Kyeot-Poll") // allowed origins only (the app's web build)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		if s.closed.Load() && r.URL.Path != "/version" {
			writeJSON(w, http.StatusGone, map[string]any{"error": serverClosedMsg, "closed": true})
			return
		}
		s.mux.ServeHTTP(w, r)
	})
	if s.opts.AccessLog == nil {
		return h
	}
	return s.accessLog(h)
}

func (s *Server) allowed(remote string) bool {
	if len(s.opts.AllowNets) == 0 {
		return true
	}
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(host)
	for _, n := range s.opts.AllowNets {
		if ip != nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

// netAliases are the names usable in KYEOT_NETS.
var netAliases = map[string][]string{
	"loopback":  {"127.0.0.0/8", "::1/128"},
	"tailscale": {"100.64.0.0/10", "fd7a:115c:a1e0::/48"},                       // public-ok
	"lan":       {"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fe80::/10"}, // public-ok
	"all":       {"0.0.0.0/0", "::/0"},
}

// ParseNets turns names like "loopback,tailscale" or a CIDR list into networks. "all" means everything.
func ParseNets(spec string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		cidrs, ok := netAliases[part]
		if !ok {
			cidrs = []string{part}
		}
		for _, c := range cidrs {
			_, n, err := net.ParseCIDR(c)
			if err != nil {
				return nil, fmt.Errorf("KYEOT_NETS: %q", part)
			}
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("KYEOT_NETS: 비어 있다")
	}
	return out, nil
}

// originHosts converts to the "host:port" form that websocket.Accept takes.
func originHosts(origins []string) []string {
	var hosts []string
	for _, o := range origins {
		if u, err := url.Parse(o); err == nil && u.Host != "" {
			hosts = append(hosts, u.Host)
		}
	}
	return hosts
}

// Close drops all connections and closes the DB after connection cleanup finishes.
func (s *Server) Close() error {
	s.reqMu.Lock()
	s.stopping = true
	s.reqMu.Unlock()
	s.hub.closeAll()
	done := make(chan struct{})
	go func() { s.conns.Wait(); close(done) }()
	select { // wait first so the close code (1001) gets sent
	case <-done:
	case <-time.After(closeGrace):
	}
	s.cancel()
	s.conns.Wait()
	s.bg.Wait()
	s.hub.pushes.Wait()
	reqs := make(chan struct{})
	go func() { s.reqs.Wait(); close(reqs) }()
	select { // so long requests like transcription and uploads don't write to a closed DB
	case <-reqs:
	case <-time.After(reqGrace):
		log.Printf("waited %s for in-flight requests, which did not finish; closing the DB", reqGrace)
	}
	return s.store.Close()
}

const (
	closeGrace = 2 * time.Second
	reqGrace   = 15 * time.Second // within launchd's default exit wait (20 seconds)
)

func sameToken(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

func bearer(r *http.Request) string {
	if t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return t
	}
	if r.URL.Path == "/client" || r.URL.Path == "/agent" { // browser websockets can't set headers
		return r.URL.Query().Get("token")
	}
	return ""
}

// ownerOf resolves an owner token to a user. The first user uses the server config token, the rest the DB.
func (s *Server) ownerOf(r *http.Request) (string, bool) {
	owner, ok := s.ownerByToken(bearer(r))
	if ok {
		noteOwner(r, owner, "")
	}
	return owner, ok
}

func (s *Server) ownerByToken(t string) (string, bool) {
	if sameToken(t, s.ownerToken()) { // the computer owner's account. After a handover and before rotate, it is nobody
		host := s.store.HostOwner()
		return host, host != ""
	}
	return s.store.OwnerByToken(t)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

type apiHandler func(w http.ResponseWriter, r *http.Request, owner string) error

func (s *Server) api(h apiHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		owner, ok := s.ownerOf(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		err := h(w, r, owner)
		var ae *ActionError
		switch {
		case err == nil:
		case errors.As(err, &ae):
			writeJSON(w, ae.Status, map[string]string{"error": ae.Msg})
		default:
			log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		}
	}
}

func (s *Server) ownedRoom(id, owner string) (protocol.Room, error) {
	room, roomOwner, err := s.store.Room(id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && roomOwner != owner) {
		return room, reject(http.StatusNotFound, "room not found")
	}
	return room, err
}

// readBody extracts all string fields of a JSON body. fields are the ones that must be non-empty.
func readBody(r *http.Request, fields ...string) (map[string]string, error) {
	var body map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(&body); err != nil {
		return nil, reject(http.StatusBadRequest, "invalid json")
	}
	out := map[string]string{}
	for k, v := range body {
		if str, ok := v.(string); ok {
			out[k] = strings.TrimSpace(str)
		}
	}
	for _, f := range fields {
		v, _ := body[f].(string)
		if strings.TrimSpace(v) == "" {
			return nil, reject(http.StatusBadRequest, f+" is required")
		}
	}
	return out, nil
}

func (s *Server) routes() {
	s.agentRoutes()
	s.taskRoutes()
	s.routineRoutes()
	s.roomListRoutes()
	s.roomSafetyRoutes()
	s.roomRoutes()
	s.memberRoutes()
	s.listRoutes()

	m := s.mux
	m.HandleFunc("GET /agent", s.serveAgent)
	m.HandleFunc("GET /client", s.serveClient)
	s.uiRoutes(m)
}
