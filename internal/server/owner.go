package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"

	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/store"
)

// Owner leaving from the app: handing over ownership, or deleting the whole server.
// Handing over creates a new owner token in the server config (file only, never sent to the app). That token belongs to no account,
// and if the computer rotates it with pair --rotate and restarts, store.Reclaim creates a new owner account.

const (
	confirmWord       = "지우기"
	closeServerClosed = 4010 // websocket close code; the app shows "server closed"
)

const serverClosedMsg = "서버가 닫혔어요"

// ownerToken is the owner token in the current server config.
func (s *Server) ownerToken() string { return s.ownerTok.Load().(string) }

// newOwnerToken replaces the owner token file and the in-memory value. On failure nothing changes.
func (s *Server) newOwnerToken() (token string, restore func(), err error) {
	path := s.opts.OwnerTokenFile
	if path == "" {
		return "", nil, errors.New("no owner token file")
	}
	old, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	if err := writePrivate(path, token+"\n"); err != nil {
		return "", nil, err
	}
	prev := s.ownerToken()
	s.ownerTok.Store(token)
	return token, func() {
		if err := writePrivate(path, string(old)); err != nil {
			log.Printf("owner handover: could not restore the old token: %v", err)
		}
		s.ownerTok.Store(prev)
	}, nil
}

func hostName() string {
	h, _ := os.Hostname()
	return h
}

func confirmed(w http.ResponseWriter, r *http.Request, dst any) error {
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(dst) != nil {
		return reject(http.StatusBadRequest, "invalid json")
	}
	return nil
}

func (s *Server) ownerRoutes() {
	m := s.mux

	// shown before leaving: other users (admins first), my records to be deleted, the server computer's name
	m.HandleFunc("GET /api/owner/leave", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if owner != s.store.OwnerID() {
			return reject(http.StatusForbidden, "only the server owner")
		}
		others, err := s.store.Others()
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"others": others, "mine": s.store.Mine(owner), "host": hostName(),
			"canHandOver": s.opts.OwnerTokenFile != "", // a token given by environment variable cannot be changed by the server
		})
		return nil
	}))

	// hand over and delete only my data
	m.HandleFunc("POST /api/owner/handover", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		s.leaveMu.Lock()
		defer s.leaveMu.Unlock()
		if owner != s.store.OwnerID() { // re-check under the lock; an earlier handover may have changed the owner
			return reject(http.StatusForbidden, "only the server owner can hand over")
		}
		var b struct {
			HeirID  string `json:"heirId"`
			Confirm string `json:"confirm"`
		}
		if err := confirmed(w, r, &b); err != nil {
			return err
		}
		if b.Confirm != confirmWord {
			return reject(http.StatusBadRequest, "「지우기」를 입력해 주세요")
		}
		if s.opts.OwnerTokenFile == "" {
			return reject(http.StatusConflict, "소유자 토큰을 환경변수로 주고 있어 넘길 수 없어요")
		}
		token, restore, err := s.newOwnerToken()
		if err != nil {
			return err
		}
		old := owner
		d, err := s.hub.HandOver(old, b.HeirID, token)
		if err != nil {
			restore()
			if errors.Is(err, store.ErrNotFound) {
				return reject(http.StatusNotFound, "이어받을 사용자가 없어요")
			}
			return err
		}
		s.removeUploadFiles("owner handover", d.Uploads)
		s.dropOwnerCodes(old)
		log.Printf("owner handover: %.8s → %.8s, agents %d, uploads %d", old, b.HeirID, len(d.Agents), len(d.Uploads))
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	// delete everything with the server; afterwards every request except /version gets 410
	m.HandleFunc("POST /api/server/close", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		s.leaveMu.Lock()
		defer s.leaveMu.Unlock()
		if owner != s.store.OwnerID() {
			return reject(http.StatusForbidden, "only the server owner can close the server")
		}
		var b struct {
			Confirm string `json:"confirm"`
		}
		if err := confirmed(w, r, &b); err != nil {
			return err
		}
		if b.Confirm != confirmWord {
			return reject(http.StatusBadRequest, "「지우기」를 입력해 주세요")
		}
		s.closed.Store(true) // also blocks requests arriving during the delete
		uploads, err := s.hub.CloseServer()
		if err != nil {
			s.closed.Store(false)
			return err
		}
		s.removeUploadFiles("server close", uploads)
		s.removeLinkPreviewFiles()
		s.removeBackups()
		log.Printf("server close: everyone's records deleted, uploads %d", len(uploads))
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	m.HandleFunc("POST /api/me/handover-seen", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if err := s.store.HandoverSeen(owner); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))
}

// dropOwnerCodes discards the codes and pending approvals issued by the person who left.
func (s *Server) dropOwnerCodes(owner string) {
	s.pairs.dropOwner(owner)
	s.roomInvites.dropOwner(owner)
	s.ownerInvites.dropOwner(owner)
	for _, jr := range s.joins.dropOwner(owner) {
		s.hub.toAdmins(protocol.ClientMessage{Type: "join.done", Join: &jr}, nil)
	}
}
