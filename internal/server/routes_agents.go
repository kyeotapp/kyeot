package server

import (
	"errors"
	"net/http"

	"github.com/kyeotapp/kyeot/internal/store"
)

func (s *Server) agentRoutes() {
	m := s.mux

	m.HandleFunc("GET /api/agents", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		agents, err := s.store.Agents(owner)
		if err == nil {
			writeJSON(w, http.StatusOK, agents)
		}
		return err
	}))

	m.HandleFunc("POST /api/agents", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		b, err := readBody(r, "name")
		if err != nil {
			return err
		}
		agent, token, err := s.hub.CreateAgent(owner, b["name"])
		if err == nil {
			writeJSON(w, http.StatusCreated, map[string]any{"agent": agent, "token": token})
		}
		return err
	}))

	m.HandleFunc("GET /api/agents/{id}/report", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if !s.store.AgentOwnedBy(r.PathValue("id"), owner) {
			return reject(http.StatusNotFound, "agent not found")
		}
		report, at := s.store.Report(r.PathValue("id"))
		writeJSON(w, http.StatusOK, map[string]any{"report": report, "at": at})
		return nil
	}))

	m.HandleFunc("PUT /api/agents/{id}/avatar", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		b, err := readBody(r, "avatar")
		if err != nil {
			return err
		}
		a, err := s.hub.SetAvatar(owner, r.PathValue("id"), b["avatar"])
		if err == nil {
			writeJSON(w, http.StatusOK, a)
		}
		return err
	}))

	m.HandleFunc("DELETE /api/agents/{id}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if err := s.hub.UnlinkAgent(owner, r.PathValue("id")); err != nil {
			return err
		}
		s.pairs.drop(r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	m.HandleFunc("POST /api/agents/{id}/token", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		token, err := s.hub.RotateAgentToken(owner, r.PathValue("id"))
		if errors.Is(err, store.ErrNotFound) {
			return reject(http.StatusNotFound, "agent not found")
		}
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]string{"token": token})
		return nil
	}))
}
