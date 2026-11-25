package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kyeotapp/kyeot/internal/linkpreview"
	"github.com/kyeotapp/kyeot/internal/store"
)

const (
	linkPreviewOK        = 7 * 24 * time.Hour
	linkPreviewFail      = time.Hour
	linkPreviewImageKeep = 30 * 24 * time.Hour
	linkPreviewParallel  = 4
	linkPreviewDirMax    = 512 << 20 // total size of the image folder. Above it, text only
	linkPreviewPerMinute = 30        // per person, counts only addresses not in the cache

	reasonRateLimited = "rate_limited"
	reasonNotCached   = "not_cached"
)

// linkPreview is the response shape. Storage uses it too, but image and favicon hold only hashes and are turned into signed addresses on the way out.
type linkPreview struct {
	OK          bool   `json:"ok"`
	Reason      string `json:"reason,omitempty"`
	URL         string `json:"url,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Site        string `json:"site,omitempty"`
	Author      string `json:"author,omitempty"`
	Image       string `json:"image,omitempty"`
	Favicon     string `json:"favicon,omitempty"`
	At          int64  `json:"at,omitempty"`
}

// linkPreviews: 4 at a time server-wide; a request for the same address waits for the one that started first.
type linkPreviews struct {
	fetcher  *linkpreview.Fetcher
	slots    chan struct{}
	mu       sync.Mutex
	flight   map[string]*previewCall
	rate     map[string]*rateWindow
	perMin   int
	maxBytes int64
}

type previewCall struct {
	done    chan struct{}
	res     linkPreview
	aborted bool // a request that came first was cut off while waiting for a slot. The waiting side starts over
}

type rateWindow struct {
	start time.Time
	n     int
}

// allowLocked opens up to perMin fetches per person in a 1-minute window. Called holding lp.mu.
func (lp *linkPreviews) allowLocked(owner string, now time.Time) bool {
	w := lp.rate[owner]
	if w == nil || now.Sub(w.start) >= time.Minute {
		for k, v := range lp.rate {
			if now.Sub(v.start) >= time.Minute {
				delete(lp.rate, k)
			}
		}
		w = &rateWindow{start: now}
		lp.rate[owner] = w
	}
	if w.n >= lp.perMin {
		return false
	}
	w.n++
	return true
}

var imageHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (s *Server) linkPreviewDir() string {
	if s.opts.LinkPreviewDir != "" {
		return s.opts.LinkPreviewDir
	}
	if p := s.opts.DBPath; p == ":memory:" || strings.Contains(p, "mode=memory") {
		return filepath.Join(s.uploadDir(), "link-previews")
	}
	return filepath.Join(filepath.Dir(s.opts.DBPath), "link-previews")
}

func (s *Server) linkPreviewRoutes() {
	f := s.opts.LinkPreviewFetcher
	if f == nil {
		f = linkpreview.New()
	}
	s.previews = &linkPreviews{fetcher: f, slots: make(chan struct{}, linkPreviewParallel), flight: map[string]*previewCall{},
		rate: map[string]*rateWindow{}, perMin: linkPreviewPerMinute, maxBytes: linkPreviewDirMax}

	s.mux.HandleFunc("GET /api/link-preview", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		raw := r.URL.Query().Get("url")
		if raw == "" {
			return reject(http.StatusBadRequest, "url is required")
		}
		u, err := s.previews.fetcher.Parse(raw)
		if errors.Is(err, linkpreview.ErrBlocked) {
			writeJSON(w, http.StatusOK, linkPreview{Reason: linkpreview.ReasonBlocked})
			return nil
		}
		if err != nil {
			return reject(http.StatusBadRequest, "invalid url")
		}
		if r.URL.Query().Get("cached") == "1" {
			p, ok, err := s.cachedLinkPreview(owner, u.String())
			if err != nil {
				return err
			}
			if !ok {
				p = linkPreview{Reason: reasonNotCached}
			}
			writeJSON(w, http.StatusOK, s.signPreview(p))
			return nil
		}
		p, err := s.linkPreview(r.Context(), owner, u.String())
		if errors.Is(err, errRateLimited) {
			writeJSON(w, http.StatusTooManyRequests, linkPreview{Reason: reasonRateLimited})
			return nil
		}
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, s.signPreview(p))
		return nil
	}))

	// <Image> cannot attach headers, so it fetches by the address signature like attachments. The owner token works too.
	s.mux.HandleFunc("GET /api/link-preview/image/{hash}", func(w http.ResponseWriter, r *http.Request) {
		hash := r.PathValue("hash")
		_, byToken := s.ownerOf(r)
		if !imageHash.MatchString(hash) || !(byToken || s.signedOK(r, previewSignID(hash))) {
			http.NotFound(w, r)
			return
		}
		path := filepath.Join(s.linkPreviewDir(), hash)
		data, err := os.ReadFile(path)
		mt := linkpreview.SniffImage(data)
		if err != nil || mt == "" {
			http.NotFound(w, r)
			return
		}
		now := time.Now()
		os.Chtimes(path, now, now) // the 30-day cleanup is based on the last time it was used
		w.Header().Set("Content-Type", mt)
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Write(data)
	})
}

func previewSignID(hash string) string { return "link-preview/" + hash }

var errRateLimited = errors.New("link preview: rate limited")

// previewKey is the cache slot. Kept per user so nobody can tell whether another person saw an address.
func previewKey(owner, url string) string { return owner + " " + url }

// cachedLinkPreview returns stored results only. Gives them even if expired, and makes no outside connection.
func (s *Server) cachedLinkPreview(owner, url string) (linkPreview, bool, error) {
	raw, _, _, err := s.store.LinkPreview(previewKey(owner, url))
	if errors.Is(err, store.ErrNotFound) {
		return linkPreview{}, false, nil
	}
	if err != nil {
		return linkPreview{}, false, err
	}
	var p linkPreview
	if json.Unmarshal([]byte(raw), &p) != nil {
		return linkPreview{}, false, nil
	}
	return p, true, nil
}

// linkPreview returns the cache if alive, otherwise fetches and stores. Waiting for a slot stops if the request is cut off.
func (s *Server) linkPreview(ctx context.Context, owner, url string) (linkPreview, error) {
	key := previewKey(owner, url)
	raw, at, ok, err := s.store.LinkPreview(key)
	if err == nil {
		keep := linkPreviewFail
		if ok {
			keep = linkPreviewOK
		}
		var p linkPreview
		if time.Since(time.Unix(at, 0)) < keep && json.Unmarshal([]byte(raw), &p) == nil {
			return p, nil
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return linkPreview{}, err
	}

	lp := s.previews
	var c *previewCall
	for c == nil {
		lp.mu.Lock()
		other, busy := lp.flight[key]
		if !busy {
			if !lp.allowLocked(owner, time.Now()) {
				lp.mu.Unlock()
				return linkPreview{}, errRateLimited
			}
			c = &previewCall{done: make(chan struct{})}
			lp.flight[key] = c
		}
		lp.mu.Unlock()
		if !busy {
			break
		}
		select {
		case <-other.done:
		case <-ctx.Done():
			return linkPreview{}, ctx.Err()
		}
		if !other.aborted {
			return other.res, nil
		}
	}
	defer func() {
		lp.mu.Lock()
		delete(lp.flight, key)
		lp.mu.Unlock()
		close(c.done)
	}()

	select {
	case lp.slots <- struct{}{}:
	case <-ctx.Done():
		c.aborted = true
		return linkPreview{}, ctx.Err()
	case <-s.ctx.Done():
		c.aborted = true
		return linkPreview{}, s.ctx.Err()
	}
	got, ferr := lp.fetcher.Fetch(s.ctx, url)
	<-lp.slots

	now := time.Now().Unix()
	if ferr != nil {
		c.res = linkPreview{Reason: linkpreview.Reason(ferr), At: now}
	} else {
		c.res = linkPreview{OK: true, URL: got.URL, Kind: got.Kind, Title: got.Title, Description: got.Description,
			Site: got.Site, Author: got.Author, Image: s.saveImage(got.Image), Favicon: s.saveImage(got.Favicon), At: now}
	}
	data, _ := json.Marshal(c.res)
	if err := s.store.PutLinkPreview(key, string(data), now, c.res.OK); err != nil {
		log.Printf("link preview save: %v", err)
	}
	return c.res, nil
}

// saveImage stores the content under its hash name and returns the hash. "" on failure.
func (s *Server) saveImage(img *linkpreview.Image) string {
	if img == nil {
		return ""
	}
	sum := sha256.Sum256(img.Data)
	hash := hex.EncodeToString(sum[:])
	dir := s.linkPreviewDir()
	path := filepath.Join(dir, hash)
	if _, err := os.Stat(path); err == nil {
		now := time.Now()
		os.Chtimes(path, now, now)
		return hash
	}
	if dirSize(dir)+int64(len(img.Data)) > s.previews.maxBytes {
		log.Printf("link preview image: not saving, folder total exceeds %dMB", s.previews.maxBytes>>20)
		return ""
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("link preview image: %v", err)
		return ""
	}
	tmp, err := os.CreateTemp(dir, ".part-*")
	if err != nil {
		log.Printf("link preview image: %v", err)
		return ""
	}
	_, err = tmp.Write(img.Data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
		log.Printf("link preview image: %v", err)
		return ""
	}
	return hash
}

func dirSize(dir string) int64 {
	entries, _ := os.ReadDir(dir)
	var n int64
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !e.IsDir() {
			n += info.Size()
		}
	}
	return n
}

// signPreview turns a stored hash into an address signed for now, and refreshes the used image's timestamp.
func (s *Server) signPreview(p linkPreview) linkPreview {
	sign := func(hash string) string {
		if !imageHash.MatchString(hash) {
			return ""
		}
		path := filepath.Join(s.linkPreviewDir(), hash)
		now := time.Now()
		if os.Chtimes(path, now, now) != nil { // a deleted image gets no address
			return ""
		}
		exp := now.Add(urlTTL).Unix()
		return "/api/link-preview/image/" + hash + "?exp=" + strconv.FormatInt(exp, 10) + "&sig=" + s.sign(previewSignID(hash), exp)
	}
	p.Image, p.Favicon = sign(p.Image), sign(p.Favicon)
	return p
}

// sweepLinkPreviews deletes images unused for 30 days and old cache rows.
func (s *Server) sweepLinkPreviews(now time.Time) {
	cutoff := now.Add(-linkPreviewImageKeep)
	if err := s.store.PruneLinkPreviews(cutoff.Unix()); err != nil {
		log.Printf("link preview cleanup: %v", err)
	}
	entries, err := os.ReadDir(s.linkPreviewDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() || info.ModTime().After(cutoff) {
			continue
		}
		os.Remove(filepath.Join(s.linkPreviewDir(), e.Name()))
	}
}

// removeLinkPreviewFiles deletes the image folder whole on server close. WipeAll empties the table.
func (s *Server) removeLinkPreviewFiles() {
	if err := os.RemoveAll(s.linkPreviewDir()); err != nil {
		log.Printf("server close: link preview images: %v", err)
	}
}
