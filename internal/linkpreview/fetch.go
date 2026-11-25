// Package linkpreview fetches preview info (title, description, image) from third-party sites.
// Internal-address blocking is decided on the real IP in the dialer just before connecting. Redirects go through the same dialer.
package linkpreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	Timeout      = 5 * time.Second
	MaxRedirects = 3
	MaxHTML      = 512 << 10
	MaxImage     = 1 << 20
	MaxURL       = 2048
	UserAgent    = "kyeot-link-preview"
)

// Failure reason, used as-is in the response's reason.
const (
	ReasonBlocked     = "blocked"
	ReasonUnreachable = "unreachable"
	ReasonNotHTML     = "not_html"
	ReasonTooLarge    = "too_large"
	ReasonTimeout     = "timeout"
)

var ErrBlocked = errors.New("linkpreview: blocked address")

// ErrAddressBlocked is the case where the dialer blocked by IP. In the response it appears as unreachable, same as a name resolution failure.
var ErrAddressBlocked = errors.New("linkpreview: blocked ip")

// Preview is the fetched result. Images are carried as bytes; storing them and their addresses is up to the caller.
type Preview struct {
	URL         string
	Kind        string // video | page
	Title       string
	Description string
	Site        string
	Author      string
	Image       *Image
	Favicon     *Image
}

type Image struct {
	Mime string
	Data []byte
}

// Don't use the zero value of Fetcher. Create one with New.
type Fetcher struct {
	client *http.Client
	// for tests. Defaults: Blocked, ports 80/443, system DNS, real YouTube
	blocked   func(netip.Addr) bool
	allowPort func(int) bool
	lookup    func(ctx context.Context, host string) ([]netip.Addr, error)
	self      func() []netip.Prefix
	oembed    string
	timeout   time.Duration
}

type Option func(*Fetcher)

// WithPolicy is for tests. Use it only to reach a fake server (loopback, arbitrary port).
func WithPolicy(blocked func(netip.Addr) bool, allowPort func(int) bool) Option {
	return func(f *Fetcher) { f.blocked, f.allowPort = blocked, allowPort }
}

// WithLookup is a DNS override for tests.
func WithLookup(lookup func(ctx context.Context, host string) ([]netip.Addr, error)) Option {
	return func(f *Fetcher) { f.lookup = lookup }
}

// WithSelf is a test override of this server's own address ranges.
func WithSelf(self func() []netip.Prefix) Option { return func(f *Fetcher) { f.self = self } }

// WithTimeout is a test override of the overall time limit.
func WithTimeout(d time.Duration) Option { return func(f *Fetcher) { f.timeout = d } }

// WithOEmbed is a test override of the YouTube oEmbed address.
func WithOEmbed(endpoint string) Option { return func(f *Fetcher) { f.oembed = endpoint } }

func New(opts ...Option) *Fetcher {
	f := &Fetcher{
		blocked:   Blocked,
		allowPort: func(p int) bool { return p == 80 || p == 443 },
		lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		self:    interfacePrefixes,
		oembed:  "https://www.youtube.com/oembed",
		timeout: Timeout,
	}
	for _, o := range opts {
		o(f)
	}
	tr := &http.Transport{
		Proxy:                 nil, // going through an env-var proxy would skip the dialer check
		DialContext:           f.dial,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   f.timeout,
		ResponseHeaderTimeout: f.timeout,
	}
	f.client = &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			req.Header.Del("Referer")
			if len(via) > MaxRedirects {
				return errTooManyRedirects
			}
			if _, err := netip.ParseAddr(req.URL.Hostname()); err == nil {
				return fmt.Errorf("%w: redirect to ip literal", ErrBlocked)
			}
			return f.checkURL(req.URL)
		},
	}
	return f
}

func prefixes(list ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(list))
	for i, p := range list {
		out[i] = netip.MustParsePrefix(p)
	}
	return out
}

var (
	blockedV4 = prefixes(
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16", // public-ok
		"192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", // public-ok
		"224.0.0.0/4", "240.0.0.0/4",
	)
	globalV6  = netip.MustParsePrefix("2000::/3")
	blockedV6 = prefixes(
		"::ffff:0:0:0/96", "64:ff9b:1::/48", "100::/64", "2001::/32", "2001:db8::/32", "fec0::/10", // public-ok
	)
	compatV6 = netip.MustParsePrefix("::/96")
	nat64V6  = netip.MustParsePrefix("64:ff9b::/96")
	sixTo4   = netip.MustParsePrefix("2002::/16")
)

// Blocked checks whether the address is one the server must not reach. It is an allow-list:
// only IPv4 outside the ranges above and IPv6 within 2000::/3 and outside those ranges are allowed. IPv6 embedding an IPv4 is re-checked by the inner IPv4.
func Blocked(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() {
		return true
	}
	if a.Is6() {
		b := a.As16()
		switch {
		case compatV6.Contains(a), nat64V6.Contains(a):
			return Blocked(netip.AddrFrom4([4]byte(b[12:16])))
		case sixTo4.Contains(a):
			return Blocked(netip.AddrFrom4([4]byte(b[2:6])))
		}
		if !globalV6.Contains(a) {
			return true
		}
		for _, p := range blockedV6 {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	if !a.IsGlobalUnicast() {
		return true
	}
	for _, p := range blockedV4 {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// SelfPrefixes turns this server's interface addresses into ranges to block: the address itself for IPv4, its /64 for IPv6. Loopback is blocked by Blocked.
func SelfPrefixes(addrs []net.Addr) []netip.Prefix {
	var out []netip.Prefix
	for _, na := range addrs {
		ipn, ok := na.(*net.IPNet)
		if !ok {
			continue
		}
		a, ok := netip.AddrFromSlice(ipn.IP)
		if !ok {
			continue
		}
		a = a.Unmap()
		if a.IsLoopback() {
			continue
		}
		bits := 32
		if a.Is6() {
			bits = 64
		}
		p, _ := a.WithZone("").Prefix(bits)
		out = append(out, p)
	}
	return out
}

var selfCache struct {
	sync.Mutex
	at   time.Time
	list []netip.Prefix
}

// interfacePrefixes caches SelfPrefixes for 10 seconds.
func interfacePrefixes() []netip.Prefix {
	selfCache.Lock()
	defer selfCache.Unlock()
	if time.Since(selfCache.at) > 10*time.Second {
		addrs, err := net.InterfaceAddrs()
		if err == nil {
			selfCache.list, selfCache.at = SelfPrefixes(addrs), time.Now()
		}
	}
	return selfCache.list
}

// deny is the dialer's verdict: policy (Blocked) or this server's own ranges.
func (f *Fetcher) deny(a netip.Addr) bool {
	if f.blocked(a) {
		return true
	}
	a = a.Unmap().WithZone("")
	for _, p := range f.self() {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// checkURL checks scheme and port for the first address and every redirect. The address (IP) check is done by the dialer.
func (f *Fetcher) checkURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q", ErrBlocked, u.Scheme)
	}
	if u.User != nil || u.Hostname() == "" {
		return fmt.Errorf("%w: host", ErrBlocked)
	}
	port := 80
	if u.Scheme == "https" {
		port = 443
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return fmt.Errorf("%w: port", ErrBlocked)
		}
		port = n
	}
	if !f.allowPort(port) {
		return fmt.Errorf("%w: port %d", ErrBlocked, port)
	}
	return nil
}

// dial resolves the name to IPs, drops blocked IPs, and connects directly to the rest. At connect time Control checks the socket address again.
func (f *Fetcher) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || !f.allowPort(port) {
		return nil, fmt.Errorf("%w: port %s", ErrBlocked, portStr)
	}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else if addrs, err = f.lookup(ctx, host); err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: f.timeout, Control: func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil || f.deny(ap.Addr()) || !f.allowPort(int(ap.Port())) {
			return fmt.Errorf("%w: %s", ErrAddressBlocked, address)
		}
		return nil
	}}
	lastErr := error(fmt.Errorf("%w: %s has no address", ErrAddressBlocked, host))
	for _, a := range addrs {
		if f.deny(a) {
			lastErr = fmt.Errorf("%w: %s", ErrAddressBlocked, a)
			continue
		}
		c, err := d.DialContext(ctx, network, netip.AddrPortFrom(a.Unmap(), uint16(port)).String())
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// Reason turns a Fetch error into the response's reason. A blocked IP (ErrAddressBlocked) becomes unreachable, like a name resolution failure.
func Reason(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, ErrBlocked):
		return ReasonBlocked
	case errors.Is(err, errNotHTML):
		return ReasonNotHTML
	case errors.Is(err, errTooLarge):
		return ReasonTooLarge
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return ReasonTimeout
	default:
		return ReasonUnreachable
	}
}

var (
	errNotHTML  = errors.New("linkpreview: not html")
	errTooLarge = errors.New("linkpreview: too large")

	errTooManyRedirects = errors.New("linkpreview: too many redirects")
)

// Parse validates and normalizes the requested address (the #… fragment is dropped). A blocked scheme or port gives ErrBlocked.
func (f *Fetcher) Parse(raw string) (*url.URL, error) {
	if len(raw) > MaxURL {
		return nil, fmt.Errorf("%w: url too long", ErrBlocked)
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	u.Fragment, u.RawFragment = "", ""
	u.Host = strings.ToLower(u.Host)
	return u, f.checkURL(u)
}

// Fetch gets the preview within 5 seconds overall. If the image can't be fetched, it succeeds without one.
func (f *Fetcher) Fetch(ctx context.Context, raw string) (Preview, error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	u, err := f.Parse(raw)
	if err != nil {
		return Preview{}, err
	}
	if isYouTube(u.Hostname()) {
		return f.youtube(ctx, u)
	}
	return f.page(ctx, u)
}

func (f *Fetcher) get(ctx context.Context, u string, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", accept)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		resp.Body.Close()
		return nil, fmt.Errorf("linkpreview: status %d", resp.StatusCode)
	}
	return resp, nil
}

func (f *Fetcher) page(ctx context.Context, u *url.URL) (Preview, error) {
	resp, err := f.get(ctx, u.String(), "text/html")
	if err != nil {
		return Preview{}, err
	}
	defer resp.Body.Close()
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt != "text/html" {
		return Preview{}, fmt.Errorf("%w: %q", errNotHTML, mt)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxHTML+1))
	if err != nil {
		return Preview{}, err
	}
	truncated := len(body) > MaxHTML
	if truncated {
		body = body[:MaxHTML]
	}
	meta := parseHTML(string(body))
	if meta.title == "" && truncated {
		return Preview{}, fmt.Errorf("%w: no title in first %d bytes", errTooLarge, MaxHTML)
	}
	base := resp.Request.URL // resolve relative addresses against the post-redirect address
	p := Preview{URL: u.String(), Kind: "page", Title: meta.title, Description: meta.description, Site: meta.site}
	if p.Site == "" {
		p.Site = strings.TrimPrefix(base.Hostname(), "www.")
	}
	if ref := resolve(base, meta.image); ref != "" {
		p.Image = f.image(ctx, ref)
	}
	if ref := resolve(base, meta.icon); ref != "" {
		p.Favicon = f.image(ctx, ref)
	}
	return p, nil
}

func resolve(base *url.URL, ref string) string {
	if ref == "" {
		return ""
	}
	r, err := base.Parse(ref)
	if err != nil || (r.Scheme != "http" && r.Scheme != "https") {
		return ""
	}
	return r.String()
}

var imageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true, "image/gif": true}

// image returns nil on failure. It is discarded if over 1MB or not an allowed type.
func (f *Fetcher) image(ctx context.Context, ref string) *Image {
	u, err := url.Parse(ref)
	if err != nil || f.checkURL(u) != nil {
		return nil
	}
	resp, err := f.get(ctx, ref, "image/png,image/jpeg,image/webp,image/gif")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.ContentLength > MaxImage {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxImage+1))
	if err != nil || len(data) == 0 || len(data) > MaxImage {
		return nil
	}
	sniffed := SniffImage(data)
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); !imageTypes[mt] || sniffed == "" {
		return nil
	}
	return &Image{Mime: sniffed, Data: data}
}

// SniffImage detects the image type from the bytes. Returns "" unless it is one of the four allowed.
func SniffImage(data []byte) string {
	if mt := http.DetectContentType(data); imageTypes[mt] {
		return mt
	}
	return ""
}

func isYouTube(host string) bool {
	host = strings.TrimSuffix(host, ".")
	return host == "youtu.be" || host == "youtube.com" || strings.HasSuffix(host, ".youtube.com")
}

func (f *Fetcher) youtube(ctx context.Context, u *url.URL) (Preview, error) {
	q := url.Values{"url": {u.String()}, "format": {"json"}}
	resp, err := f.get(ctx, f.oembed+"?"+q.Encode(), "application/json")
	if err != nil {
		return Preview{}, err
	}
	defer resp.Body.Close()
	var o struct {
		Title        string `json:"title"`
		AuthorName   string `json:"author_name"`
		ThumbnailURL string `json:"thumbnail_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&o); err != nil {
		return Preview{}, err
	}
	p := Preview{URL: u.String(), Kind: "video", Title: clip(o.Title, maxTitle), Site: "YouTube", Author: clip(o.AuthorName, maxSite)}
	if o.ThumbnailURL != "" {
		p.Image = f.image(ctx, o.ThumbnailURL)
	}
	return p, nil
}

const (
	maxTitle = 300
	maxDesc  = 500
	maxSite  = 100
)

// clip turns control characters into spaces, removes bidirectional controls and zero-width characters, collapses spaces to one, and truncates to n characters.
func clip(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r):
			return ' '
		case unicode.Is(unicode.Bidi_Control, r), r == '\u200b', r == '\u2060', r == '\ufeff', r == '\u180e':
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
