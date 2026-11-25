package server

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"sync"
	"time"
)

// Pairing codes for attaching an agent on another computer. The app gets a 6-digit code and that computer's `kyeot connect` exchanges it for a token.
// The token is never shown. A code lasts 10 minutes and works once.
const (
	pairTTL      = 10 * time.Minute
	pairMaxFails = 10 // wrong guesses one address may make in 10 minutes
	pairCodeMiss = 30 // wrong guesses one live code tolerates; past it only that code is dropped (no global lock, which would let others block everyone)
	pairHeader   = "X-Kyeot-Pair"
)

type pairCode struct {
	owner, agentID string
	expires        time.Time
	misses         int
}

type pairStore struct {
	mu    sync.Mutex
	codes map[string]pairCode
	fails map[string][]time.Time
	now   func() time.Time
}

func (p *pairStore) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// issue mints a new code for one agent and drops that agent's old code.
func (p *pairStore) issue(owner, agentID string) (string, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.codes == nil {
		p.codes = map[string]pairCode{}
	}
	now := p.clock()
	for c, v := range p.codes {
		if v.agentID == agentID || now.After(v.expires) {
			delete(p.codes, c)
		}
	}
	for {
		n, _ := rand.Int(rand.Reader, big.NewInt(1_000_000))
		code := fmt.Sprintf("%06d", n.Int64())
		if _, used := p.codes[code]; !used {
			exp := now.Add(pairTTL)
			p.codes[code] = pairCode{owner: owner, agentID: agentID, expires: exp}
			return code, exp
		}
	}
}

// dropOwner drops every code that user issued (on revoke).
func (p *pairStore) dropOwner(owner string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for c, v := range p.codes {
		if v.owner == owner {
			delete(p.codes, c)
		}
	}
}

// drop discards an agent's code (on detach).
func (p *pairStore) drop(agentID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for c, v := range p.codes {
		if v.agentID == agentID {
			delete(p.codes, c)
		}
	}
}

func fresh(ts []time.Time, now time.Time) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if now.Sub(t) < pairTTL {
			out = append(out, t)
		}
	}
	return out
}

// take spends and deletes a code. A wrong guess counts against the address and each live code. blocked means the address is blocked.
func (p *pairStore) take(code, from string) (pc pairCode, ok, blocked bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clock()
	if p.fails == nil {
		p.fails = map[string][]time.Time{}
	}
	for k, ts := range p.fails { // drop stale address records
		if ts = fresh(ts, now); len(ts) == 0 {
			delete(p.fails, k)
		} else {
			p.fails[k] = ts
		}
	}
	if len(p.fails[from]) >= pairMaxFails {
		return pairCode{}, false, true
	}
	pc, ok = p.codes[code]
	if ok {
		delete(p.codes, code)
	}
	if ok && !now.After(pc.expires) {
		return pc, true, false
	}
	p.fails[from] = append(p.fails[from], now)
	for c, v := range p.codes {
		if v.misses++; v.misses >= pairCodeMiss || now.After(v.expires) {
			delete(p.codes, c)
		} else {
			p.codes[c] = v
		}
	}
	return pairCode{}, false, false
}

// pairKey is the unit failures are counted by; IPv6 is grouped by /64 because one person can change addresses easily.
func pairKey(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
	}
	return host
}

// pairGuard stops browser pages from calling the tokenless pairing routes behind the user's back.
// Browsers add Origin, so only then is the X-Kyeot-Pair header required (which forces a preflight); programs like curl work without it.
func pairGuard(r *http.Request) bool {
	return r.Header.Get(pairHeader) == "1" || r.Header.Get("Origin") == ""
}

func (s *Server) pairRoutes() {
	// app: pairing code for an agent not yet attached (or to be re-attached)
	s.mux.HandleFunc("POST /api/agents/{id}/pair", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		id := r.PathValue("id")
		if !s.store.AgentOwnedBy(id, owner) {
			return reject(http.StatusNotFound, "agent not found")
		}
		code, exp := s.pairs.issue(owner, id)
		writeJSON(w, http.StatusOK, map[string]any{"code": code, "expiresAt": exp.UnixMilli()})
		return nil
	}))
	// agent-side computer: exchanges the code for a token; called without an owner token, so the code is the key
	s.mux.HandleFunc("POST /pair/agent", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Code string `json:"code"`
		}
		if !pairGuard(r) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": pairHeader + ": 1 헤더가 필요하다"})
			return
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body) != nil || len(body.Code) != 6 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "6자리 코드가 필요하다"})
			return
		}
		pc, ok, blocked := s.pairs.take(body.Code, pairKey(r.RemoteAddr))
		if blocked {
			log.Printf("pairing blocked: %s (too many wrong codes)", r.RemoteAddr)
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "코드를 너무 많이 틀렸다. 잠시 뒤에 다시"})
			return
		}
		if !ok {
			log.Printf("pairing failed: %s", r.RemoteAddr)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "코드가 틀렸거나 만료됐다. 앱에서 새 코드를 받는다"})
			return
		}
		token, err := s.hub.RotateAgentToken(pc.owner, pc.agentID)
		log.Printf("pairing %s: agent %.8s, %s", map[bool]string{true: "ok", false: "failed (no agent)"}[err == nil], pc.agentID, r.RemoteAddr)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "에이전트가 없거나 해제됐다"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"token": token})
	})
}
