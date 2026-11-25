package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/kyeotapp/kyeot/internal/store"
)

func (s *Server) taskRoutes() {
	m := s.mux

	m.HandleFunc("GET /api/tasks", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		tasks, err := s.store.Tasks(owner, r.URL.Query().Get("agent"))
		if err == nil {
			writeJSON(w, http.StatusOK, tasks)
		}
		return err
	}))

	m.HandleFunc("POST /api/tasks", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		b, atts, err := s.readMessage(r, owner)
		if err != nil {
			return err
		}
		if b.AgentID == "" {
			return reject(http.StatusBadRequest, "agentId is required")
		}
		task, err := s.hub.StartTask(owner, b.AgentID, b.Text, b.Lang, atts)
		if err == nil {
			writeJSON(w, http.StatusCreated, task)
		}
		return err
	}))

	m.HandleFunc("DELETE /api/tasks/{id}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if err := s.hub.HideTask(owner, r.PathValue("id")); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	m.HandleFunc("PATCH /api/tasks/{id}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		var body struct {
			Title *string `json:"title"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(&body); err != nil || body.Title == nil {
			return reject(http.StatusBadRequest, "title is required")
		}
		task, err := s.hub.RenameTask(owner, r.PathValue("id"), *body.Title)
		if err == nil {
			writeJSON(w, http.StatusOK, task)
		}
		return err
	}))

	m.HandleFunc("GET /api/tasks/{id}/events", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		t, seen, err := s.store.CanSeeTask(r.PathValue("id"), owner)
		if errors.Is(err, store.ErrNotFound) || (err == nil && !seen) {
			return reject(http.StatusNotFound, "task not found")
		}
		if err != nil {
			return err
		}
		events, err := s.store.Events(t.ID)
		if err == nil {
			writeJSON(w, http.StatusOK, s.signEvents(events))
		}
		return err
	}))

	m.HandleFunc("POST /api/tasks/{id}/messages", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		b, atts, err := s.readMessage(r, owner)
		if err != nil {
			return err
		}
		taskOwner, by := owner, ""
		if t, err := s.store.Task(r.PathValue("id")); err == nil && t.OwnerID != owner && s.store.CanAskInTask(t.ID, owner) {
			taskOwner, by = t.OwnerID, owner // an agent member of a shared room continues the owner's task
		}
		if err := s.hub.SendMessage(taskOwner, r.PathValue("id"), b.Text, b.QuoteID, b.EditID, atts, by); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	m.HandleFunc("POST /api/tasks/{id}/reactions", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		b, err := readBody(r, "eventId", "emoji")
		if err != nil {
			return err
		}
		target, err := strconv.ParseInt(b["eventId"], 10, 64)
		if err != nil {
			return reject(http.StatusBadRequest, "eventId must be a number")
		}
		on, err := s.hub.React(owner, r.PathValue("id"), target, b["emoji"])
		if err == nil {
			writeJSON(w, http.StatusOK, map[string]bool{"on": on})
		}
		return err
	}))

	m.HandleFunc("POST /api/tasks/{id}/approvals/{approval}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		b, err := readBody(r, "choice")
		if err != nil {
			return err
		}
		if err := s.hub.AnswerApproval(owner, r.PathValue("id"), r.PathValue("approval"), b["choice"]); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	m.HandleFunc("POST /api/tasks/{id}/cancel", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if err := s.hub.CancelTask(owner, r.PathValue("id")); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	m.HandleFunc("GET /api/search", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if len([]rune(q)) < 1 || len(q) > 200 {
			return reject(http.StatusBadRequest, "q must be 1 to 200 bytes")
		}
		found, err := s.store.SearchEvents(owner, q, 50)
		if err == nil {
			writeJSON(w, http.StatusOK, s.signEvents(found))
		}
		return err
	}))

	m.HandleFunc("GET /api/approvals", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		events, err := s.store.PendingApprovals(owner)
		if err == nil {
			writeJSON(w, http.StatusOK, s.signEvents(events))
		}
		return err
	}))
}
