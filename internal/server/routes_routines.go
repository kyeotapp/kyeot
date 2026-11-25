package server

import (
	"cmp"
	"encoding/json"
	"net/http"
	"strconv"
)

func (s *Server) routineRoutes() {
	m := s.mux

	// For picking routines to bind to a room: the report's routines plus those of agents that send only routine.run and no report
	m.HandleFunc("GET /api/agents/{id}/routines", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		id := r.PathValue("id")
		if !s.store.AgentOwnedBy(id, owner) {
			return reject(http.StatusNotFound, "agent not found")
		}
		type item struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			Reported bool   `json:"reported"` // in the report (otherwise known only from results)
		}
		out := []item{}
		seen := map[string]bool{}
		if raw, _ := s.store.Report(id); raw != nil {
			var rep struct {
				Routines []struct{ ID, Name, Title string } `json:"routines"`
			}
			if json.Unmarshal(raw, &rep) == nil {
				for _, x := range rep.Routines {
					out = append(out, item{x.ID, cmp.Or(x.Title, x.Name, x.ID), true})
					seen[x.ID] = true
				}
			}
		}
		learned, err := s.store.LearnedRoutines(id)
		if err != nil {
			return err
		}
		for _, x := range learned {
			if !seen[x.ID] {
				out = append(out, item{x.ID, x.Name, false})
			}
		}
		writeJSON(w, http.StatusOK, out)
		return nil
	}))

	m.HandleFunc("GET /api/agents/{id}/routines/{routine}/history", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if !s.store.AgentOwnedBy(r.PathValue("id"), owner) {
			return reject(http.StatusNotFound, "agent not found")
		}
		runs, err := s.store.RoutineHistory(r.PathValue("id"), r.PathValue("routine"))
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, runs)
		return nil
	}))

	m.HandleFunc("POST /api/agents/{id}/routines", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		b, err := readBody(r, "name", "schedule", "prompt")
		if err != nil {
			return err
		}
		if err := s.hub.CreateRoutine(owner, r.PathValue("id"), b["name"], b["schedule"], b["prompt"]); err != nil {
			return err
		}
		w.WriteHeader(http.StatusAccepted)
		return nil
	}))

	m.HandleFunc("DELETE /api/agents/{id}/routines/{routine}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if err := s.hub.RemoveRoutine(owner, r.PathValue("id"), r.PathValue("routine")); err != nil {
			return err
		}
		w.WriteHeader(http.StatusAccepted)
		return nil
	}))

	m.HandleFunc("POST /api/agents/{id}/routines/{routine}/toggle", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		b, err := readBody(r, "on")
		if err != nil {
			return err
		}
		on, err := strconv.ParseBool(b["on"])
		if err != nil {
			return reject(http.StatusBadRequest, "on must be true or false")
		}
		if err := s.hub.ToggleRoutine(owner, r.PathValue("id"), r.PathValue("routine"), on); err != nil {
			return err
		}
		w.WriteHeader(http.StatusAccepted)
		return nil
	}))

	m.HandleFunc("POST /api/agents/{id}/routines/{routine}/run", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if err := s.hub.RunRoutine(owner, r.PathValue("id"), r.PathValue("routine")); err != nil {
			return err
		}
		w.WriteHeader(http.StatusAccepted)
		return nil
	}))

	m.HandleFunc("POST /api/agents/{id}/data/{name}/refresh", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		row, err := s.hub.RefreshData(r.Context(), owner, r.PathValue("id"), r.PathValue("name"))
		if err != nil {
			return err
		}
		if row != nil {
			writeJSON(w, http.StatusOK, row)
			return nil
		}
		w.WriteHeader(http.StatusAccepted)
		return nil
	}))
}
