package server

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"time"

	"github.com/kyeotapp/kyeot/internal/push"
	"github.com/kyeotapp/kyeot/internal/store"
)

const pushBodyRunes = 120

// notify pushes only to people whose app is closed. With members, also to members of shared rooms holding that task as a post (routine results only). Called under the hub lock.
func (h *hub) notify(a store.AgentAuth, taskID, body string, members bool) {
	if h.push == nil {
		return
	}
	title := a.ID
	if ag, err := h.store.Agent(a.ID); err == nil {
		title = cmp.Or(ag.Name, title)
	}
	if r := []rune(body); len(r) > pushBodyRunes {
		body = string(r[:pushBodyRunes])
	}
	n := push.Notice{Title: title, Body: cmp.Or(body, "답이 도착했어요"), TaskID: taskID, AgentID: a.ID}
	to := []string{a.OwnerID}
	if members {
		to = append(to, h.audience(a.OwnerID, taskID)...)
	}
	for _, owner := range to {
		h.pushTo(owner, n)
	}
}

func (h *hub) pushTo(owner string, n push.Notice) {
	n.ServerID = h.store.ServerID()
	for _, o := range h.clients {
		if o == owner {
			return // the app is open
		}
	}
	devices, err := h.store.Devices(owner)
	if err != nil || len(devices) == 0 {
		return
	}
	h.pushes.Add(1)
	go func() {
		defer h.pushes.Done()
		for _, d := range devices {
			if d.Platform != "ios" {
				continue
			}
			ctx, cancel := context.WithTimeout(h.pushCtx, 20*time.Second)
			err := h.push.Send(ctx, push.Device{Token: d.Token, Env: d.Env}, n)
			cancel()
			switch {
			case err == nil:
			case errors.Is(err, push.ErrBadToken):
				log.Printf("push %s…: invalid, removing (%v)", d.Token[:8], err)
				if err := h.store.DeleteDevice(owner, d.Token); err != nil {
					log.Printf("push %s…: remove: %v", d.Token[:8], err)
				}
			default:
				log.Printf("push %s…: %v", d.Token[:8], err)
			}
		}
	}()
}

var deviceToken = regexp.MustCompile(`^[0-9a-fA-F]{32,200}$`)

func (s *Server) deviceRoutes() {
	s.mux.HandleFunc("POST /api/devices", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		var b struct{ Token, Platform, Env string }
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<10)).Decode(&b); err != nil {
			return reject(http.StatusBadRequest, "invalid json")
		}
		if !deviceToken.MatchString(b.Token) {
			return reject(http.StatusBadRequest, "token must be 32-200 hex characters")
		}
		if b.Platform != "ios" {
			return reject(http.StatusBadRequest, "platform must be ios")
		}
		if b.Env != push.EnvSandbox && b.Env != push.EnvProduction {
			return reject(http.StatusBadRequest, "env must be sandbox or production")
		}
		err := s.store.SaveDevice(owner, store.Device{Token: b.Token, Platform: b.Platform, Env: b.Env})
		if errors.Is(err, store.ErrDeviceTaken) {
			return reject(http.StatusConflict, "this device is registered to another account")
		}
		if err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	s.mux.HandleFunc("DELETE /api/devices/{token}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		token := r.PathValue("token")
		if !deviceToken.MatchString(token) {
			return reject(http.StatusBadRequest, "token must be 32-200 hex characters")
		}
		if err := s.store.DeleteDevice(owner, token); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))
}
