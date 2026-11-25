package netwatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Raw is one measured value. Response time is ping's text as is; empty if there is no response.
type Raw struct {
	GW, GWMAC, IP string
	GwMs, WanMs   string
	DNS           string // answer status (NOERROR, NXDOMAIN); empty if no answer
	TCP, UDP      string // "-" if lsof is missing
	Top           string
	Pwr           string
	Sockets       string // raw lsof output for the snapshot
}

// Prober wraps the measurement commands. Tests inject a fake.
// An error from Measure means measuring itself failed (no ping, permissions). A network outage is not an error.
type Prober interface {
	Measure(ctx context.Context) (Raw, error)
	Snapshot(ctx context.Context, r Raw) string
}

// Judge decides the state in the same order as netwatch.sh.
func Judge(r Raw) string {
	switch {
	case r.GW == "":
		return "NO_ROUTE"
	case r.GwMs == "":
		return "GW_DOWN"
	case r.WanMs == "" && r.DNS == "":
		return "WAN_DOWN"
	case r.DNS == "":
		return "DNS_DOWN"
	}
	return "OK"
}

// System measures with this computer's commands (Mac, Linux).
type System struct {
	WAN string // external ping target; 1.1.1.1 if empty
}

var (
	pingTime = regexp.MustCompile(`time[=<]([0-9.]+)`)
	macAddr  = regexp.MustCompile(`(?i)\b([0-9a-f]{1,2}:){5}[0-9a-f]{1,2}\b`)
	powerSrc = regexp.MustCompile(`'(.*) Power'`)
	inetAddr = regexp.MustCompile(`inet ([0-9.]+)`)
)

// bin finds system commands even under launchd's narrow PATH.
func bin(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, dir := range []string{"/sbin", "/usr/sbin", "/bin", "/usr/bin"} {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s 명령이 없다", name)
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	p, err := bin(name)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, p, args...).CombinedOutput()
	return string(out), err
}

func field(out, key string) string {
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && k == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (s System) Measure(ctx context.Context) (Raw, error) {
	var r Raw
	if _, err := bin("ping"); err != nil {
		return r, err
	}
	var ifc string
	if runtime.GOOS == "darwin" {
		if _, err := bin("route"); err != nil {
			return r, err
		}
		out, _ := run(ctx, "route", "-n", "get", "default") // fails when there is no route; that is NO_ROUTE
		r.GW, ifc = field(out, "gateway"), field(out, "interface")
		if ifc != "" {
			out, _ := run(ctx, "ipconfig", "getifaddr", ifc)
			r.IP = strings.TrimSpace(out)
		}
		out, _ = run(ctx, "pmset", "-g", "ps")
		if m := powerSrc.FindStringSubmatch(out); m != nil {
			r.Pwr = m[1]
		}
	} else {
		if _, err := bin("ip"); err != nil {
			return r, err
		}
		out, _ := run(ctx, "ip", "route", "show", "default")
		if f := strings.Fields(strings.SplitN(out, "\n", 2)[0]); len(f) >= 5 && f[0] == "default" {
			r.GW, ifc = f[2], f[4]
		}
		if ifc != "" {
			out, _ := run(ctx, "ip", "-4", "addr", "show", ifc)
			if m := inetAddr.FindStringSubmatch(out); m != nil {
				r.IP = m[1]
			}
		}
		r.Pwr = linuxPower()
	}
	if r.GW != "" {
		ms, err := s.ping(ctx, r.GW)
		if err != nil {
			return r, err
		}
		r.GwMs = ms
		out, _ := run(ctx, "arp", "-n", r.GW)
		r.GWMAC = strings.ToLower(macAddr.FindString(out))
	}
	ms, err := s.ping(ctx, cmpOr(s.WAN, "1.1.1.1"))
	if err != nil {
		return r, err
	}
	r.WanMs = ms
	r.DNS = probeDNS(ctx)
	r.TCP, r.UDP, r.Top, r.Sockets = sockets(ctx)
	return r, nil
}

// ping returns the response time; empty if no response; an error if lacking permission.
func (System) ping(ctx context.Context, host string) (string, error) {
	args := []string{"-c1", "-W1", host}
	if runtime.GOOS == "darwin" {
		args = []string{"-c1", "-t1", host}
	}
	out, err := run(ctx, "ping", args...)
	if m := pingTime.FindStringSubmatch(out); m != nil {
		return m[1], nil
	}
	low := strings.ToLower(out)
	if err != nil && strings.Contains(low, "socket:") && (strings.Contains(low, "not permitted") || strings.Contains(low, "permission denied")) { // a sendto failure is a network problem, so exclude it
		return "", errors.New("ping 권한이 없다")
	}
	return "", nil
}

// probeDNS asks for a nonexistent name. NXDOMAIN also means DNS answered, so it is normal.
// The system resolver can return "no such name" even with no network, so ask the server directly with the Go resolver.
func probeDNS(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	name := fmt.Sprintf("nw%d%d.example.com", time.Now().Unix(), os.Getpid())
	_, err := (&net.Resolver{PreferGo: true}).LookupHost(ctx, name)
	var de *net.DNSError
	switch {
	case err == nil:
		return "NOERROR"
	case errors.As(err, &de) && de.IsNotFound:
		return "NXDOMAIN"
	}
	return ""
}

func sockets(ctx context.Context) (tcp, udp, top, raw string) {
	if _, err := bin("lsof"); err != nil {
		return "-", "-", "", ""
	}
	out, _ := run(ctx, "lsof", "-nP", "-i")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) > 0 {
		lines = lines[1:]
	}
	nt, nu := 0, 0
	byCmd := map[string]int{}
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		if strings.Contains(l, " TCP ") {
			nt++
		}
		if strings.Contains(l, " UDP ") {
			nu++
		}
		byCmd[f[0]]++
	}
	cmds := make([]string, 0, len(byCmd))
	for c := range byCmd {
		cmds = append(cmds, c)
	}
	sort.Slice(cmds, func(i, j int) bool {
		if byCmd[cmds[i]] != byCmd[cmds[j]] {
			return byCmd[cmds[i]] > byCmd[cmds[j]]
		}
		return cmds[i] < cmds[j]
	})
	var parts []string
	for _, c := range cmds[:min(5, len(cmds))] {
		parts = append(parts, fmt.Sprintf("%s=%d", c, byCmd[c]))
	}
	return fmt.Sprint(nt), fmt.Sprint(nu), strings.Join(parts, ","), strings.Join(lines, "\n")
}

func linuxPower() string {
	files, _ := filepath.Glob("/sys/class/power_supply/BAT*/status")
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil && strings.TrimSpace(string(b)) == "Discharging" {
			return "Battery"
		}
	}
	return "AC"
}

func head(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	return strings.Join(lines[:min(n, len(lines))], "\n")
}

// Snapshot collects the routing table, arp, sockets, power, top CPU, and router front page at the moment the state turned abnormal.
func (System) Snapshot(ctx context.Context, r Raw) string {
	var b strings.Builder
	sec := func(title, body string) { fmt.Fprintf(&b, "### %s\n%s\n", title, strings.TrimRight(body, "\n")) }
	cmd := func(n int, name string, args ...string) string {
		out, _ := run(ctx, name, args...)
		return head(out, n)
	}
	sec("열린 소켓", r.Sockets)
	byCmd, byRemote := map[string]int{}, map[string]int{}
	for _, l := range strings.Split(r.Sockets, "\n") {
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		byCmd[f[0]]++
		if len(f) >= 9 {
			if _, remote, ok := strings.Cut(f[8], "->"); ok {
				if i := strings.LastIndex(remote, ":"); i > 0 {
					remote = remote[:i]
				}
				byRemote[remote]++
			}
		}
	}
	sec("프로세스별 소켓 수", counts(byCmd, 0))
	sec("원격 주소별 연결 수 상위", counts(byRemote, 30))
	sec("CPU 상위", cmd(26, "ps", "-axo", "pid,etime,%cpu,command"))
	if runtime.GOOS == "darwin" {
		sec("route", cmd(30, "netstat", "-rn", "-f", "inet"))
		sec("dns", cmd(20, "scutil", "--dns"))
		sec("전원", cmd(10, "pmset", "-g", "ps")+"\n"+cmd(30, "pmset", "-g", "assertions"))
	} else {
		sec("route", cmd(30, "ip", "route"))
		resolv, _ := os.ReadFile("/etc/resolv.conf")
		sec("dns", string(resolv))
	}
	sec("arp", cmd(100, "arp", "-an"))
	if r.GW != "" {
		sec("공유기 첫 화면 (http://"+r.GW+"/)", routerPage(ctx, r.GW))
	}
	return b.String()
}

func counts(m map[string]int, limit int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return m[keys[i]] > m[keys[j]] || (m[keys[i]] == m[keys[j]] && keys[i] < keys[j]) })
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
	}
	var b bytes.Buffer
	for _, k := range keys {
		fmt.Fprintf(&b, "%7d %s\n", m[k], k)
	}
	return b.String()
}

func routerPage(ctx context.Context, gw string) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+gw+"/", nil)
	if err != nil {
		return err.Error()
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return err.Error()
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 8<<10))
	return head(res.Status+"\n"+string(body), 40)
}
