package server

import (
	"net/http"

	"github.com/coder/websocket"
)

func (s *Server) accept(w http.ResponseWriter, r *http.Request) (*peer, bool) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: originHosts(s.opts.AllowedOrigins)})
	if err != nil {
		return nil, false
	}
	conn.SetReadLimit(1 << 20)
	s.reqMu.RLock()
	defer s.reqMu.RUnlock()
	if s.stopping { // avoid racing Close's conns.Wait
		conn.Close(websocket.StatusGoingAway, "server shutdown")
		return nil, false
	}
	p := newPeer(s.ctx, conn)
	s.conns.Add(1)
	go p.writeLoop()
	return p, true
}

// readLoop passes messages along until the connection ends, then calls done and cleans up.
func (s *Server) readLoop(p *peer, onMessage func([]byte), done func()) {
	defer s.conns.Done()
	defer done()
	defer p.cancel()
	for {
		_, data, err := p.conn.Read(p.ctx)
		if err != nil {
			return
		}
		onMessage(data)
	}
}

func (s *Server) serveAgent(w http.ResponseWriter, r *http.Request) {
	token := bearer(r)
	auth, err := s.store.AgentByToken(token)
	if token == "" || err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	noteOwner(r, auth.OwnerID, auth.ID)
	p, ok := s.accept(w, r)
	if !ok {
		return
	}
	if !s.hub.attachAgent(p, auth, token) {
		s.conns.Done()
		return
	}
	go p.pingLoop(s.opts.PingInterval, func() { s.hub.checkAgent(p, auth) })
	s.readLoop(p, func(data []byte) { s.hub.onAgentMessage(p, auth, data) }, func() { s.hub.detachAgent(p, auth) })
}

func (s *Server) serveClient(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.ownerOf(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	p, ok := s.accept(w, r)
	if !ok {
		return
	}
	s.hub.attachClient(p, owner)
	// if close or handover cut it just before registration, cut it here
	if s.closed.Load() {
		p.close(closeServerClosed, "server closed")
	} else if now, ok := s.ownerOf(r); !ok || now != owner {
		p.close(4003, "owner revoked")
	}
	go p.pingLoop(s.opts.PingInterval, nil)
	s.readLoop(p, func([]byte) {}, func() { s.hub.detachClient(p) })
}
