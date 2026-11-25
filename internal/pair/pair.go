// Package pair builds the connection QR code the phone app scans.
package pair

import (
	"net"
	"net/url"
	"strings"

	"rsc.io/qr"
)

// Link is the connection address the app reads. Only the in-app scanner reads it; it is not handled as an external deep link.
func Link(server, token string) string {
	q := url.Values{"url": {server}, "token": {token}}
	return "kyeot://connect?" + q.Encode()
}

var tailscale = func() *net.IPNet { _, n, _ := net.ParseCIDR("100.64.0.0/10"); return n }() // public-ok

// IsTailscale checks whether the address is one Tailscale hands out (CGNAT 100.64.0.0/10). // public-ok
func IsTailscale(ip net.IP) bool { return ip != nil && tailscale.Contains(ip) }

// Hosts returns this device's addresses the phone can reach, in preferred order: Tailscale, then private network.
func Hosts(addrs []net.Addr) []string {
	var ts, lan []string
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.To4() == nil || n.IP.IsLoopback() {
			continue
		}
		switch {
		case IsTailscale(n.IP):
			ts = append(ts, n.IP.String())
		case n.IP.IsPrivate():
			lan = append(lan, n.IP.String())
		}
	}
	return append(ts, lan...)
}

// Terminal draws the QR as terminal characters. One character covers two vertical cells.
func Terminal(text string) (string, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	const quiet = 2
	n := code.Size
	dark := func(x, y int) bool { return x >= 0 && y >= 0 && x < n && y < n && code.Black(x, y) }
	var b strings.Builder
	for y := -quiet; y < n+quiet; y += 2 {
		b.WriteString("\x1b[30;107m") // black dots on white regardless of terminal colors
		for x := -quiet; x < n+quiet; x++ {
			top, bot := dark(x, y), dark(x, y+1)
			switch {
			case top && bot:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bot:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\x1b[0m\n")
	}
	return b.String(), nil
}
