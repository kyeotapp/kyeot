package server

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"log"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Expo Updates protocol 1. Serves the published manifest, signature, and files as is; the signature, not a token, prevents forgery.

var (
	safeRuntime = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	safeFile    = regexp.MustCompile(`^[a-f0-9]{64}(\.[a-z0-9]{1,10})?$`)
)

func (s *Server) updateDir() string {
	if s.opts.UpdateDir != "" {
		return s.opts.UpdateDir
	}
	return filepath.Join(filepath.Dir(s.opts.DBPath), "updates")
}

func (s *Server) updateRoutes() {
	s.mux.HandleFunc("GET /updates/manifest", s.updateManifest)
	s.mux.HandleFunc("GET /updates/files/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !safeFile.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".bundle") || strings.HasSuffix(name, ".hbc") {
			log.Printf("update bundle fetch: %s %.12s", r.RemoteAddr, name)
		}
		// the name is the content's sha256, so it never changes
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.ServeFile(w, r, filepath.Join(s.updateDir(), "files", name))
	})
}

func updateHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("expo-protocol-version", "1")
	h.Set("expo-sfv-version", "0")
	h.Set("cache-control", "private, max-age=0")
}

func (s *Server) updateManifest(w http.ResponseWriter, r *http.Request) {
	log.Printf("update check: %s %s version %.12s", r.RemoteAddr, r.Header.Get("expo-platform"), r.Header.Get("expo-runtime-version"))
	updateHeaders(w)
	if v := r.Header.Get("expo-protocol-version"); v != "" && v != "1" {
		http.Error(w, "unsupported protocol version", http.StatusNotAcceptable)
		return
	}
	platform := r.Header.Get("expo-platform")
	if platform != "ios" && platform != "android" {
		http.Error(w, "expo-platform must be ios or android", http.StatusBadRequest)
		return
	}
	runtime := r.Header.Get("expo-runtime-version")
	if !safeRuntime.MatchString(runtime) {
		http.Error(w, "expo-runtime-version is required", http.StatusBadRequest)
		return
	}
	accept := r.Header.Get("Accept")
	multi := accept == "" || strings.Contains(accept, "multipart/mixed")
	jsonOK := accept == "" || strings.Contains(accept, "application/json") || strings.Contains(accept, "application/expo+json") || strings.Contains(accept, "*/*")
	if !multi && !jsonOK {
		http.Error(w, "unsupported accept", http.StatusNotAcceptable)
		return
	}

	dir := filepath.Join(s.updateDir(), platform, runtime)
	manifest, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		// no bundle for this runtimeVersion: the app keeps using its embedded bundle
		if multi {
			w.WriteHeader(http.StatusNoContent)
		} else {
			http.Error(w, "no update for this runtime version", http.StatusNotFound)
		}
		return
	}
	sig, err := os.ReadFile(filepath.Join(dir, "manifest.sig"))
	if err != nil {
		http.Error(w, "update is not signed", http.StatusInternalServerError)
		return
	}
	signature := fmt.Sprintf(`sig="%s", keyid="main", alg="rsa-v1_5-sha256"`, strings.TrimSpace(string(sig)))

	if !multi {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("expo-signature", signature)
		w.Write(manifest)
		return
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.SetBoundary(boundary()); err != nil {
		http.Error(w, "boundary", http.StatusInternalServerError)
		return
	}
	part := func(name string, extra map[string]string, content []byte) error {
		// keep the name lowercase. Set canonicalizes to Expo-Signature, but iOS looks only for lowercase
		h := textproto.MIMEHeader{
			"content-disposition": {fmt.Sprintf(`form-data; name="%s"`, name)},
			"content-type":        {"application/json; charset=utf-8"},
		}
		for k, v := range extra {
			h[k] = []string{v}
		}
		pw, err := mw.CreatePart(h)
		if err == nil {
			_, err = pw.Write(content)
		}
		return err
	}
	if err := part("manifest", map[string]string{"expo-signature": signature}, manifest); err != nil {
		http.Error(w, "multipart", http.StatusInternalServerError)
		return
	}
	if err := part("extensions", nil, []byte(`{"assetRequestHeaders":{}}`)); err != nil {
		http.Error(w, "multipart", http.StatusInternalServerError)
		return
	}
	mw.Close()
	w.Header().Set("Content-Type", "multipart/mixed; boundary="+mw.Boundary())
	w.Write(body.Bytes())
}

func boundary() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("kyeot-%x", b)
}
