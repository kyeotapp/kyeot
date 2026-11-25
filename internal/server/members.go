package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/store"
)

// User invites and room sharing. Design: docs/plans/2026-11-15-shared-rooms.md.
// Members are read-only. Write paths go through ownedRoom/ownedTask, which admit only the owner.

const ownerInviteKey = "owner-invite" // only one invite code is live at a time

// visibleRoom returns the room and its owner if the caller is the owner or a member.
func (s *Server) visibleRoom(id, owner string) (protocol.Room, string, error) {
	room, roomOwner, err := s.store.Room(id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && roomOwner != owner && !s.store.IsRoomMember(id, owner)) {
		return room, "", reject(http.StatusNotFound, "room not found")
	}
	if room.Shared = err == nil && roomOwner != owner; room.Shared {
		room.OwnerName, room.CanAsk = s.store.OwnerName(roomOwner), s.store.MemberRole(id, owner) == store.RoleAgent
	}
	return room, roomOwner, err
}

type roomWithMembers struct {
	room    protocol.Room
	members []store.Member
}

// ownedRoomMembers returns the rooms that person owns, with their members.
func ownedRoomMembers(st *store.Store, owner string) ([]roomWithMembers, error) {
	rooms, err := st.Rooms(owner)
	if err != nil {
		return nil, err
	}
	var out []roomWithMembers
	for _, r := range rooms {
		if r.Shared {
			continue
		}
		members, err := st.RoomMembers(r.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, roomWithMembers{r, members})
	}
	return out, nil
}

// takeCode spends a 6-digit code once. The wrong-guess limit matches pairing. On failure the response is already written.
func takeCode(w http.ResponseWriter, r *http.Request, codes *pairStore, code, what string) (pairCode, bool) {
	if len(code) != 6 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "6자리 코드가 필요하다"})
		return pairCode{}, false
	}
	pc, ok, blocked := codes.take(code, pairKey(r.RemoteAddr))
	if blocked {
		log.Printf("%s blocked: %s (too many wrong codes)", what, r.RemoteAddr)
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "코드를 너무 많이 틀렸다. 잠시 뒤에 다시"})
		return pairCode{}, false
	}
	if !ok {
		log.Printf("%s failed: %s", what, r.RemoteAddr)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "코드가 틀렸거나 만료됐다. 새 코드를 받는다"})
		return pairCode{}, false
	}
	return pc, true
}

// role is owner, admin, or empty.
func (s *Server) role(id string) string {
	switch {
	case id == s.store.OwnerID():
		return "owner"
	case s.store.IsAdmin(id):
		return "admin"
	}
	return ""
}

// me is the body shared by GET and PATCH /api/me.
func (s *Server) me(owner string) map[string]any {
	role := s.role(owner)
	return map[string]any{"ownerId": owner, "name": s.store.OwnerName(owner), "avatar": s.store.OwnerAvatar(owner), "canInvite": role != "", "role": role,
		"termsVersion": TermsVersion, "termsAgreed": s.store.TermsVersion(owner) == TermsVersion}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (s *Server) memberRoutes() {
	m := s.mux

	// me: the app knows its own id and hides "me" in the member list; the name shows in the list
	// canInvite: owner and admins invite people. role: owner, admin, empty
	m.HandleFunc("GET /api/me", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		me := s.me(owner)
		if from, ok := s.store.HandoverNotice(owner); ok {
			me["handoverFrom"], me["host"] = from, hostName()
		}
		writeJSON(w, http.StatusOK, me)
		return nil
	}))
	m.HandleFunc("PATCH /api/me", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		var b struct{ Name, Avatar *string }
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 210_000)).Decode(&b) != nil {
			return reject(http.StatusBadRequest, "invalid json")
		}
		var name string
		if b.Name != nil || b.Avatar == nil {
			if name = strings.TrimSpace(deref(b.Name)); name == "" || len([]rune(name)) > 40 {
				return reject(http.StatusBadRequest, "name 은 1~40자")
			}
		}
		if b.Avatar != nil && !store.PersonAvatarOK(*b.Avatar) {
			return reject(http.StatusBadRequest, "avatar must be empty, color:<name> or a jpeg/png data URL under 200KB")
		}
		if name != "" {
			if err := s.store.RenameOwner(owner, name); err != nil {
				return err
			}
		}
		if b.Avatar != nil {
			if err := s.store.SetOwnerAvatar(owner, *b.Avatar); err != nil {
				return err
			}
		}
		writeJSON(w, http.StatusOK, s.me(owner))
		return nil
	}))

	// an invited user deletes their own account; posts in others' rooms stay as a departed user's
	m.HandleFunc("DELETE /api/me", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if owner == s.store.OwnerID() {
			return reject(http.StatusForbidden, "서버 주인 계정은 앱에서 지울 수 없어요. 서버 컴퓨터에서 kyeot uninstall --purge 로 지워 주세요")
		}
		d, err := s.hub.DeleteOwner(owner)
		if errors.Is(err, store.ErrNotFound) {
			return reject(http.StatusNotFound, "user not found")
		}
		if err != nil {
			return err
		}
		s.removeUploadFiles("account delete", d.Uploads)
		s.dropOwnerCodes(owner)
		log.Printf("account deleted: %.8s, agents %d, uploads %d", owner, len(d.Agents), len(d.Uploads))
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	// invite from the owner or an admin that grants an account without a room
	m.HandleFunc("POST /api/owners/invite", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if !s.store.IsAdmin(owner) {
			return reject(http.StatusForbidden, "only admins can invite")
		}
		code, exp := s.ownerInvites.issue(owner, ownerInviteKey)
		writeJSON(w, http.StatusOK, map[string]any{"code": code, "expiresAt": exp.UnixMilli()})
		return nil
	}))

	// closing the window discards the code
	m.HandleFunc("DELETE /api/owners/invite", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if !s.store.IsAdmin(owner) {
			return reject(http.StatusForbidden, "only admins can invite")
		}
		s.ownerInvites.drop(ownerInviteKey)
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	// owner and admins list users and revoke them; revoking cuts access but keeps the records
	m.HandleFunc("GET /api/owners", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if !s.store.IsAdmin(owner) {
			return reject(http.StatusForbidden, "only admins can list users")
		}
		list, err := s.store.Invitees()
		if err == nil {
			writeJSON(w, http.StatusOK, list)
		}
		return err
	}))
	m.HandleFunc("DELETE /api/owners/{id}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if !s.store.IsAdmin(owner) {
			return reject(http.StatusForbidden, "only admins can revoke users")
		}
		id := r.PathValue("id")
		if id == s.store.OwnerID() {
			return reject(http.StatusBadRequest, "the server owner cannot be revoked")
		}
		if owner != s.store.OwnerID() && s.store.IsAdmin(id) {
			return reject(http.StatusForbidden, "only the server owner can revoke admins")
		}
		agents, err := s.store.Agents(id)
		if err != nil {
			return err
		}
		owned, err := ownedRoomMembers(s.store, id)
		if err != nil {
			return err
		}
		err = s.hub.locked(func() error {
			rooms, err := s.store.RevokeOwner(id)
			if err != nil {
				return err
			}
			for _, o := range owned {
				o.room.Shared = true
				for _, m := range o.members {
					s.hub.toClients(m.OwnerID, protocol.ClientMessage{Type: "room.removed", Room: &o.room})
				}
			}
			for _, roomID := range rooms {
				if room, roomOwner, err := s.store.Room(roomID); err == nil {
					s.hub.toRoom(roomOwner, room) // participant list for the remaining people
				}
			}
			return nil
		})
		if errors.Is(err, store.ErrNotFound) {
			return reject(http.StatusNotFound, "user not found")
		}
		if err != nil {
			return err
		}
		ids := make([]string, len(agents))
		for i, a := range agents {
			ids[i] = a.ID
		}
		s.hub.closeOwner(id, ids)
		s.pairs.dropOwner(id)
		s.roomInvites.dropOwner(id)
		for _, jr := range s.joins.dropOwner(id) {
			s.hub.toAdmins(protocol.ClientMessage{Type: "join.done", Join: &jr}, nil)
		}
		log.Printf("user revoked: %.8s", id)
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	// only the owner grants or removes admin; the owner cannot be changed
	m.HandleFunc("PATCH /api/owners/{id}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if owner != s.store.OwnerID() {
			return reject(http.StatusForbidden, "only the server owner can change admins")
		}
		var b struct {
			Admin *bool `json:"admin"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&b) != nil || b.Admin == nil {
			return reject(http.StatusBadRequest, "admin 이 필요하다")
		}
		id := r.PathValue("id")
		if id == s.store.OwnerID() {
			return reject(http.StatusBadRequest, "the server owner is always an admin")
		}
		if err := s.store.SetAdmin(id, *b.Admin); errors.Is(err, store.ErrNotFound) {
			return reject(http.StatusNotFound, "user not found")
		} else if err != nil {
			return err
		}
		log.Printf("admin %v: %.8s", *b.Admin, id)
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	// new phone: exchanges an invite code for an owner token; called without a token, so the code is the key
	m.HandleFunc("POST /pair/owner", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Code  string `json:"code"`
			Name  string `json:"name"`
			Token string `json:"token"` // my token from this server; if valid, the same account is kept (token unchanged) instead of a new one
		}
		if !pairGuard(r) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": pairHeader + ": 1 헤더가 필요하다"})
			return
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&body) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "code 와 name 이 필요하다"})
			return
		}
		name := strings.TrimSpace(body.Name)
		if name == "" || len([]rune(name)) > 40 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name 은 1~40자"})
			return
		}
		if _, ok := takeCode(w, r, &s.ownerInvites, body.Code, "invite"); !ok {
			return
		}
		if body.Token != "" { // same person returning: no new account; the account's name is kept
			id, ok, err := s.store.RejoinOwner(body.Token)
			if err != nil {
				log.Printf("invite (rejoin): %v", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
				return
			}
			if ok {
				log.Printf("invite ok (same account): user %.8s, %s", id, r.RemoteAddr)
				writeJSON(w, http.StatusOK, map[string]any{"token": body.Token, "ownerId": id, "rejoined": true})
				return
			}
		}
		id, token, err := s.store.CreateOwner(name)
		if err != nil {
			log.Printf("invite: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		log.Printf("invite ok: user %.8s, %s", id, r.RemoteAddr)
		writeJSON(w, http.StatusOK, map[string]string{"token": token, "ownerId": id})
	})
	s.roomMemberRoutes()
}

// roomMemberRoutes are the room share code, join, and room member routes.
func (s *Server) roomMemberRoutes() {
	m := s.mux

	// room owner: share code
	m.HandleFunc("POST /api/rooms/{id}/invite", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		room, err := s.ownedRoom(r.PathValue("id"), owner)
		if err != nil {
			return err
		}
		code, exp := s.roomInvites.issue(owner, room.ID)
		writeJSON(w, http.StatusOK, map[string]any{"code": code, "expiresAt": exp.UnixMilli()})
		return nil
	}))

	m.HandleFunc("DELETE /api/rooms/{id}/invite", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		room, err := s.ownedRoom(r.PathValue("id"), owner)
		if err != nil {
			return err
		}
		s.roomInvites.drop(room.ID)
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	// other users: join a room with a share code
	m.HandleFunc("POST /api/rooms/join", func(w http.ResponseWriter, r *http.Request) {
		owner, ok := s.ownerOf(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		var body struct {
			Code string `json:"code"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "code 가 필요하다"})
			return
		}
		pc, ok := takeCode(w, r, &s.roomInvites, body.Code, "room share")
		if !ok {
			return
		}
		if pc.owner == owner {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "내 방이다"})
			return
		}
		room, roomOwner, err := s.store.Room(pc.agentID)
		if err != nil || roomOwner != pc.owner {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "room not found"})
			return
		}
		if err := s.hub.locked(func() error {
			if err := s.store.AddRoomMember(room.ID, owner); err != nil {
				return err
			}
			if fresh, _, err := s.store.Room(room.ID); err == nil {
				s.hub.toRoom(roomOwner, fresh) // the participant list changes at once for owner and all members (including the joiner)
			}
			return nil
		}); err != nil {
			log.Printf("room share: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		room.Shared, room.OwnerName = true, s.store.OwnerName(roomOwner)
		writeJSON(w, http.StatusOK, room)
	})

	s.joinRoutes()

	m.HandleFunc("GET /api/rooms/{id}/members", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		room, _, err := s.visibleRoom(r.PathValue("id"), owner)
		if err != nil {
			return err
		}
		members, err := s.store.RoomMembers(room.ID)
		if err == nil {
			writeJSON(w, http.StatusOK, members)
		}
		return err
	}))

	// room owner and members with their pictures; the owner's picture is not put on the websocket room (the photo would go out with every message)
	m.HandleFunc("GET /api/rooms/{id}/people", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		room, roomOwner, err := s.visibleRoom(r.PathValue("id"), owner)
		if err != nil {
			return err
		}
		members, err := s.store.RoomPeople(room.ID)
		if err == nil {
			host := store.Member{OwnerID: roomOwner, Name: s.store.OwnerName(roomOwner), Avatar: s.store.OwnerAvatar(roomOwner)}
			writeJSON(w, http.StatusOK, map[string]any{"owner": host, "members": members})
		}
		return err
	}))

	// the owner grants each member permission to ask the agent (member, agent), per room
	m.HandleFunc("PATCH /api/rooms/{id}/members/{member}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		room, err := s.ownedRoom(r.PathValue("id"), owner)
		if err != nil {
			return err
		}
		var b struct{ Role string }
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&b) != nil || (b.Role != store.RoleMember && b.Role != store.RoleAgent) {
			return reject(http.StatusBadRequest, "role 은 member 또는 agent")
		}
		member := r.PathValue("member")
		if err := s.hub.locked(func() error {
			ok, err := s.store.SetMemberRole(room.ID, member, b.Role)
			if err != nil {
				return err
			}
			if !ok {
				return reject(http.StatusNotFound, "member not found")
			}
			room.Shared, room.OwnerName, room.CanAsk = true, s.store.OwnerName(owner), b.Role == store.RoleAgent
			s.hub.toClients(member, protocol.ClientMessage{Type: "room", Room: &room}) // that member's input box changes at once
			return nil
		}); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, store.Member{OwnerID: member, Name: s.store.OwnerName(member), Role: b.Role, Avatar: s.store.OwnerAvatar(member)})
		return nil
	}))

	// the owner can remove anyone; a member can only leave themselves
	m.HandleFunc("DELETE /api/rooms/{id}/members/{member}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		room, roomOwner, err := s.visibleRoom(r.PathValue("id"), owner)
		if err != nil {
			return err
		}
		member := r.PathValue("member")
		if owner != roomOwner && member != owner {
			return reject(http.StatusForbidden, "only the room owner can remove others")
		}
		if member == roomOwner {
			return reject(http.StatusBadRequest, "the room owner is not a member")
		}
		if err := s.removeMember(room, roomOwner, member); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))
}
