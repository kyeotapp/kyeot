package server

import (
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/kyeotapp/kyeot/internal/store"
)

var releaseVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// versionRoutes reports the server version, protocol, feature labels, and accepted motion-character names. Read by the install script and the app. No auth.
func (s *Server) versionRoutes() {
	s.mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		if !releaseVersion.MatchString(s.opts.Version) {
			http.Error(w, "개발판 서버라 버전이 없다", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		body := map[string]any{"version": s.opts.Version, "protocol": Protocol, "minClientProtocol": MinClientProtocol, "features": s.features(), "avatarPresets": store.MotionPresets, "closed": s.closed.Load(), "serverId": s.store.ServerID()}
		if v := s.latestVersion(); v != "" {
			body["latest"] = v // newest public release, from the daily update check
		}
		json.NewEncoder(w).Encode(body)
	})
}
