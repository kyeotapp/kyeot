package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

const (
	maxRoomRoutines = 20
	maxRoutineID    = 128
)

// routineIDs merges the new routineIds and the old routineId, dropping blanks and duplicates. Over the limit, it returns the truncated list with an error.
func routineIDs(list []string, legacy string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	var err error
	for _, id := range append(list, legacy) {
		id = strings.TrimSpace(id)
		switch {
		case id == "" || seen[id]:
			continue
		case len(id) > maxRoutineID:
			err = fmt.Errorf("routine id is too long")
			continue
		case len(out) == maxRoomRoutines:
			err = fmt.Errorf("at most %d routines", maxRoomRoutines)
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, err
}

// roomName cleans a room name: trims spaces and a leading # (added by the UI), collapses whitespace, max 40 chars. "" if empty.
func roomName(raw string) string {
	return clipName(strings.TrimLeft(strings.TrimSpace(raw), "#＃ "), 40)
}

// agentName cleans an agent name by the room-name rules and drops control and bidi characters. Max 32 chars (it goes into titles, lists, notification text). "" if empty.
func agentName(raw string) string {
	return clipName(strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Bidi_Control, r):
			return -1
		}
		return r
	}, raw), 32)
}

func clipName(s string, max int) string {
	name := strings.Join(strings.Fields(s), " ")
	if r := []rune(name); len(r) > max {
		name = strings.TrimSpace(string(r[:max]))
	}
	return name
}

func (s *Server) roomRoutes() {
	m := s.mux
	m.HandleFunc("POST /api/rooms", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		var b struct {
			Name       string   `json:"name"`
			AgentID    string   `json:"agentId"`
			RoutineID  string   `json:"routineId"`
			RoutineIDs []string `json:"routineIds"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(&b); err != nil {
			return reject(http.StatusBadRequest, "invalid json")
		}
		b.Name, b.AgentID = roomName(b.Name), strings.TrimSpace(b.AgentID)
		if b.Name == "" || b.AgentID == "" {
			return reject(http.StatusBadRequest, "name and agentId are required")
		}
		ids, err := routineIDs(b.RoutineIDs, b.RoutineID)
		if err != nil {
			return reject(http.StatusBadRequest, err.Error())
		}
		if !s.store.AgentOwnedBy(b.AgentID, owner) {
			return reject(http.StatusNotFound, "agent not found")
		}
		room, err := s.hub.saveRoom(owner, func() (protocol.Room, error) { return s.store.CreateRoom(owner, b.Name, b.AgentID, ids) })
		if err == nil {
			writeJSON(w, http.StatusCreated, room)
		}
		return err
	}))

	m.HandleFunc("PATCH /api/rooms/{id}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		room, err := s.ownedRoom(r.PathValue("id"), owner)
		if err != nil {
			return err
		}
		var b struct {
			Name   string `json:"name"`
			Filter *bool  `json:"filter"` // rooms.filter; sending only this keeps the name
		}
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<16)).Decode(&b); err != nil {
			return reject(http.StatusBadRequest, "invalid json")
		}
		if b.Filter != nil && b.Name == "" {
			room, err = s.hub.saveRoom(owner, func() (protocol.Room, error) { return s.store.SetRoomFilter(room.ID, *b.Filter) })
			if err == nil {
				writeJSON(w, http.StatusOK, room)
			}
			return err
		}
		name := roomName(b.Name)
		if name == "" {
			return reject(http.StatusBadRequest, "name is required")
		}
		room, err = s.hub.saveRoom(owner, func() (protocol.Room, error) {
			if b.Filter != nil {
				if _, err := s.store.SetRoomFilter(room.ID, *b.Filter); err != nil {
					return protocol.Room{}, err
				}
			}
			return s.store.RenameRoom(room.ID, name)
		})
		if err == nil {
			writeJSON(w, http.StatusOK, room)
		}
		return err
	}))

	// Routine ids are checked for format only: the routine list lives in the agent report and may lag, and the feed always collects only the room agent's tasks
	m.HandleFunc("PUT /api/rooms/{id}/routines", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		room, err := s.ownedRoom(r.PathValue("id"), owner)
		if err != nil {
			return err
		}
		var b struct {
			RoutineIDs *[]string `json:"routineIds"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(&b); err != nil || b.RoutineIDs == nil {
			return reject(http.StatusBadRequest, "routineIds is required")
		}
		ids, err := routineIDs(*b.RoutineIDs, "")
		if err != nil {
			return reject(http.StatusBadRequest, err.Error())
		}
		room, err = s.hub.saveRoom(owner, func() (protocol.Room, error) { return s.store.SetRoomRoutines(room.ID, ids) })
		if err == nil {
			writeJSON(w, http.StatusOK, room)
		}
		return err
	}))
}
