package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/linkpreview"
	"github.com/kyeotapp/kyeot/internal/store"
)

const lpToken = "test-owner-token-0123456789"

type lpRig struct {
	srv  *Server
	api  *httptest.Server
	dir  string
	site *httptest.Server
	hits sync.Map // path → *atomic.Int32
}

func (rg *lpRig) hit(path string) int32 {
	v, _ := rg.hits.LoadOrStore(path, new(atomic.Int32))
	return v.(*atomic.Int32).Load()
}

func lpPNG(t *testing.T) []byte {
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 3, 3)))
	return b.Bytes()
}

// newLPRig starts a server with a fake site (loopback) and a Fetcher that is opened only to it.
func newLPRig(t *testing.T, handler func(rg *lpRig, w http.ResponseWriter, r *http.Request)) *lpRig {
	t.Helper()
	rg := &lpRig{dir: t.TempDir()}
	rg.site = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, _ := rg.hits.LoadOrStore(r.URL.Path, new(atomic.Int32))
		v.(*atomic.Int32).Add(1)
		handler(rg, w, r)
	}))
	t.Cleanup(rg.site.Close)
	fake := netip.MustParseAddr("127.0.0.1") // public-ok
	f := linkpreview.New(
		linkpreview.WithPolicy(func(a netip.Addr) bool { return a.Unmap() != fake && linkpreview.Blocked(a) }, func(int) bool { return true }),
		linkpreview.WithOEmbed(rg.site.URL+"/oembed"),
		linkpreview.WithLookup(func(_ context.Context, host string) ([]netip.Addr, error) {
			return nil, fmt.Errorf("no dns in tests: %s", host)
		}),
	)
	srv, err := New(Options{DBPath: filepath.Join(t.TempDir(), "k.db"), OwnerToken: lpToken, UploadDir: t.TempDir(), LinkPreviewDir: rg.dir, LinkPreviewFetcher: f})
	if err != nil {
		t.Fatal(err)
	}
	rg.srv = srv
	rg.api = httptest.NewServer(srv.Handler())
	t.Cleanup(func() { rg.api.Close(); srv.Close() })
	return rg
}

func (rg *lpRig) get(t *testing.T, path, token string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", rg.api.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b bytes.Buffer
	b.ReadFrom(resp.Body)
	return resp.StatusCode, b.Bytes()
}

func (rg *lpRig) preview(t *testing.T, target string) linkPreview {
	t.Helper()
	code, body := rg.get(t, "/api/link-preview?url="+url.QueryEscape(target), lpToken)
	if code != 200 {
		t.Fatalf("%s: %d %s", target, code, body)
	}
	var p linkPreview
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func sitePage(t *testing.T) func(rg *lpRig, w http.ResponseWriter, r *http.Request) {
	img := lpPNG(t)
	return func(rg *lpRig, w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/img.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(img)
		case r.URL.Path == "/oembed":
			fmt.Fprintf(w, `{"title":"Fake video","author_name":"Fake channel","thumbnail_url":"%s/img.png"}`, rg.site.URL)
		case r.URL.Path == "/fail":
			http.Error(w, "nope", 500)
		case strings.HasPrefix(r.URL.Path, "/slow"):
			time.Sleep(150 * time.Millisecond)
			fallthrough
		default:
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<meta property="og:title" content="Title  %s"><meta property="og:description" content="Description"><meta property="og:image" content="/img.png"><link rel="icon" href="/img.png">`, r.URL.Path)
		}
	}
}

func TestLinkPreviewResponseAndSignedImage(t *testing.T) {
	rg := newLPRig(t, sitePage(t))
	if code, _ := rg.get(t, "/api/link-preview?url="+url.QueryEscape(rg.site.URL+"/a"), ""); code != 401 {
		t.Fatalf("no token %d", code)
	}
	p := rg.preview(t, rg.site.URL+"/a")
	if !p.OK || p.Kind != "page" || p.Title != "Title /a" || p.Description != "Description" || p.URL != rg.site.URL+"/a" || p.At == 0 {
		t.Fatalf("response: %+v", p)
	}
	if !strings.HasPrefix(p.Image, "/api/link-preview/image/") || !strings.Contains(p.Image, "sig=") || p.Favicon == "" {
		t.Fatalf("image address: %q %q", p.Image, p.Favicon)
	}
	if code, body := rg.get(t, p.Image, ""); code != 200 || !bytes.Equal(body, lpPNG(t)) { // fetched by signature alone
		t.Fatalf("signed image: %d", code)
	}
	bad := p.Image[:len(p.Image)-4] + "0000"
	if code, _ := rg.get(t, bad, ""); code != 404 {
		t.Fatalf("wrong signature: %d", code)
	}
	plain, _, _ := strings.Cut(p.Image, "?")
	if code, _ := rg.get(t, plain, ""); code != 404 {
		t.Fatalf("no signature: %d", code)
	}
	if code, _ := rg.get(t, plain, lpToken); code != 200 {
		t.Fatalf("with token: %d", code)
	}

	v := rg.preview(t, "https://youtu.be/abc123")
	if !v.OK || v.Kind != "video" || v.Title != "Fake video" || v.Author != "Fake channel" || v.Site != "YouTube" || v.Image == "" {
		t.Fatalf("YouTube: %+v", v)
	}
}

func TestLinkPreviewBlockedViaAPI(t *testing.T) {
	rg := newLPRig(t, sitePage(t))
	noDNS := linkpreview.WithLookup(func(_ context.Context, host string) ([]netip.Addr, error) {
		return nil, fmt.Errorf("no dns in tests: %s", host)
	})
	rg.srv.previews.fetcher = linkpreview.New(linkpreview.WithPolicy(linkpreview.Blocked, func(int) bool { return true }), noDNS)
	if p := rg.preview(t, rg.site.URL+"/x"); p.OK || p.Reason != "unreachable" { // a blocked IP looks the same as a name resolution failure
		t.Errorf("loopback: %+v", p)
	}
	rg.srv.previews.fetcher = linkpreview.New(noDNS) // the real port rule
	for _, u := range []string{"ftp://example.com/", "http://example.com:8080/", "https://example.com:22/"} {
		if p := rg.preview(t, u); p.OK || p.Reason != "blocked" {
			t.Errorf("%s: %+v", u, p)
		}
	}
	if rg.hit("/x") != 0 {
		t.Fatal("reached a blocked address")
	}
	if code, _ := rg.get(t, "/api/link-preview?url=", lpToken); code != 400 {
		t.Fatalf("empty url: %d", code)
	}
}

func TestLinkPreviewCacheAndRetry(t *testing.T) {
	rg := newLPRig(t, sitePage(t))
	ok, fail := rg.site.URL+"/c", rg.site.URL+"/fail"
	rg.preview(t, ok)
	rg.preview(t, ok)
	if rg.hit("/c") != 1 {
		t.Fatalf("success cache: fetched %d times", rg.hit("/c"))
	}
	if p := rg.preview(t, fail); p.OK || p.Reason != "unreachable" {
		t.Fatalf("failure: %+v", p)
	}
	rg.preview(t, fail)
	if rg.hit("/fail") != 1 {
		t.Fatalf("failure cache: %d times", rg.hit("/fail"))
	}
	age := func(u string, d time.Duration) {
		raw, _, okRow, err := rg.srv.store.LinkPreview(previewKey(store.Owner, u))
		if err != nil {
			t.Fatal(err)
		}
		rg.srv.store.PutLinkPreview(previewKey(store.Owner, u), raw, time.Now().Add(-d).Unix(), okRow)
	}
	age(fail, 61*time.Minute)
	age(ok, 6*24*time.Hour)
	rg.preview(t, fail)
	rg.preview(t, ok)
	if rg.hit("/fail") != 2 || rg.hit("/c") != 1 {
		t.Fatalf("failure after 1 hour, success after 7 days: fail %d, ok %d", rg.hit("/fail"), rg.hit("/c"))
	}
	age(ok, 7*24*time.Hour+time.Minute)
	rg.preview(t, ok)
	if rg.hit("/c") != 2 {
		t.Fatalf("did not refetch a success older than 7 days: %d", rg.hit("/c"))
	}
}

func TestLinkPreviewCoalesceAndLimit(t *testing.T) {
	var cur, peak atomic.Int32
	page := sitePage(t)
	rg := newLPRig(t, func(rg *lpRig, w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/slow") {
			n := cur.Add(1)
			defer cur.Add(-1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
		}
		page(rg, w, r)
	})
	var wg sync.WaitGroup
	for range 10 { // same address 10 times -> once
		wg.Go(func() { rg.preview(t, rg.site.URL+"/slow-same") })
	}
	wg.Wait()
	if rg.hit("/slow-same") != 1 {
		t.Fatalf("fetched the same address %d times", rg.hit("/slow-same"))
	}
	for i := range 12 {
		wg.Go(func() { rg.preview(t, fmt.Sprintf("%s/slow-%d", rg.site.URL, i)) })
	}
	wg.Wait()
	if p := peak.Load(); p > linkPreviewParallel || p < 2 {
		t.Fatalf("max concurrency %d (limit %d)", p, linkPreviewParallel)
	}
}

func TestLinkPreviewSweepAndWipe(t *testing.T) {
	rg := newLPRig(t, sitePage(t))
	p := rg.preview(t, rg.site.URL+"/w")
	files, _ := os.ReadDir(rg.dir)
	if len(files) != 1 { // the image and favicon have the same content, so one file
		t.Fatalf("%d image files", len(files))
	}
	path := filepath.Join(rg.dir, files[0].Name())
	rg.srv.sweepLinkPreviews(time.Now())
	if _, err := os.Stat(path); err != nil {
		t.Fatal("deleted a new image")
	}
	old := time.Now().Add(-31 * 24 * time.Hour)
	os.Chtimes(path, old, old)
	rg.srv.sweepLinkPreviews(time.Now())
	if _, err := os.Stat(path); err == nil {
		t.Fatal("did not delete an image unused for 30 days")
	}
	if q := rg.preview(t, rg.site.URL+"/w"); q.Image != "" {
		t.Fatalf("gave the address of a deleted image: %q (before %q)", q.Image, p.Image)
	}

	rg.preview(t, rg.site.URL+"/w2")
	req, _ := http.NewRequest("POST", rg.api.URL+"/api/server/close", strings.NewReader(`{"confirm":"`+confirmWord+`"}`))
	req.Header.Set("Authorization", "Bearer "+lpToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("server close: %d", resp.StatusCode)
	}
	if _, err := os.Stat(rg.dir); !os.IsNotExist(err) {
		t.Fatalf("the image folder remained after closing: %v", err)
	}
	if _, _, _, err := rg.srv.store.LinkPreview(previewKey(store.Owner, rg.site.URL+"/w2")); err != store.ErrNotFound {
		t.Fatalf("cache rows remained after closing: %v", err)
	}
}

func TestLinkPreviewFeature(t *testing.T) {
	if !slices.Contains(Features, "links.preview") {
		t.Fatal("links.preview is missing from Features")
	}
}

// L1: addresses not in the cache are allowed up to perMin times per person per minute. Beyond that, 429 rate_limited; the cache is still served.
func TestLinkPreviewRateLimit(t *testing.T) {
	rg := newLPRig(t, sitePage(t))
	rg.srv.previews.perMin = 3
	for i := range 3 {
		rg.preview(t, fmt.Sprintf("%s/r%d", rg.site.URL, i))
	}
	code, body := rg.get(t, "/api/link-preview?url="+url.QueryEscape(rg.site.URL+"/r9"), lpToken)
	var p linkPreview
	json.Unmarshal(body, &p)
	if code != http.StatusTooManyRequests || p.Reason != "rate_limited" || p.OK {
		t.Fatalf("fourth: %d %s", code, body)
	}
	if rg.hit("/r9") != 0 {
		t.Fatal("fetched despite exceeding the limit")
	}
	if p := rg.preview(t, rg.site.URL+"/r0"); !p.OK {
		t.Fatalf("blocked the cache too: %+v", p)
	}
	lp := rg.srv.previews
	now := time.Now()
	lp.mu.Lock()
	other := lp.allowLocked("someone-else", now)
	later := lp.allowLocked(store.Owner, now.Add(time.Minute))
	lp.mu.Unlock()
	if !other || !later {
		t.Fatalf("other person %v, after 1 minute %v", other, later)
	}
}

// L1: when the image folder exceeds its cap, images are not stored and only text is given.
func TestLinkPreviewImageDirCap(t *testing.T) {
	rg := newLPRig(t, sitePage(t))
	rg.srv.previews.maxBytes = int64(len(lpPNG(t))) - 1
	p := rg.preview(t, rg.site.URL+"/cap")
	if !p.OK || p.Title != "Title /cap" || p.Image != "" || p.Favicon != "" {
		t.Fatalf("over the cap: %+v", p)
	}
	if files, _ := os.ReadDir(rg.dir); len(files) != 0 {
		t.Fatalf("stored %d images", len(files))
	}
	rg.srv.previews.maxBytes = linkPreviewDirMax
	if p := rg.preview(t, rg.site.URL+"/cap2"); p.Image == "" {
		t.Fatal("gave no image within the cap")
	}
}

// L1: when a request waiting for a slot is cut off, the wait is released and another request waiting on the same address takes over.
func TestLinkPreviewSlotWaitHonorsRequest(t *testing.T) {
	rg := newLPRig(t, sitePage(t))
	lp := rg.srv.previews
	for range linkPreviewParallel {
		lp.slots <- struct{}{}
	}
	target := rg.site.URL + "/wait"
	ctxA, cancelA := context.WithCancel(context.Background())
	errA := make(chan error, 1)
	go func() { _, err := rg.srv.linkPreview(ctxA, store.Owner, target); errA <- err }()
	for { // wait for A to become the one that started first
		lp.mu.Lock()
		_, started := lp.flight[previewKey(store.Owner, target)]
		lp.mu.Unlock()
		if started {
			break
		}
		time.Sleep(time.Millisecond)
	}
	resB := make(chan linkPreview, 1)
	go func() {
		p, err := rg.srv.linkPreview(context.Background(), store.Owner, target)
		if err != nil {
			t.Error(err)
		}
		resB <- p
	}()
	time.Sleep(100 * time.Millisecond) // cut off after B starts waiting on A
	cancelA()
	select {
	case err := <-errA:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("A: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the cut-off request was not released from the slot wait")
	}
	for range linkPreviewParallel {
		<-lp.slots
	}
	select {
	case p := <-resB:
		if !p.OK || rg.hit("/wait") != 1 {
			t.Fatalf("B: %+v, fetch count %d", p, rg.hit("/wait"))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("B did not take over")
	}
}

// cached=1 returns stored results only. not_cached if none; expired ones are given; the outside site is never reached.
func TestLinkPreviewCachedOnly(t *testing.T) {
	if !slices.Contains(Features, "links.preview.cached") {
		t.Fatal("links.preview.cached is missing from Features")
	}
	rg := newLPRig(t, sitePage(t))
	cached := func(target string) linkPreview {
		t.Helper()
		code, body := rg.get(t, "/api/link-preview?cached=1&url="+url.QueryEscape(target), lpToken)
		if code != 200 {
			t.Fatalf("%s: %d %s", target, code, body)
		}
		var p linkPreview
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	u := rg.site.URL + "/q"
	for range 3 {
		if p := cached(u); p.OK || p.Reason != "not_cached" {
			t.Fatalf("missing address: %+v", p)
		}
	}
	if rg.hit("/q") != 0 {
		t.Fatalf("cached=1 reached the site %d times", rg.hit("/q"))
	}
	if code, _ := rg.get(t, "/api/link-preview?cached=1&url="+url.QueryEscape(u), ""); code != 401 {
		t.Fatalf("no token %d", code)
	}

	rg.preview(t, u)
	raw, _, okRow, _ := rg.srv.store.LinkPreview(previewKey(store.Owner, u))
	rg.srv.store.PutLinkPreview(previewKey(store.Owner, u), raw, time.Now().Add(-30*24*time.Hour).Unix(), okRow) // expired
	p := cached(u)
	if !p.OK || p.Title != "Title /q" || !strings.Contains(p.Image, "sig=") {
		t.Fatalf("expired stored result: %+v", p)
	}
	if rg.hit("/q") != 1 {
		t.Fatalf("must not refetch even when expired: %d", rg.hit("/q"))
	}

	rg.srv.previews.perMin = 0 // asking does not count toward the rate
	if p := cached(rg.site.URL + "/z"); p.Reason != "not_cached" {
		t.Fatalf("rate: %+v", p)
	}
}

func TestLinkPreviewCacheIsPerOwner(t *testing.T) {
	rg := newLPRig(t, sitePage(t))
	u := rg.site.URL + "/mine"
	rg.preview(t, u)
	if p, ok, _ := rg.srv.cachedLinkPreview(store.Owner, u); !ok || !p.OK {
		t.Fatalf("my cache: %v %+v", ok, p)
	}
	if _, ok, _ := rg.srv.cachedLinkPreview("someone-else", u); ok {
		t.Fatal("an address another person saw shows in my cache")
	}
	if _, err := rg.srv.linkPreview(context.Background(), "someone-else", u); err != nil {
		t.Fatal(err)
	}
	if rg.hit("/mine") != 2 {
		t.Fatalf("another user must not use someone else's cache and must fetch separately: %d", rg.hit("/mine"))
	}
}
