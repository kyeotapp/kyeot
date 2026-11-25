package server

import (
	"net/http"
	"strconv"

	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/store"
)

func (s *Server) roomListRoutes() {
	m := s.mux

	m.HandleFunc("GET /api/rooms", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		rooms, err := s.store.Rooms(owner)
		if err == nil {
			writeJSON(w, http.StatusOK, rooms)
		}
		return err
	}))

	m.HandleFunc("DELETE /api/rooms/{id}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		room, err := s.ownedRoom(r.PathValue("id"), owner)
		if err != nil {
			return err
		}
		var files []string
		err = s.hub.locked(func() error {
			members, _ := s.store.RoomMembers(room.ID)
			var err error
			if files, err = s.store.DeleteRoom(room.ID); err != nil {
				return err
			}
			s.hub.toClients(owner, protocol.ClientMessage{Type: "room.removed", Room: &room})
			for _, m := range members {
				s.hub.toClients(m.OwnerID, protocol.ClientMessage{Type: "room.removed", Room: &room})
			}
			return nil
		})
		if err != nil {
			return err
		}
		s.removeUploadFiles("room delete", files)
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	m.HandleFunc("GET /api/rooms/{id}/posts", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		room, roomOwner, err := s.visibleRoom(r.PathValue("id"), owner)
		if err != nil {
			return err
		}
		posts, err := s.store.RoomPosts(room, roomOwner, 200)
		if err == nil {
			writeJSON(w, http.StatusOK, posts)
		}
		return err
	}))

	m.HandleFunc("POST /api/rooms/{id}/messages", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		room, roomOwner, err := s.visibleRoom(r.PathValue("id"), owner)
		if err != nil {
			return err
		}
		b, atts, err := s.readMessage(r, owner)
		if err != nil {
			return err
		}
		ask := owner == roomOwner // older apps don't send ask; if owner, go to the agent as before
		if b.Ask != nil {
			ask = *b.Ask
		}
		if !(ask && owner == roomOwner) { // the owner asking their own agent still works for older apps
			if err := s.needTerms(room.ID, roomOwner, owner); err != nil {
				return err
			}
		}
		if !ask {
			m, err := s.hub.PostRoomMessage(roomOwner, room.ID, owner, b.Text, b.QuoteID, atts)
			if err == nil {
				writeJSON(w, http.StatusCreated, m)
			}
			return err
		}
		by := ""
		if owner != roomOwner {
			if s.store.MemberRole(room.ID, owner) != store.RoleAgent {
				return reject(http.StatusForbidden, "only the room owner or agent members can ask the agent")
			}
			by = owner
		}
		task, err := s.hub.StartTaskIn(roomOwner, room.AgentID, room.ID, b.Text, b.Lang, atts, by)
		if err == nil {
			writeJSON(w, http.StatusCreated, task)
		}
		return err
	}))

	m.HandleFunc("GET /api/rooms/{id}/messages", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		room, _, err := s.visibleRoom(r.PathValue("id"), owner)
		if err != nil {
			return err
		}
		msgs, err := s.store.RoomMessages(room.ID, 200, owner)
		if err == nil {
			for i := range msgs {
				s.hub.signRoomMessage(&msgs[i])
			}
			writeJSON(w, http.StatusOK, msgs)
		}
		return err
	}))

	// Edit a room people message: only the author's last message in that room
	m.HandleFunc("PATCH /api/rooms/{id}/messages/{mid}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		room, roomOwner, err := s.visibleRoom(r.PathValue("id"), owner)
		if err != nil {
			return err
		}
		mid, err := strconv.ParseInt(r.PathValue("mid"), 10, 64)
		if err != nil {
			return reject(http.StatusNotFound, "message not found in this room")
		}
		if err := s.needTerms(room.ID, roomOwner, owner); err != nil {
			return err
		}
		b, err := readBody(r, "text")
		if err != nil {
			return err
		}
		msg, err := s.hub.EditRoomMessage(roomOwner, room.ID, owner, mid, b["text"])
		if err == nil {
			writeJSON(w, http.StatusOK, msg)
		}
		return err
	}))

	m.HandleFunc("POST /api/rooms/{id}/messages/{mid}/reactions", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		room, roomOwner, err := s.visibleRoom(r.PathValue("id"), owner)
		if err != nil {
			return err
		}
		mid, err := strconv.ParseInt(r.PathValue("mid"), 10, 64)
		if err != nil {
			return reject(http.StatusNotFound, "message not found in this room")
		}
		b, err := readBody(r, "emoji")
		if err != nil {
			return err
		}
		on, err := s.hub.ReactRoomMessage(roomOwner, room.ID, owner, mid, b["emoji"])
		if err == nil {
			writeJSON(w, http.StatusOK, map[string]bool{"on": on})
		}
		return err
	}))
}
