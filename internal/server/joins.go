package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/push"
)

// People without an account join through a room invite code. Immediately if the room owner is an admin, otherwise an admin approves.
// Waiting requests live only in memory. No key is stored before approval, and requests are made again after a server restart.

const (
	joinTTL        = 30 * time.Minute
	joinMaxPending = 20 // above this, new requests are refused (prevents flooding admin notifications)
)

type joinReq struct {
	protocol.JoinRequest
	roomOwner string
	poll      [32]byte // only the requester gets the result (sha256)
	status    string   // pending, approved, rejected
	token     string   // handed over once on approval, then deleted
	ownerID   string
	expires   time.Time
}

type joinStore struct {
	mu sync.Mutex
	m  map[string]*joinReq
}

func (j *joinStore) sweep(now time.Time) {
	for id, r := range j.m {
		if now.After(r.expires) {
			delete(j.m, id)
		}
	}
}

func (j *joinStore) pending() []protocol.JoinRequest {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.sweep(time.Now())
	out := []protocol.JoinRequest{}
	for _, r := range j.m {
		if r.status == "pending" {
			out = append(out, r.JoinRequest)
		}
	}
	return out
}

// dropOwner discards requests waiting to join that person's rooms.
func (j *joinStore) dropOwner(owner string) []protocol.JoinRequest {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []protocol.JoinRequest
	for id, r := range j.m {
		if r.roomOwner == owner && r.status == "pending" {
			delete(j.m, id)
			out = append(out, r.JoinRequest)
		}
	}
	return out
}

func newPoll() (string, [32]byte) {
	b := make([]byte, 32)
	rand.Read(b)
	p := base64.RawURLEncoding.EncodeToString(b)
	return p, sha256.Sum256([]byte(p))
}

// toAdmins sends to the owner and admin screens. Pushes if the app is closed. Called outside the hub lock.
func (h *hub) toAdmins(msg protocol.ClientMessage, notice *push.Notice) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, id := range h.store.Admins() {
		h.toClients(id, msg)
		if notice != nil && h.push != nil {
			h.pushTo(id, *notice)
		}
	}
}

// admitTo creates the account and adds it to that room.
func (s *Server) admitTo(name, roomID, roomOwner string) (ownerID, token string, err error) {
	ownerID, token, err = s.store.CreateOwner(name)
	if err != nil {
		return "", "", err
	}
	err = s.hub.locked(func() error {
		if err := s.store.AddRoomMember(roomID, ownerID); err != nil {
			return err
		}
		if fresh, _, err := s.store.Room(roomID); err == nil {
			s.hub.toRoom(roomOwner, fresh)
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}
	return ownerID, token, nil
}

func (s *Server) joinRoutes() {
	m := s.mux
	bad := func(w http.ResponseWriter, status int, msg string) {
		writeJSON(w, status, map[string]string{"error": msg})
	}

	// Person without an account: room invite code + name. Called without a token, so the code is the key
	m.HandleFunc("POST /pair/room", func(w http.ResponseWriter, r *http.Request) {
		if !pairGuard(r) {
			bad(w, http.StatusBadRequest, pairHeader+": 1 헤더가 필요하다")
			return
		}
		var body struct{ Code, Name string }
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body) != nil {
			bad(w, http.StatusBadRequest, "code 와 name 이 필요하다")
			return
		}
		name := strings.TrimSpace(body.Name)
		if name == "" || len([]rune(name)) > 40 {
			bad(w, http.StatusBadRequest, "name 은 1~40자")
			return
		}
		if len(s.joins.pending()) >= joinMaxPending {
			bad(w, http.StatusTooManyRequests, "승인을 기다리는 사람이 너무 많다. 잠시 뒤에 다시")
			return
		}
		pc, ok := takeCode(w, r, &s.roomInvites, body.Code, "room invite (no account)")
		if !ok {
			return
		}
		room, roomOwner, err := s.store.Room(pc.agentID)
		if err != nil || roomOwner != pc.owner {
			bad(w, http.StatusNotFound, "room not found")
			return
		}
		if s.store.IsAdmin(roomOwner) { // a code issued by an admin is permission by itself
			id, token, err := s.admitTo(name, room.ID, roomOwner)
			if err != nil {
				log.Printf("room invite join: %v", err)
				bad(w, http.StatusInternalServerError, "internal error")
				return
			}
			log.Printf("room invite join: user %.8s, %s", id, r.RemoteAddr)
			writeJSON(w, http.StatusOK, map[string]string{"status": "approved", "token": token, "ownerId": id, "roomId": room.ID})
			return
		}
		poll, hash := newPoll()
		req := &joinReq{
			JoinRequest: protocol.JoinRequest{ID: uuid.NewString(), Name: name, RoomID: room.ID, RoomName: room.Name, InviterName: s.store.OwnerName(roomOwner), CreatedAt: time.Now().UnixMilli()},
			roomOwner:   roomOwner, poll: hash, status: "pending", expires: time.Now().Add(joinTTL),
		}
		s.joins.mu.Lock()
		if s.joins.m == nil {
			s.joins.m = map[string]*joinReq{}
		}
		s.joins.m[req.ID] = req
		s.joins.mu.Unlock()
		log.Printf("join request: %.8s (room %.8s), %s", req.ID, room.ID, r.RemoteAddr)
		jr := req.JoinRequest
		s.hub.toAdmins(protocol.ClientMessage{Type: "join.request", Join: &jr},
			&push.Notice{Title: "곁", Body: name + " 님이 " + jr.InviterName + " 님의 방 「" + room.Name + "」에 들어오려 해요"})
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending", "requestId": req.ID, "poll": poll})
	})

	// Waiting person: asks for the result. Once approved, the key is handed over only once
	m.HandleFunc("GET /pair/room/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !pairGuard(r) {
			bad(w, http.StatusBadRequest, pairHeader+": 1 헤더가 필요하다")
			return
		}
		hash := sha256.Sum256([]byte(r.Header.Get("X-Kyeot-Poll")))
		s.joins.mu.Lock()
		defer s.joins.mu.Unlock()
		s.joins.sweep(time.Now())
		req := s.joins.m[r.PathValue("id")]
		if req == nil || subtle.ConstantTimeCompare(hash[:], req.poll[:]) != 1 {
			bad(w, http.StatusNotFound, "요청이 없거나 만료됐다")
			return
		}
		switch req.status {
		case "approved":
			delete(s.joins.m, req.ID)
			writeJSON(w, http.StatusOK, map[string]string{"status": "approved", "token": req.token, "ownerId": req.ownerID, "roomId": req.RoomID})
		case "rejected":
			delete(s.joins.m, req.ID)
			writeJSON(w, http.StatusOK, map[string]string{"status": "rejected"})
		default:
			writeJSON(w, http.StatusOK, map[string]string{"status": "pending"})
		}
	})

	m.HandleFunc("GET /api/joins", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if !s.store.IsAdmin(owner) {
			return reject(http.StatusForbidden, "only admins can see join requests")
		}
		writeJSON(w, http.StatusOK, s.joins.pending())
		return nil
	}))

	m.HandleFunc("POST /api/joins/{id}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if !s.store.IsAdmin(owner) {
			return reject(http.StatusForbidden, "only admins can answer join requests")
		}
		var b struct {
			Approve *bool `json:"approve"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&b) != nil || b.Approve == nil {
			return reject(http.StatusBadRequest, "approve 가 필요하다")
		}
		s.joins.mu.Lock()
		s.joins.sweep(time.Now())
		req := s.joins.m[r.PathValue("id")]
		if req == nil || req.status != "pending" {
			s.joins.mu.Unlock()
			return reject(http.StatusNotFound, "join request not found")
		}
		req.status = "answering" // only once even if two admins press at the same time
		s.joins.mu.Unlock()

		status := "rejected"
		var id, token string
		if *b.Approve {
			if _, roomOwner, err := s.store.Room(req.RoomID); err != nil || roomOwner != req.roomOwner {
				status = "rejected" // the room disappeared in the meantime
			} else if id, token, err = s.admitTo(req.Name, req.RoomID, req.roomOwner); err != nil {
				s.joins.mu.Lock()
				req.status = "pending"
				s.joins.mu.Unlock()
				return err
			} else {
				status = "approved"
			}
		}
		s.joins.mu.Lock()
		req.status, req.token, req.ownerID = status, token, id
		s.joins.mu.Unlock()
		log.Printf("join request %s: %.8s (by %.8s)", status, req.ID, owner)
		jr := req.JoinRequest
		s.hub.toAdmins(protocol.ClientMessage{Type: "join.done", Join: &jr}, nil)
		writeJSON(w, http.StatusOK, map[string]string{"status": status})
		return nil
	}))
}
