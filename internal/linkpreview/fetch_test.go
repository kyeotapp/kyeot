package linkpreview_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/linkpreview"
)

// All of the spec's blocked-address table.
func TestBlockedTable(t *testing.T) {
	blocked := []string{
		"10.0.0.1", "10.255.255.255", // public-ok
		"172.16.0.1", "172.31.255.254", // public-ok
		"192.168.0.1", "192.168.255.255", // public-ok
		"127.0.0.1", "127.8.8.8", "::1", // public-ok
		"169.254.169.254", "169.254.0.1", "fe80::1", // public-ok
		"100.64.0.1", "100.100.100.100", "100.127.255.255", // public-ok
		"fc00::1", "fd7a:115c:a1e0::1", "fdff::1", // public-ok
		"224.0.0.1", "239.255.255.250", "ff02::1", "ff05::2", // public-ok
		"0.0.0.0", "0.1.2.3", "::", // public-ok
		"255.255.255.255", "240.0.0.1", // public-ok
		"::ffff:10.0.0.1", "::ffff:127.0.0.1", "::ffff:169.254.169.254", // public-ok
		// added (M2): IPv6 embedding IPv4, and special ranges
		"::127.0.0.1", "::a00:1", "::ffff:0:7f00:1", "::ffff:0:808:808", // public-ok
		"64:ff9b::7f00:1", "64:ff9b::a00:1", "64:ff9b::a9fe:a9fe", "64:ff9b:1::a00:1", "64:ff9b:1::808:808", // public-ok
		"2002:7f00:1::", "2002:c0a8:101::1", "2002:a9fe:a9fe::1", // public-ok
		"2001:0:4136:e378:8000:63bf:3fff:fdd2", "2001::1", // public-ok
		"fec0::1", "100::1", "2001:db8::1", "2001:db8:ffff::1", // public-ok
		"192.0.0.1", "192.0.0.170", "198.18.0.1", "198.19.255.255", // public-ok
		"192.0.2.1", "198.51.100.1", "203.0.113.1", // public-ok
		"4000::1", "::2", // outside 2000::/3
	}
	for _, s := range blocked {
		if !linkpreview.Blocked(netip.MustParseAddr(s)) {
			t.Errorf("%s was not blocked", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "100.63.255.255", "100.128.0.1", "172.32.0.1", "2001:4860:4860::8888", // public-ok
		"198.17.255.255", "198.20.0.1", "192.0.1.1", "2001:1::1", "2002:808:808::1", "64:ff9b::808:808", "::808:808"} { // public-ok
		if linkpreview.Blocked(netip.MustParseAddr(s)) {
			t.Errorf("%s is a public address but was blocked", s)
		}
	}
}

var anyPort = func(int) bool { return true }

// loopback is the fake public server. Only it is opened; everything else stays blocked per the spec.
var loopbackPublic = netip.MustParseAddr("127.0.0.1")

func exceptFake(a netip.Addr) bool { return a.Unmap() != loopbackPublic && linkpreview.Blocked(a) }

func fakeDNS(table map[string][]string) func(context.Context, string) ([]netip.Addr, error) {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		ips, ok := table[host]
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		var out []netip.Addr
		for _, s := range ips {
			out = append(out, netip.MustParseAddr(s))
		}
		return out, nil
	}
}

// addrBlocked checks the dialer's internal verdict that it blocked by IP. The response reason must be unreachable, same as a name resolution failure.
func addrBlocked(t *testing.T, f *linkpreview.Fetcher, u string) bool {
	t.Helper()
	_, err := f.Fetch(context.Background(), u)
	if errors.Is(err, linkpreview.ErrAddressBlocked) && linkpreview.Reason(err) != linkpreview.ReasonUnreachable {
		t.Errorf("%s: reason for blocked IP is %s, want unreachable", u, linkpreview.Reason(err))
	}
	return errors.Is(err, linkpreview.ErrAddressBlocked)
}

func reason(t *testing.T, f *linkpreview.Fetcher, u string) string {
	t.Helper()
	_, err := f.Fetch(context.Background(), u)
	if err == nil {
		return "ok"
	}
	return linkpreview.Reason(err)
}

// The dialer blocks by the real IP, whether the address is numeric or a name.
func TestDialerBlocksInternalAddresses(t *testing.T) {
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, "<title>internal</title>")
	}))
	defer ts.Close()
	port := ts.Listener.Addr().(*net.TCPAddr).Port

	dns := fakeDNS(map[string][]string{
		"localhost":         {"127.0.0.1", "::1"}, // public-ok
		"loop.example":      {"127.0.0.1"},        // public-ok
		"private.example":   {"10.1.2.3"},         // public-ok
		"meta.example":      {"169.254.169.254"},  // public-ok
		"ts.example":        {"100.100.1.1"},      // public-ok
		"ula.example":       {"fd00::1"},          // public-ok
		"v6loop.example":    {"::1"},              // public-ok
		"mapped.example":    {"::ffff:127.0.0.1"},
		"zero.example":      {"0.0.0.0"},               // public-ok
		"multicast.example": {"224.0.0.251"},           // public-ok
		"mixed.example":     {"10.9.9.9", "127.0.0.1"}, // public-ok
	})
	f := linkpreview.New(linkpreview.WithPolicy(linkpreview.Blocked, anyPort), linkpreview.WithLookup(dns))
	for _, u := range []string{
		ts.URL,
		fmt.Sprintf("http://[::1]:%d/", port),
		fmt.Sprintf("http://[::ffff:127.0.0.1]:%d/", port),
		fmt.Sprintf("http://localhost:%d/", port),
		fmt.Sprintf("http://loop.example:%d/", port),
		fmt.Sprintf("http://mapped.example:%d/", port),
		fmt.Sprintf("http://v6loop.example:%d/", port),
		fmt.Sprintf("http://mixed.example:%d/", port),
		"http://private.example/", "http://meta.example/", "http://ts.example/", "http://ula.example/",
		"http://zero.example/", "http://multicast.example/",
		"http://10.0.0.1/", "http://169.254.169.254/latest/meta-data/", "http://[fe80::1]/", "http://0.0.0.0/", // public-ok
	} {
		if !addrBlocked(t, f, u) {
			t.Errorf("%s: dialer did not block", u)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("reached the internal server %d times", hits.Load())
	}
}

// The first address is public but the redirect goes internal.
func TestRedirectIntoInternalIsBlocked(t *testing.T) {
	var internalHits atomic.Int32
	internal := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		internalHits.Add(1)
		fmt.Fprint(w, "<title>internal</title>")
	}))
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip("no IPv6 loopback")
	}
	internal.Listener = ln
	internal.Start()
	defer internal.Close()

	target := ""
	pub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer pub.Close()
	iport := ln.Addr().(*net.TCPAddr).Port

	dns := fakeDNS(map[string][]string{"intra.example": {"192.168.1.10"}, "v6.example": {"::1"}}) // public-ok
	f := linkpreview.New(linkpreview.WithPolicy(exceptFake, anyPort), linkpreview.WithLookup(dns))
	for _, tgt := range []string{ // names are blocked by the dialer by IP
		fmt.Sprintf("http://v6.example:%d/", iport),
		"http://intra.example/",
	} {
		target = tgt
		if !addrBlocked(t, f, pub.URL) {
			t.Errorf("→ %s: dialer did not block", tgt)
		}
	}
	for _, tgt := range []string{ // IP literals and schemes are blocked by the redirect check
		internal.URL,
		"http://169.254.169.254/", // public-ok
		"file:///etc/passwd",
		"gopher://example.com/",
	} {
		target = tgt
		if got := reason(t, f, pub.URL); got != linkpreview.ReasonBlocked {
			t.Errorf("→ %s: %s, want blocked", tgt, got)
		}
	}
	if internalHits.Load() != 0 {
		t.Fatalf("reached the internal server %d times via redirect", internalHits.Load())
	}
}

func TestPortsAndSchemes(t *testing.T) {
	f := linkpreview.New()
	for _, u := range []string{"http://example.com:8080/", "https://example.com:22/", "ftp://example.com/", "file:///etc/hosts", "javascript:alert(1)", "http://user:pw@example.com/", "http:///x"} {
		if _, err := f.Parse(u); !errors.Is(err, linkpreview.ErrBlocked) {
			t.Errorf("%s: %v, want blocked", u, err)
		}
	}
	for _, u := range []string{"http://example.com/", "https://example.com:443/a", "http://example.com:80/"} {
		if _, err := f.Parse(u); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	// redirects to another port are blocked too
	pub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://pub.example:22/", http.StatusFound)
	}))
	defer pub.Close()
	pubPort := pub.Listener.Addr().(*net.TCPAddr).Port
	g := linkpreview.New(linkpreview.WithPolicy(exceptFake, func(p int) bool { return p == pubPort }),
		linkpreview.WithLookup(fakeDNS(map[string][]string{"pub.example": {"127.0.0.1"}}))) // public-ok
	if got := reason(t, g, pub.URL); got != linkpreview.ReasonBlocked {
		t.Fatalf("port 22 redirect: %s", got)
	}
}

// pubHost is the address that names the fake server (loopback). Redirects reject IP literals, so a name is needed.
func pubHost(ts *httptest.Server) (string, linkpreview.Option) {
	port := ts.Listener.Addr().(*net.TCPAddr).Port
	return fmt.Sprintf("http://pub.example:%d", port), linkpreview.WithLookup(fakeDNS(map[string][]string{"pub.example": {"127.0.0.1"}})) // public-ok
}

func TestRedirectLimit(t *testing.T) {
	var base string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n int
		fmt.Sscanf(r.URL.Query().Get("n"), "%d", &n)
		if n > 0 {
			http.Redirect(w, r, fmt.Sprintf("%s/?n=%d", base, n-1), http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<title>end</title>")
	}))
	defer ts.Close()
	base, dns := pubHost(ts)
	f := linkpreview.New(linkpreview.WithPolicy(exceptFake, anyPort), dns)
	if got := reason(t, f, ts.URL+"/?n=3"); got != "ok" {
		t.Fatalf("3 redirects: %s", got)
	}
	if got := reason(t, f, ts.URL+"/?n=4"); got != linkpreview.ReasonUnreachable {
		t.Fatalf("4 redirects: %s", got)
	}
}

func TestTimeoutNotHTMLTooLarge(t *testing.T) {
	big := strings.Repeat("a", linkpreview.MaxHTML+10)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/slow":
			select {
			case <-r.Context().Done():
			case <-time.After(3 * time.Second):
			}
		case "/json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{}`)
		case "/big":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html><body>"+big)
		case "/big-titled":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<head><meta property="og:title" content="in front"></head><body>`+big+`<title>behind</title>`)
		case "/404":
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	f := linkpreview.New(linkpreview.WithPolicy(exceptFake, anyPort), linkpreview.WithTimeout(300*time.Millisecond), linkpreview.WithLookup(fakeDNS(nil)))
	for path, want := range map[string]string{
		"/slow": linkpreview.ReasonTimeout, "/json": linkpreview.ReasonNotHTML,
		"/big": linkpreview.ReasonTooLarge, "/big-titled": "ok", "/404": linkpreview.ReasonUnreachable,
	} {
		if got := reason(t, f, ts.URL+path); got != want {
			t.Errorf("%s: %s, want %s", path, got, want)
		}
	}
	if got := reason(t, f, "http://nowhere.invalid/"); got != linkpreview.ReasonUnreachable {
		t.Errorf("unknown name: %s", got)
	}
}

func pngBytes(t *testing.T, size int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for i := range img.Pix {
		img.Pix[i] = byte(i * 7)
	}
	img.Set(0, 0, color.RGBA{1, 2, 3, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestOpenGraphAndFallbacks(t *testing.T) {
	small := pngBytes(t, 4)
	huge := append(pngBytes(t, 4), make([]byte, linkpreview.MaxImage)...)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/og":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><head>
<meta property="og:title" content="Open &amp; Graph">
<META PROPERTY='og:description' CONTENT='desc   many   spaces'>
<meta property="og:site_name" content="Example Site">
<meta property="og:image" content="/img/a.png">
<link rel="shortcut icon" href="favicon.png">
<title>ignored title</title></head></html>`)
		case "/plain":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<title>plain title</title><meta name="description" content="plain description"><meta property="og:image" content="/img/huge.png"><link rel="icon" href="/img/text">`)
		case "/img/a.png", "/favicon.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(small)
		case "/img/huge.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(huge)
		case "/img/text":
			w.Header().Set("Content-Type", "image/png")
			fmt.Fprint(w, "<svg/>")
		}
	}))
	defer ts.Close()
	f := linkpreview.New(linkpreview.WithPolicy(exceptFake, anyPort))
	p, err := f.Fetch(context.Background(), ts.URL+"/og#frag")
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != "page" || p.Title != "Open & Graph" || p.Description != "desc many spaces" || p.Site != "Example Site" || p.URL != ts.URL+"/og" {
		t.Fatalf("og: %+v", p)
	}
	if p.Image == nil || p.Image.Mime != "image/png" || !bytes.Equal(p.Image.Data, small) || p.Favicon == nil {
		t.Fatalf("images: %+v %+v", p.Image, p.Favicon)
	}
	p, err = f.Fetch(context.Background(), ts.URL+"/plain")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(ts.URL)
	if p.Title != "plain title" || p.Description != "plain description" || p.Site != u.Hostname() {
		t.Fatalf("plain: %+v", p)
	}
	if p.Image != nil || p.Favicon != nil {
		t.Fatalf("images over 1MB and non-image content are discarded: %v %v", p.Image != nil, p.Favicon != nil)
	}
}

func TestYouTubeUsesOEmbed(t *testing.T) {
	thumb := pngBytes(t, 4)
	var gotURL string
	var mux http.ServeMux
	ts := httptest.NewServer(&mux)
	defer ts.Close()
	mux.HandleFunc("/oembed", func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Query().Get("url")
		if r.URL.Query().Get("format") != "json" || r.Header.Get("User-Agent") != linkpreview.UserAgent {
			http.Error(w, "bad", 400)
			return
		}
		fmt.Fprintf(w, `{"title":"Mac mini experiment","author_name":"Virtual Channel","thumbnail_url":"%s/thumb.jpg"}`, ts.URL)
	})
	mux.HandleFunc("/thumb.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(thumb)
	})
	f := linkpreview.New(linkpreview.WithPolicy(exceptFake, anyPort), linkpreview.WithOEmbed(ts.URL+"/oembed"))
	for _, u := range []string{"https://youtu.be/abc123", "https://www.youtube.com/watch?v=abc123", "https://m.youtube.com/watch?v=abc123"} {
		p, err := f.Fetch(context.Background(), u)
		if err != nil {
			t.Fatal(u, err)
		}
		if p.Kind != "video" || p.Title != "Mac mini experiment" || p.Author != "Virtual Channel" || p.Site != "YouTube" || p.Image == nil || gotURL != u {
			t.Fatalf("%s: %+v (oembed url=%s)", u, p, gotURL)
		}
	}
}

// M1: this server's own address is blocked even if the policy allows it.
func TestSelfAddressBlocked(t *testing.T) {
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer ts.Close()
	port := ts.Listener.Addr().(*net.TCPAddr).Port
	self := func() []netip.Prefix {
		return []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("2606:4700:1:2::/64")} // public-ok
	}
	dns := fakeDNS(map[string][]string{"me.example": {"127.0.0.1"}, "same64.example": {"2606:4700:1:2::99"}}) // public-ok
	f := linkpreview.New(linkpreview.WithPolicy(exceptFake, anyPort), linkpreview.WithLookup(dns), linkpreview.WithSelf(self))
	for _, u := range []string{fmt.Sprintf("http://me.example:%d/", port), "http://same64.example/"} {
		if !addrBlocked(t, f, u) {
			t.Errorf("%s: own address was not blocked", u)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("reached own address %d times", hits.Load())
	}

	got := linkpreview.SelfPrefixes([]net.Addr{
		&net.IPNet{IP: net.ParseIP("192.0.2.7"), Mask: net.CIDRMask(24, 32)},           // public-ok
		&net.IPNet{IP: net.ParseIP("2001:db8:1:2::5"), Mask: net.CIDRMask(128, 128)},   // public-ok
		&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},            // public-ok
		&net.IPNet{IP: net.ParseIP("::ffff:198.51.100.9"), Mask: net.CIDRMask(24, 32)}, // public-ok
	})
	want := []string{"192.0.2.7/32", "2001:db8:1:2::/64", "198.51.100.9/32"} // public-ok
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("SelfPrefixes: %v, want %v", got, want)
	}
}

// The default Fetcher blocks real interface addresses. Skipped if there is no non-loopback IPv4.
func TestDefaultSelfUsesInterfaces(t *testing.T) {
	addrs, _ := net.InterfaceAddrs()
	var ip string
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
			ip = n.IP.String()
			break
		}
	}
	if ip == "" {
		t.Skip("no non-loopback IPv4 interface")
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		t.Skip(err)
	}
	var hits atomic.Int32
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	ts.Listener = ln
	ts.Start()
	defer ts.Close()
	open := func(netip.Addr) bool { return false } // the policy allows everything; only own-address blocking remains
	f := linkpreview.New(linkpreview.WithPolicy(open, anyPort), linkpreview.WithLookup(fakeDNS(map[string][]string{"me.example": {ip}})))
	if !addrBlocked(t, f, fmt.Sprintf("http://me.example:%d/", ln.Addr().(*net.TCPAddr).Port)) || hits.Load() != 0 {
		t.Fatalf("interface address was not blocked (reached %d times)", hits.Load())
	}
}

// M1: a redirect target that is an IP literal is rejected even if public. It is blocked before connecting, so the internet is never reached.
func TestRedirectToIPLiteralRejected(t *testing.T) {
	target := ""
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer ts.Close()
	base, dns := pubHost(ts)
	f := linkpreview.New(linkpreview.WithPolicy(exceptFake, anyPort), dns)
	for _, tgt := range []string{"http://8.8.8.8/", "https://[2001:4860:4860::8888]/", "http://[fe80::1%25en0]/"} { // public-ok
		target = tgt
		_, err := f.Fetch(context.Background(), base+"/")
		if !errors.Is(err, linkpreview.ErrBlocked) || linkpreview.Reason(err) != linkpreview.ReasonBlocked {
			t.Errorf("→ %s: %v", tgt, err)
		}
	}
}

// L3: the request after a redirect has no Referer.
func TestRedirectDropsReferer(t *testing.T) {
	var base, referer string
	var reached atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/from" {
			http.Redirect(w, r, base+"/to", http.StatusFound)
			return
		}
		reached.Store(true)
		referer = r.Header.Get("Referer")
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<title>arrived</title>")
	}))
	defer ts.Close()
	base, dns := pubHost(ts)
	f := linkpreview.New(linkpreview.WithPolicy(exceptFake, anyPort), dns)
	if _, err := f.Fetch(context.Background(), base+"/from?secret=1"); err != nil || !reached.Load() {
		t.Fatalf("redirect: %v", err)
	}
	if referer != "" {
		t.Fatalf("Referer was attached: %q", referer)
	}
}

// L5: strips control characters, bidirectional controls, and zero-width characters.
func TestStripsControlCharacters(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oembed":
			fmt.Fprint(w, `{"title":"video\u202etitle\u0007","author_name":"\u2066channel\u2069\u200b"}`)
		default:
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<meta property=\"og:title\" content=\"a\u202eb\u200bc\x01d\ufeff\">"+
				"<meta property=\"og:description\" content=\"de&#8238;sc&#x200B;ription\x7f\">"+
				"<meta property=\"og:site_name\" content=\"\u2067site\u200f\u2060\">")
		}
	}))
	defer ts.Close()
	f := linkpreview.New(linkpreview.WithPolicy(exceptFake, anyPort), linkpreview.WithOEmbed(ts.URL+"/oembed"))
	p, err := f.Fetch(context.Background(), ts.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "abc d" || p.Description != "description" || p.Site != "site" {
		t.Fatalf("page: %q %q %q", p.Title, p.Description, p.Site)
	}
	v, err := f.Fetch(context.Background(), "https://youtu.be/x")
	if err != nil {
		t.Fatal(err)
	}
	if v.Title != "videotitle" || v.Author != "channel" {
		t.Fatalf("video: %q %q", v.Title, v.Author)
	}
}
