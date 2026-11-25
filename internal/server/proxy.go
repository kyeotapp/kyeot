package server

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// proxyHeaders are the headers a front proxy adds; Tailscale serve/Funnel add Tailscale-*.
var proxyHeaders = []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip"}

// ProxyHeader returns the proxy header name if a loopback request carries one, else empty.
func ProxyHeader(remote string, h http.Header) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return ""
	}
	for _, k := range proxyHeaders {
		if h.Get(k) != "" {
			return k
		}
	}
	for k := range h {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), "Tailscale-") {
			return http.CanonicalHeaderKey(k)
		}
	}
	return ""
}

// ProxyWarning is why a same-PC proxy setup is unsupported; used by the log and kyeot service status.
const ProxyWarning = "같은 PC 의 프록시(https 프록시, Tailscale serve·Funnel) 뒤에 둔 것으로 보인다. 이 배치는 지원하지 않는다: 모든 요청이 이 기기에서 온 것으로 보여 KYEOT_NETS 가 거르지 못하고, 짝짓기 10번 실패 잠금이 모든 사람에게 걸린다. 요청은 막지 않았다"

type proxySeen struct {
	once sync.Once
}

// noteProxy logs and marks only the first proxy signal seen in this run.
func (s *Server) noteProxy(r *http.Request) {
	header := ProxyHeader(r.RemoteAddr, r.Header)
	if header == "" {
		return
	}
	s.proxy.once.Do(func() {
		log.Printf("warning: loopback request has a %s header. %s", header, ProxyWarning)
		if s.opts.ProxyMarkFile == "" {
			return
		}
		line := fmt.Sprintf("%s %s\n", time.Now().Format(time.RFC3339), header)
		if err := os.WriteFile(s.opts.ProxyMarkFile, []byte(line), 0o600); err != nil {
			log.Printf("proxy mark file: %v", err)
		}
	})
}

// ReadProxyMark reads the "time header" line of the mark file; empty if absent.
func ReadProxyMark(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return strings.TrimSpace(string(b)), err
}
