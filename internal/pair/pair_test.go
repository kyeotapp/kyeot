package pair

import (
	"net"
	"net/url"
	"strings"
	"testing"
)

func TestLinkRoundTrip(t *testing.T) {
	l := Link("http://100.64.1.2:7700", "fake-token&x=1") // public-ok
	u, err := url.Parse(l)
	if err != nil || u.Scheme != "kyeot" || u.Host != "connect" {
		t.Fatalf("address format: %q %v", l, err)
	}
	if u.Query().Get("url") != "http://100.64.1.2:7700" || u.Query().Get("token") != "fake-token&x=1" { // public-ok
		t.Fatalf("value did not round-trip: %q", l)
	}
}

func TestHostsOrder(t *testing.T) {
	mk := func(s string) net.Addr { ip, n, _ := net.ParseCIDR(s); n.IP = ip; return n }
	got := Hosts([]net.Addr{mk("127.0.0.1/8"), mk("192.168.0.5/24"), mk("8.8.8.8/32"), mk("100.101.1.2/32"), mk("fe80::1/64")}) // public-ok
	if strings.Join(got, ",") != "100.101.1.2,192.168.0.5" {                                                                    // public-ok
		t.Fatalf("got %v", got)
	}
}

func TestTerminalDrawsSquare(t *testing.T) {
	s, err := Terminal(Link("http://192.168.0.5:7700", "fake")) // public-ok
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	w := len([]rune(lines[0])) - len("\x1b[30;107m") - len("\x1b[0m")
	if w < 25 || len(lines) < w/2 {
		t.Fatalf("bad size: %d x %d", w, len(lines))
	}
}
