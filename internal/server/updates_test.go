package server

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func updateServer(t *testing.T, opts Options) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	opts.DBPath, opts.OwnerToken, opts.UpdateDir = ":memory:", "test-owner-token-0123456789", dir
	srv, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); srv.Close() })
	return ts, dir
}

// publish mimics scripts/publish-update.sh: writes the manifest and signs its bytes.
func publish(t *testing.T, dir, platform, runtime string, key *rsa.PrivateKey, manifest []byte) {
	t.Helper()
	d := filepath.Join(dir, platform, runtime)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(manifest)
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(d, "manifest.json"), manifest, 0o644)
	os.WriteFile(filepath.Join(d, "manifest.sig"), []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644)
}

func get(t *testing.T, url string, h map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for k, v := range h {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

var sigHeader = regexp.MustCompile(`^sig="([A-Za-z0-9+/=]+)", keyid="main", alg="rsa-v1_5-sha256"$`)

// verify checks the RSA-SHA256 signature over the manifest body bytes, as expo-updates does.
func verify(t *testing.T, pub *rsa.PublicKey, header string, body []byte) error {
	t.Helper()
	m := sigHeader.FindStringSubmatch(header)
	if m == nil {
		t.Fatalf("expo-signature format: %q", header)
	}
	sig, _ := base64.StdEncoding.DecodeString(m[1])
	sum := sha256.Sum256(body)
	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig)
}

func TestUpdateManifestMultipartSigned(t *testing.T) {
	ts, dir := updateServer(t, Options{})
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	manifest := []byte(`{"id":"00000000-0000-4000-8000-000000000001","createdAt":"2026-11-14T00:00:00.000Z","runtimeVersion":"abc123","launchAsset":{"hash":"x","key":"k","contentType":"application/javascript","url":"http://h/updates/files/k"},"assets":[],"metadata":{},"extra":{}}`)
	publish(t, dir, "ios", "abc123", key, manifest)

	res := get(t, ts.URL+"/updates/manifest", map[string]string{
		"expo-protocol-version": "1", "expo-platform": "ios", "expo-runtime-version": "abc123",
		"Accept":                "multipart/mixed,application/expo+json;q=0.9,application/json;q=0.8",
		"expo-expect-signature": `sig, keyid="main", alg="rsa-v1_5-sha256"`,
	})
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	for k, want := range map[string]string{"expo-protocol-version": "1", "expo-sfv-version": "0", "Cache-Control": "private, max-age=0"} {
		if got := res.Header.Get(k); got != want {
			t.Fatalf("%s = %q, want %q", k, got, want)
		}
	}
	mt, params, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/mixed" {
		t.Fatalf("content-type %q", res.Header.Get("Content-Type"))
	}
	raw, _ := io.ReadAll(res.Body)
	if !bytes.Contains(raw, []byte("\r\nexpo-signature: ")) { // iOS looks only for the lowercase name
		t.Fatal("part header must be lowercase expo-signature")
	}
	mr := multipart.NewReader(bytes.NewReader(raw), params["boundary"])
	parts := map[string][]byte{}
	var sig string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(p)
		parts[p.FormName()] = b
		if p.FormName() == "manifest" {
			sig = p.Header.Get("expo-signature")
			if ct := p.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
				t.Fatalf("manifest content-type %q", ct)
			}
		}
	}
	if string(parts["manifest"]) != string(manifest) {
		t.Fatalf("manifest is not byte-for-byte: %q", parts["manifest"])
	}
	if string(parts["extensions"]) != `{"assetRequestHeaders":{}}` {
		t.Fatalf("extensions %q", parts["extensions"])
	}
	if err := verify(t, &key.PublicKey, sig, parts["manifest"]); err != nil {
		t.Fatalf("signature verification failed: %v", err)
	}
	// The app must reject a bundle signed with another key
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	if verify(t, &other.PublicKey, sig, parts["manifest"]) == nil {
		t.Fatal("verification passed with a different key")
	}
}

func TestUpdateManifestJSON(t *testing.T) {
	ts, dir := updateServer(t, Options{})
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	manifest := []byte(`{"id":"x","runtimeVersion":"r1"}`)
	publish(t, dir, "android", "r1", key, manifest)
	res := get(t, ts.URL+"/updates/manifest", map[string]string{"expo-platform": "android", "expo-runtime-version": "r1", "Accept": "application/expo+json"})
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || string(body) != string(manifest) || res.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("json: %d %q %s", res.StatusCode, body, res.Header.Get("Content-Type"))
	}
	if err := verify(t, &key.PublicKey, res.Header.Get("expo-signature"), body); err != nil {
		t.Fatalf("json signature: %v", err)
	}
}

func TestUpdateManifestNoUpdateAndBadRequests(t *testing.T) {
	ts, dir := updateServer(t, Options{})
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	publish(t, dir, "ios", "new", key, []byte(`{"id":"x"}`))
	base := map[string]string{"expo-protocol-version": "1", "expo-platform": "ios", "Accept": "multipart/mixed"}
	with := func(k, v string) map[string]string {
		h := map[string]string{}
		for a, b := range base {
			h[a] = b
		}
		h[k] = v
		return h
	}
	cases := []struct {
		name string
		h    map[string]string
		want int
	}{
		{"runtimeVersion mismatch (old native)", with("expo-runtime-version", "old"), http.StatusNoContent},
		{"runtimeVersion mismatch json", map[string]string{"expo-platform": "ios", "expo-runtime-version": "old", "Accept": "application/json"}, http.StatusNotFound},
		{"bad platform", map[string]string{"expo-platform": "web", "expo-runtime-version": "new"}, http.StatusBadRequest},
		{"no runtimeVersion", base, http.StatusBadRequest},
		{"path-escape runtimeVersion", with("expo-runtime-version", "../ios/new"), http.StatusBadRequest},
		{"protocol version differs", map[string]string{"expo-protocol-version": "0", "expo-platform": "ios", "expo-runtime-version": "new"}, http.StatusNotAcceptable},
		{"unacceptable format", map[string]string{"expo-platform": "ios", "expo-runtime-version": "new", "Accept": "text/html"}, http.StatusNotAcceptable},
	}
	for _, c := range cases {
		res := get(t, ts.URL+"/updates/manifest", c.h)
		if res.StatusCode != c.want {
			t.Errorf("%s: %d, want %d", c.name, res.StatusCode, c.want)
		}
		if res.Header.Get("expo-protocol-version") != "1" {
			t.Errorf("%s: no protocol header", c.name)
		}
	}
	// Without a signature file, nothing is served
	os.Remove(filepath.Join(dir, "ios", "new", "manifest.sig"))
	if res := get(t, ts.URL+"/updates/manifest", with("expo-runtime-version", "new")); res.StatusCode != http.StatusInternalServerError {
		t.Errorf("unsigned bundle: %d", res.StatusCode)
	}
}

func TestUpdateFilesAndNetworkCheck(t *testing.T) {
	ts, dir := updateServer(t, Options{})
	name := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.js"
	os.MkdirAll(filepath.Join(dir, "files"), 0o755)
	os.WriteFile(filepath.Join(dir, "files", name), []byte("bundle"), 0o644)
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("no"), 0o644)
	res := get(t, ts.URL+"/updates/files/"+name, nil)
	if b, _ := io.ReadAll(res.Body); res.StatusCode != 200 || string(b) != "bundle" {
		t.Fatalf("file: %d %q", res.StatusCode, b)
	}
	for _, bad := range []string{"/updates/files/..%2Fsecret.txt", "/updates/files/secret.txt", "/updates/files/" + name[:63] + ".js"} {
		if res := get(t, ts.URL+bad, nil); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", bad, res.StatusCode)
		}
	}
	// Outside the allowed network, the update URL is blocked too
	nets, _ := ParseNets("10.255.255.0/24") // public-ok
	blocked, _ := updateServer(t, Options{AllowNets: nets})
	if res := get(t, blocked.URL+"/updates/manifest", map[string]string{"expo-platform": "ios", "expo-runtime-version": "r"}); res.StatusCode != http.StatusForbidden {
		t.Errorf("outside allowed network: %d", res.StatusCode)
	}
}
