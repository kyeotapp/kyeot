package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// DefaultLatestURL is the public release file holding the newest version number.
const DefaultLatestURL = "https://github.com/kyeotapp/kyeot/releases/latest/download/VERSION"

const (
	latestEvery   = 24 * time.Hour
	latestTimeout = 15 * time.Second
	latestMax     = 64 // bytes; a version number is far shorter
)

// latestClient sends no cookies and refuses redirects that leave https.
func latestClient(base *http.Client) *http.Client {
	c := &http.Client{Timeout: latestTimeout}
	if base != nil {
		c.Transport = base.Transport
	}
	c.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if r.URL.Scheme != "https" {
			return errors.New("redirect to non-https")
		}
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return nil
	}
	return c
}

// latestLoop checks once right after start and then every LatestEvery. Failures only log one line.
func (s *Server) latestLoop() {
	defer s.bg.Done()
	every := s.opts.LatestEvery
	if every <= 0 {
		every = latestEvery
	}
	client := latestClient(s.opts.LatestClient)
	for {
		if !s.closed.Load() { // a server closed while running stays quiet
			v, err := fetchLatest(s.ctx, client, s.opts.LatestURL, s.opts.Version)
			switch {
			case s.ctx.Err() != nil:
				return
			case err != nil:
				log.Printf("update check failed: %v", err)
			default:
				s.latest.Store(&v)
			}
		}
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// fetchLatest GETs the VERSION file. Only a User-Agent with our version is sent.
func fetchLatest(ctx context.Context, client *http.Client, url, current string) (string, error) {
	if !strings.HasPrefix(url, "https://") {
		return "", errors.New("not https")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "kyeot/"+current)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, latestMax+1))
	if err != nil {
		return "", err
	}
	if len(b) > latestMax {
		return "", errors.New("response too large")
	}
	v := strings.TrimSpace(string(b))
	if !releaseVersion.MatchString(v) {
		return "", errors.New("not a version number")
	}
	return v, nil
}

// latestVersion is the newest released version seen, or "" if unknown.
func (s *Server) latestVersion() string {
	if p := s.latest.Load(); p != nil {
		return *p
	}
	return ""
}
