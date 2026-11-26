package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"log"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kyeotapp/kyeot/internal/server"
)

// tlsFiles are KYEOT_TLS_CERT and KYEOT_TLS_KEY. With neither, plain http; with only one, a bad configuration.
func tlsFiles() (cert, key string, err error) {
	cert, key = getenv("TLS_CERT"), getenv("TLS_KEY")
	if (cert == "") != (key == "") {
		return "", "", errors.New("KYEOT_TLS_CERT 와 KYEOT_TLS_KEY 는 함께 준다")
	}
	return cert, key, nil
}

func readCert(certFile string) *x509.Certificate {
	raw, err := os.ReadFile(certFile)
	if err != nil {
		return nil
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil
	}
	c, _ := x509.ParseCertificate(block.Bytes)
	return c
}

// certHost is the certificate's first domain name, used as the connection QR address. Empty if unreadable.
func certHost(certFile string) string {
	c := readCert(certFile)
	if c == nil || len(c.DNSNames) == 0 {
		return ""
	}
	return strings.TrimPrefix(c.DNSNames[0], "*.")
}

// certCovers reports whether the certificate passes the name check when the phone connects by host. True if unreadable (no warning).
func certCovers(certFile, host string) bool {
	c := readCert(certFile)
	return c == nil || c.VerifyHostname(host) == nil
}

// localNets are ranges not reachable from the internet. Any range outside them counts as open to the internet.
var localNets = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("fd7a:115c:a1e0::/48"), // public-ok
	netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16"), // public-ok
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("fc00::/7"),
}

func localPrefix(p netip.Prefix) bool {
	p = p.Masked()
	for _, l := range localNets {
		if l.Bits() <= p.Bits() && l.Contains(p.Addr()) {
			return true
		}
	}
	return false
}

// openToInternet reports whether the accepted-network setting has a range outside private, Tailscale and loopback. allowNets filters bad settings first.
func openToInternet(spec string) bool {
	nets, err := server.ParseNets(spec)
	if err != nil {
		return false
	}
	for _, n := range nets {
		ones, _ := n.Mask.Size()
		a, ok := netip.AddrFromSlice(n.IP)
		if !ok || !localPrefix(netip.PrefixFrom(a, ones)) {
			return true
		}
	}
	return false
}

// plainLAN reports whether the accepted networks include a private LAN while serving without https.
func plainLAN(spec string, tls bool) bool {
	if tls {
		return false
	}
	nets, err := server.ParseNets(spec)
	if err != nil {
		return false
	}
	for _, n := range nets {
		for _, ip := range []net.IP{net.IPv4(10, 0, 0, 1), net.IPv4(172, 16, 0, 1), net.IPv4(192, 168, 0, 1)} {
			if n.Contains(ip) {
				return true
			}
		}
	}
	return false
}

// checkPlain blocks startup if it is open to the internet without https, because the access key would travel in plaintext.
// With allowPlain (KYEOT_ALLOW_PLAIN) it only warns and starts.
func checkPlain(spec string, tls, allowPlain bool) error {
	if tls || !openToInternet(spec) {
		return nil
	}
	if allowPlain {
		log.Print("warning: KYEOT_NETS opens it to the internet without https. The access key travels unencrypted (allowed by KYEOT_ALLOW_PLAIN)")
		return nil
	}
	return errors.New("시작 거부: KYEOT_NETS(" + spec + ") 가 인터넷에 열려 있는데 https 가 없다. 접속 키가 암호화 없이 오간다.\n" +
		"고치는 법: KYEOT_TLS_CERT 와 KYEOT_TLS_KEY 로 인증서를 주거나, https 프록시 뒤라면 KYEOT_NETS 를 프록시 주소로 좁힌다(같은 컴퓨터면 loopback).\n" +
		"그래도 평문으로 열려면 KYEOT_ALLOW_PLAIN=1")
}

// truthy reads 1, true, yes and on as true.
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// certReloader re-reads the certificate file when it changes (modification time). A renewed certificate is used without a restart.
type certReloader struct {
	cert, key string
	mu        sync.Mutex
	mod       time.Time
	c         *tls.Certificate
}

func newCertReloader(cert, key string) (*certReloader, error) {
	if fi, err := os.Stat(key); err == nil && fi.Mode().Perm()&0o077 != 0 {
		log.Printf("warning: TLS key file %s is readable by other users (%v). Narrow it with chmod 600", key, fi.Mode().Perm())
	}
	r := &certReloader{cert: cert, key: key}
	if _, err := r.get(nil); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *certReloader) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var mod time.Time
	for _, f := range []string{r.cert, r.key} {
		if fi, err := os.Stat(f); err == nil && fi.ModTime().After(mod) {
			mod = fi.ModTime()
		}
	}
	if r.c != nil && mod.Equal(r.mod) {
		return r.c, nil
	}
	c, err := tls.LoadX509KeyPair(r.cert, r.key)
	if err != nil {
		if r.c != nil { // may be mid-write. Keep using the old one until the next change
			r.mod = mod
			log.Printf("could not reload the TLS certificate: %v", err)
			return r.c, nil
		}
		return nil, err
	}
	r.c, r.mod = &c, mod
	return r.c, nil
}
