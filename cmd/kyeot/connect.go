package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kyeotapp/kyeot/internal/hermesbridge"
	"github.com/kyeotapp/kyeot/internal/pair"
)

// connectFile holds the server address and agent token this computer connects with (0600). Later runs need only `kyeot connect`.
func connectFile() string {
	if v := os.Getenv("KYEOT_CONNECT_FILE"); v != "" {
		return v
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "" // no token in the shared temp folder
	}
	return filepath.Join(dir, "kyeot", "connect.json")
}

type connection struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// runConnect connects Hermes on another computer to the kyeot server.
//
//	kyeot connect <server address> <6-digit app code>   first time, or to reconnect
//	kyeot connect                                       with the saved connection
//	--foreground                                        run here without registering a service
//	--yes                                               overwrite a saved connection without asking (KYEOT_CONNECT_YES=1 also works)
func runConnect(args []string) error {
	foreground, yes := false, os.Getenv("KYEOT_CONNECT_YES") == "1"
	var rest []string
	for _, a := range args {
		switch a {
		case "--foreground", "-foreground":
			foreground = true
		case "--yes", "-yes", "-y":
			yes = true
		default:
			rest = append(rest, a)
		}
	}
	args = rest
	if connectFile() == "" {
		return errors.New("설정 폴더를 찾지 못했다(HOME). KYEOT_CONNECT_FILE 로 저장 위치를 정한다")
	}
	var c connection
	switch len(args) {
	case 2:
		url := strings.TrimRight(args[0], "/")
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			url = "http://" + url
		}
		if u, err := neturl.Parse(url); err != nil || u.Host == "" {
			return errors.New("서버 주소 형식이 틀렸다. 예: http://100.x.y.z:7700")
		}
		if plainToInternet(url) && !plainAllowed() {
			return fmt.Errorf("%s 는 인터넷에서 닿는 주소라 http 로는 토큰이 암호화 없이 오간다. https 주소를 주거나 Tailscale 주소를 쓴다(그래도 쓰려면 KYEOT_ALLOW_PLAIN=1)", url)
		}
		if err := confirmOverwrite(yes, os.Stderr); err != nil {
			return err
		}
		h := realHost()
		if err := preflight(h, foreground); err != nil {
			return err
		}
		if insecure(url) {
			fmt.Fprintln(os.Stderr, "주의: 암호화되지 않은 http 주소다. 같은 네트워크에서 토큰을 엿볼 수 있다. 가능하면 Tailscale 주소를 쓴다")
		}
		token, err := exchange(url, args[1])
		if err != nil {
			return err
		}
		c = connection{url, token}
		if err := savePrivate(connectFile(), c); err != nil {
			return err
		}
		if done, err := startService(h, foreground, os.Stdout); err != nil || done {
			return err
		}
	case 0:
		if foreground && registered(realHost()) {
			return errors.New("이미 서비스로 연결돼 있다. 같은 연결을 둘이 돌리지 않는다. 먼저 kyeot disconnect")
		}
		b, err := os.ReadFile(connectFile())
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("저장된 연결이 없다. 앱의 「연결 방법 보기」에서 코드를 받아 `kyeot connect <서버 주소> <코드>`")
		}
		if err != nil || json.Unmarshal(b, &c) != nil || c.URL == "" || c.Token == "" {
			return fmt.Errorf("%s 를 읽지 못했다. 앱에서 새 코드를 받아 다시 연결한다", connectFile())
		}
	default:
		return errors.New("쓰는 법: kyeot connect [--foreground] [--yes] <서버 주소> <6자리 코드>, 끊기는 kyeot disconnect")
	}

	cfg := hermesbridge.FromEnv()
	if cfg.HermesKey == "" {
		return errors.New("이 컴퓨터에서 Hermes 를 찾지 못했다 (API_SERVER_KEY 가 환경변수나 ~/.hermes/.env 에 없다)")
	}
	cfg.DeckURL, cfg.AgentToken = c.URL, c.Token
	if f := connectFile(); f != "" && !loopbackURL(c.URL) { // same computer: the server already measures
		cfg.SensorDir = sensorDir(filepath.Dir(f))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Printf("곁 서버에 붙는다: %s (끄려면 Ctrl+C)\n", redact(c.URL))
	installSkill(cfg)
	if err := hermesbridge.New(cfg).Run(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

var (
	promptIn        io.Reader = os.Stdin
	stdinIsTerminal           = func() bool {
		fi, err := os.Stdin.Stat()
		return err == nil && fi.Mode()&os.ModeCharDevice != 0
	}
)

// confirmOverwrite asks whether to overwrite a saved connection. If not a terminal, it only warns.
func confirmOverwrite(yes bool, out io.Writer) error {
	var old connection
	b, err := os.ReadFile(connectFile())
	if yes || err != nil || json.Unmarshal(b, &old) != nil || old.URL == "" {
		return nil
	}
	if !stdinIsTerminal() {
		fmt.Fprintf(out, "주의: %s 에 붙어 있던 연결을 덮어쓴다\n", redact(old.URL))
		return nil
	}
	fmt.Fprintf(out, "이미 %s 에 연결돼 있다. 덮어쓸까? [y/N] ", redact(old.URL))
	line, _ := bufio.NewReader(promptIn).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(line)); a == "y" || a == "yes" {
		return nil
	}
	return errors.New("기존 연결을 그대로 둔다. 덮어쓰려면 --yes")
}

func exchange(url, code string) (string, error) {
	body, _ := json.Marshal(map[string]string{"code": strings.TrimSpace(code)})
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodPost, url+"/pair/agent", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("서버 주소를 읽지 못했다 (%s): %w", redact(url), err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kyeot-Pair", "1")
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("서버에 닿지 않는다 (%s): %w%s", redact(url), err, reachHint(url))
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	var out struct{ Token, Error string }
	json.Unmarshal(raw, &out)
	if res.StatusCode != http.StatusOK || out.Token == "" {
		if out.Error == "" {
			out.Error = res.Status
		}
		return "", errors.New(out.Error)
	}
	return out.Token, nil
}

// savePrivate writes to a randomly named temp file (0600) in the same folder, then swaps it in.
func savePrivate(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".connect-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// reachHint appends what to check when the server is unreachable, by kind of address.
func reachHint(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	ip := net.ParseIP(h)
	switch {
	case strings.HasSuffix(h, ".ts.net") || pair.IsTailscale(ip):
		return ". 이 컴퓨터도 같은 Tailscale 계정에 로그인돼 있는지 확인한다"
	case strings.HasSuffix(h, ".local") || (ip != nil && (ip.IsPrivate() || ip.IsLinkLocalUnicast())):
		return ". 이 컴퓨터가 서버와 같은 와이파이(네트워크)에 있는지 확인한다"
	}
	return ""
}

// insecure reports whether the address would carry the token in plaintext. This computer and Tailscale (already encrypted) are fine.
func insecure(raw string) bool {
	u, err := neturl.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return false
	}
	h := u.Hostname()
	if h == "localhost" || strings.HasSuffix(h, ".ts.net") {
		return false
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return true
	}
	return !ip.IsLoopback() && !pair.IsTailscale(ip)
}

// plainToInternet reports whether plain http is used for an internet-reachable address. Same rule as server start and kyeot pair.
func plainToInternet(raw string) bool {
	u, err := neturl.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return false
	}
	h := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if ip := net.ParseIP(h); ip != nil {
		return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !pair.IsTailscale(ip)
	}
	return h != "localhost" && strings.Contains(h, ".") && !strings.HasSuffix(h, ".local") && !strings.HasSuffix(h, ".ts.net")
}

func plainAllowed() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("KYEOT_ALLOW_PLAIN"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// loopbackURL reports whether the server address is this computer itself.
func loopbackURL(raw string) bool {
	u, err := neturl.Parse(raw)
	if err != nil {
		return false
	}
	h := u.Hostname()
	ip := net.ParseIP(h)
	return h == "localhost" || (ip != nil && ip.IsLoopback())
}
