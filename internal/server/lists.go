package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"unicode/utf8"

	"github.com/kyeotapp/kyeot/internal/store"
)

const maxListGroupName = 60

// Pins and groups for the conversation and room lists. Stored per viewer; unrelated to agents.
func (s *Server) listRoutes() {
	m := s.mux

	m.HandleFunc("GET /api/lists/{kind}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		kind, err := listKind(r)
		if err != nil {
			return err
		}
		p, err := s.store.ListPrefs(owner, kind)
		if err == nil {
			writeJSON(w, http.StatusOK, p)
		}
		return err
	}))

	m.HandleFunc("PATCH /api/lists/{kind}/items/{id}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		kind, err := listKind(r)
		if err != nil {
			return err
		}
		var body struct {
			Pinned  *bool   `json:"pinned"`
			GroupID *string `json:"groupId"`
		}
		if err := decodeList(r, &body); err != nil || (body.Pinned == nil && body.GroupID == nil) {
			return reject(http.StatusBadRequest, "pinned or groupId is required")
		}
		id := r.PathValue("id")
		if err := s.listTarget(owner, kind, id); err != nil {
			return err
		}
		if body.GroupID != nil {
			if err := s.store.SetItemGroup(owner, kind, id, *body.GroupID); errors.Is(err, store.ErrNotFound) {
				return reject(http.StatusNotFound, "group not found")
			} else if err != nil {
				return err
			}
		}
		if body.Pinned != nil {
			if err := s.store.SetPinned(owner, kind, id, *body.Pinned); err != nil {
				return err
			}
		}
		return s.writeListPrefs(w, owner, kind)
	}))

	m.HandleFunc("PUT /api/lists/{kind}/pins", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		kind, err := listKind(r)
		if err != nil {
			return err
		}
		ids, err := readIDs(r)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := s.listTarget(owner, kind, id); err != nil {
				return err
			}
		}
		if err := s.store.SetPinOrder(owner, kind, ids); err != nil {
			return err
		}
		return s.writeListPrefs(w, owner, kind)
	}))

	m.HandleFunc("POST /api/lists/{kind}/groups", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		kind, err := listKind(r)
		if err != nil {
			return err
		}
		name, err := groupName(r)
		if err != nil {
			return err
		}
		g, err := s.store.CreateListGroup(owner, kind, name)
		if err == nil {
			writeJSON(w, http.StatusCreated, g)
		}
		return err
	}))

	m.HandleFunc("PUT /api/lists/{kind}/groups", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		kind, err := listKind(r)
		if err != nil {
			return err
		}
		ids, err := readIDs(r)
		if err != nil {
			return err
		}
		switch err := s.store.SetGroupOrder(owner, kind, ids); {
		case errors.Is(err, store.ErrNotFound):
			return reject(http.StatusNotFound, "group not found")
		case errors.Is(err, store.ErrBadList):
			return reject(http.StatusBadRequest, "ids must list every group once")
		case err != nil:
			return err
		}
		return s.writeListPrefs(w, owner, kind)
	}))

	m.HandleFunc("PUT /api/lists/{kind}/groups/{gid}/items", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		kind, err := listKind(r)
		if err != nil {
			return err
		}
		ids, err := readIDs(r)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := s.listTarget(owner, kind, id); err != nil {
				return err
			}
		}
		if err := s.store.SetGroupItems(owner, kind, r.PathValue("gid"), ids); errors.Is(err, store.ErrNotFound) {
			return reject(http.StatusNotFound, "group not found")
		} else if err != nil {
			return err
		}
		return s.writeListPrefs(w, owner, kind)
	}))

	m.HandleFunc("PATCH /api/lists/{kind}/groups/{gid}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		kind, err := listKind(r)
		if err != nil {
			return err
		}
		name, err := groupName(r)
		if err != nil {
			return err
		}
		g, err := s.store.RenameListGroup(owner, kind, r.PathValue("gid"), name)
		if errors.Is(err, store.ErrNotFound) {
			return reject(http.StatusNotFound, "group not found")
		}
		if err == nil {
			writeJSON(w, http.StatusOK, g)
		}
		return err
	}))

	m.HandleFunc("DELETE /api/lists/{kind}/groups/{gid}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		kind, err := listKind(r)
		if err != nil {
			return err
		}
		if err := s.store.DeleteListGroup(owner, kind, r.PathValue("gid")); errors.Is(err, store.ErrNotFound) {
			return reject(http.StatusNotFound, "group not found")
		} else if err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))
}

// listKind maps the tasks|rooms path segment to a stored kind.
func listKind(r *http.Request) (string, error) {
	switch r.PathValue("kind") {
	case "tasks":
		return store.ListTasks, nil
	case "rooms":
		return store.ListRooms, nil
	}
	return "", reject(http.StatusNotFound, "unknown list")
}

// listTarget returns 404 unless the conversation or room is visible in that person's list.
func (s *Server) listTarget(owner, kind, id string) error {
	if kind == store.ListTasks {
		if !s.store.OwnsVisibleTask(owner, id) {
			return reject(http.StatusNotFound, "task not found")
		}
		return nil
	}
	_, _, err := s.visibleRoom(id, owner)
	return err
}

func (s *Server) writeListPrefs(w http.ResponseWriter, owner, kind string) error {
	p, err := s.store.ListPrefs(owner, kind)
	if err == nil {
		writeJSON(w, http.StatusOK, p)
	}
	return err
}

func decodeList(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}

func readIDs(r *http.Request) ([]string, error) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := decodeList(r, &body); err != nil || body.IDs == nil {
		return nil, reject(http.StatusBadRequest, "ids is required")
	}
	seen := map[string]bool{}
	for _, id := range body.IDs {
		if id == "" || seen[id] {
			return nil, reject(http.StatusBadRequest, "ids must be distinct")
		}
		seen[id] = true
	}
	return body.IDs, nil
}

func groupName(r *http.Request) (string, error) {
	b, err := readBody(r, "name")
	if err != nil {
		return "", err
	}
	if utf8.RuneCountInString(b["name"]) > maxListGroupName {
		return "", reject(http.StatusBadRequest, "name is too long")
	}
	return b["name"], nil
}
