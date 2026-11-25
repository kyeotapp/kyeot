package server

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// LocalHermes is used to attach one Hermes (one profile) on the same computer after user consent.
type LocalHermes struct {
	ID      string // profile id. "default" if empty
	Name    string // agent name
	Label   string // profile name shown on screen. Name if empty
	Model   string
	Gateway string                                  // running, stopped. "" if unknown
	NoUI    bool                                    // does not use the in-Mac screen address (7701)
	IDPath  string                                  // agent id file. If present, consent was given
	Find    func() (found, hasKey bool)             // does not pass the key value
	Probe   func(ctx context.Context) string        // problem code. "" if reachable
	Start   func(ctx context.Context, token string) // runs the adapter. Stops when ctx ends
}

// This computer's Hermes is seen only by the server's first user
var errLocalOwnerOnly = reject(http.StatusForbidden, "only the server owner can use local hermes")

// HTTP status per problem code
var localProblemStatus = map[string]int{
	"not_found":   http.StatusNotFound,
	"no_key":      http.StatusBadRequest,
	"unreachable": http.StatusBadGateway,
	"bad_key":     http.StatusBadGateway,
	"gateway_off": http.StatusServiceUnavailable,
	"handed_over": http.StatusConflict, // after the owner was handed over, before rotate. This computer's account is not the owner
}

type localHermes struct {
	mu      sync.Mutex
	list    func() []LocalHermes // looked up again on every call
	conns   map[string]*localConn
	probing chan struct{} // while a connection attempt is in progress, wait until it closes
}

type localConn struct {
	agentID string
	problem string // why it could not attach at startup
	stop    context.CancelFunc
}

type localHermesStatus struct {
	Found     bool                 `json:"found"`
	Connected bool                 `json:"connected"`
	AgentID   string               `json:"agentId,omitempty"`
	Problem   string               `json:"problem,omitempty"`
	Profiles  []localHermesProfile `json:"profiles,omitempty"`
}

type localHermesProfile struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Model     string `json:"model,omitempty"`
	Gateway   string `json:"gateway,omitempty"`
	Found     bool   `json:"found"`
	HasKey    bool   `json:"hasKey"`
	Connected bool   `json:"connected"`
	AgentID   string `json:"agentId,omitempty"`
	Problem   string `json:"problem,omitempty"`
	UI        bool   `json:"ui"`
}

// SetLocalHermes registers one Hermes on this computer. If never called, it is treated as not found.
func (s *Server) SetLocalHermes(cfg LocalHermes) {
	s.SetLocalHermesList(func() []LocalHermes { return []LocalHermes{cfg} })
}

// SetLocalHermesList registers the list of Hermes profiles on this computer. The first entry is the default that old apps see.
func (s *Server) SetLocalHermesList(list func() []LocalHermes) {
	s.local.mu.Lock()
	defer s.local.mu.Unlock()
	s.local.list = func() []LocalHermes {
		out := list()
		for i := range out {
			if out[i].ID == "" {
				out[i].ID = "default"
			}
		}
		return out
	}
	if s.local.conns == nil {
		s.local.conns = map[string]*localConn{}
	}
}

func (l *localHermes) conn(id string) *localConn {
	c := l.conns[id]
	if c == nil {
		c = &localConn{}
		l.conns[id] = c
	}
	return c
}

func (l *localHermes) lookup(id string) (LocalHermes, bool) {
	if l.list == nil {
		return LocalHermes{}, false
	}
	if id == "" {
		id = "default"
	}
	for _, p := range l.list() {
		if p.ID == id {
			return p, true
		}
	}
	return LocalHermes{}, false
}

// ResumeLocalHermes attaches only profiles that already consented (have an id file). consented says whether any such file existed.
func (s *Server) ResumeLocalHermes() (consented bool, err error) {
	if s.closed.Load() {
		return false, nil
	}
	l := &s.local
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.list == nil {
		return false, nil
	}
	var errs []error
	for _, p := range l.list() {
		if _, err := os.Lstat(p.IDPath); errors.Is(err, os.ErrNotExist) {
			continue
		}
		consented = true
		c := l.conn(p.ID)
		token, err := s.LocalAgent(p.Name, p.IDPath)
		if err != nil {
			c.problem = "agent_file"
			if errors.Is(err, ErrLocalAgentUnlinked) {
				c.problem = "unlinked"
			}
			errs = append(errs, fmt.Errorf("%s: %w", p.ID, err))
			continue
		}
		if err := s.startLocal(p, token); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.ID, err))
		}
	}
	return consented, errors.Join(errs...)
}

// ConnectLocalHermes attaches when the user presses connect. Empty profile means default. If already attached, the same id. If problem is set, it did not attach.
func (s *Server) ConnectLocalHermes(ctx context.Context, profile string) (agentID, problem string, err error) {
	l := &s.local
	l.mu.Lock()
	defer l.mu.Unlock()
	for l.probing != nil { // the lock is released while checking, so other connection attempts wait until it ends
		wait := l.probing
		l.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			l.mu.Lock()
			return "", "", ctx.Err()
		}
		l.mu.Lock()
	}
	if s.store.HandedOver() { // leave the id file alone. It is used again if the computer becomes owner again via rotate
		return "", "handed_over", nil
	}
	p, ok := l.lookup(profile)
	if !ok {
		return "", "not_found", nil
	}
	if id := s.liveLocalID(p.ID); id != "" {
		return id, "", nil
	}
	found, hasKey := p.Find()
	switch {
	case !hasKey && !found:
		return "", "not_found", nil
	case !hasKey:
		return "", "no_key", nil
	}
	done := make(chan struct{})
	l.probing = done
	defer func() { l.probing = nil; close(done) }()
	l.mu.Unlock()
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	pr := p.Probe(pctx)
	cancel()
	l.mu.Lock()
	if pr != "" {
		return "", pr, nil
	}
	token, err := s.LocalAgent(p.Name, p.IDPath)
	if errors.Is(err, ErrLocalAgentUnlinked) { // pressing again after an unlink attaches anew
		if err := os.Remove(p.IDPath); err != nil {
			return "", "", err
		}
		token, err = s.LocalAgent(p.Name, p.IDPath)
	}
	if err != nil {
		return "", "", err
	}
	if err := s.startLocal(p, token); err != nil {
		return "", "", err
	}
	return l.conns[p.ID].agentID, "", nil
}

// startLocal is called holding l.mu. The adapter started earlier for that profile is stopped.
func (s *Server) startLocal(p LocalHermes, token string) error {
	if s.ctx.Err() != nil {
		return s.ctx.Err() // closing. Do not add to bg
	}
	a, err := s.store.AgentByToken(token)
	if err != nil {
		return err
	}
	s.hub.agentForgets(a.ID) // restarts anew every time, so the previous process's approvals cannot be continued
	c := s.local.conn(p.ID)
	if c.stop != nil {
		c.stop()
	}
	ctx, cancel := context.WithCancel(s.ctx)
	c.agentID, c.problem, c.stop = a.ID, "", cancel
	s.bg.Add(1)
	go func() {
		defer s.bg.Done() // Close waits for the adapter to finish
		p.Start(ctx, token)
	}()
	return nil
}

// liveLocalID is the id of the agent attached to that profile if not released. Called holding l.mu.
func (s *Server) liveLocalID(profile string) string {
	c := s.local.conns[profile]
	if c == nil || c.agentID == "" {
		return ""
	}
	if removed, err := s.store.AgentRemoved(c.agentID); err != nil || removed {
		return ""
	}
	return c.agentID
}

func (s *Server) localProfileStatus(p LocalHermes) localHermesProfile {
	st := localHermesProfile{ID: p.ID, Name: cmp.Or(p.Label, p.Name), Model: p.Model, Gateway: p.Gateway, UI: !p.NoUI}
	st.Found, st.HasKey = p.Find()
	if id := s.liveLocalID(p.ID); id != "" {
		st.Found, st.Connected, st.AgentID = true, true, id
		return st
	}
	switch {
	case !st.Found:
	case !st.HasKey:
		st.Problem = "no_key"
	default:
		if c := s.local.conns[p.ID]; c != nil {
			st.Problem = c.problem
		}
	}
	return st
}

func (s *Server) localHermesStatus() localHermesStatus {
	if s.store.HandedOver() { // this computer's Hermes is not the new owner's
		return localHermesStatus{Problem: "handed_over"}
	}
	l := &s.local
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.list == nil {
		return localHermesStatus{}
	}
	var out localHermesStatus
	for i, p := range l.list() {
		st := s.localProfileStatus(p)
		if i == 0 { // old apps see only default
			out.Found, out.Connected, out.AgentID, out.Problem = st.Found, st.Connected, st.AgentID, st.Problem
		}
		out.Profiles = append(out.Profiles, st)
	}
	return out
}

func (s *Server) localHermesRoutes() {
	s.mux.HandleFunc("GET /api/local-hermes", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if owner != s.store.OwnerID() {
			return errLocalOwnerOnly
		}
		writeJSON(w, http.StatusOK, s.localHermesStatus())
		return nil
	}))
	s.mux.HandleFunc("POST /api/local-hermes/connect", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if owner != s.store.OwnerID() {
			return errLocalOwnerOnly
		}
		var body struct {
			Profile string `json:"profile"`
		}
		if r.ContentLength != 0 {
			if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
				return reject(http.StatusBadRequest, "bad json")
			}
		}
		id, problem, err := s.ConnectLocalHermes(r.Context(), cmp.Or(body.Profile, r.URL.Query().Get("profile")))
		switch {
		case err != nil:
			return err
		case problem != "":
			writeJSON(w, localProblemStatus[problem], map[string]string{"error": "local hermes: " + problem, "problem": problem})
		default:
			writeJSON(w, http.StatusOK, map[string]string{"agentId": id})
		}
		return nil
	}))
}
