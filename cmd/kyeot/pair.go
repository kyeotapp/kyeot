package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/kyeotapp/kyeot/internal/pair"
)

// runPair draws the connection QR for the phone app in the terminal.
func runPair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	host := fs.String("host", "", tr("폰이 닿을 주소. 이름(x.example.com)이면 scheme 과 포트를 붙이고, 전체 주소(https://x.example.com)면 그대로 쓴다. 비우면 KYEOT_PUBLIC_URL, Tailscale, 사설망 순"))
	copyToken := fs.Bool("copy", false, tr("QR 대신 소유자 토큰을 클립보드에 복사한다 (맥 pbcopy. 안 되면 화면의 토큰을 쓴다)"))
	rotate := fs.Bool("rotate", false, tr("소유자 토큰을 새로 만든다. 옛 토큰으로 연결한 폰은 새 QR 을 찍어야 한다"))
	rotateUploads := fs.Bool("rotate-uploads", false, tr("첨부 서명 키를 새로 만든다. 이미 나간 첨부 주소가 모두 무효가 된다"))
	yes := fs.Bool("yes", false, tr("--rotate-uploads 를 묻지 않고 한다"))
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf(tr("모르는 인수 %q"), fs.Arg(0))
	}
	var handedTo string
	if db, err := dbFile(); err == nil {
		info, err := readHostInfo(db)
		if err != nil {
			return err
		}
		handedTo = info.handedTo
	}
	if *rotate || *rotateUploads {
		if err := rotateFlow(*rotate, *rotateUploads, *yes, handedTo, os.Stdin, os.Stdout, restartServerService); err != nil {
			return err
		}
		if !*rotate {
			return nil
		}
		fmt.Println()
		fmt.Println(tr("새 QR:"))
	} else if handedOverNote(os.Stdout, handedTo) {
		return nil
	}
	token, err := readOwnerToken()
	if err != nil {
		return err
	}
	listen := env("ADDR", defaultAddr)
	server, err := pairServer(listen, *host)
	if err != nil {
		return err
	}
	if *copyToken {
		return copyOwnerToken(os.Stdout, token, server, copyToClipboard)
	}
	return showPair(os.Stdout, server, token, listen)
}

// copyToClipboard copies with macOS pbcopy.
func copyToClipboard(text string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// copyOwnerToken is the -copy flow. If copying fails (SSH, Linux), it does not end in an error but tells the user to take it from the screen.
func copyOwnerToken(w io.Writer, token, server string, copyFn func(string) error) error {
	if err := copyFn(token); err != nil {
		fmt.Fprintln(w, tr("클립보드에 복사하지 못했어요. 토큰은 kyeot pair 화면에 있어요."))
		fmt.Fprintln(w, tr("서버 주소:"), server)
		return nil
	}
	fmt.Fprintln(w, tr("소유자 토큰을 클립보드에 복사했어요. 앱 「직접 입력하기」의 소유자 토큰 칸에 붙여 넣으세요."))
	fmt.Fprintln(w, tr("서버 주소:"), server)
	return nil
}

// pairServer is the server address for the QR. Chosen in the order -host, KYEOT_PUBLIC_URL, Tailscale, private network.
func pairServer(listen, host string) (string, error) {
	if host == "" {
		pu, err := publicURL()
		if err != nil {
			return "", err
		}
		host = pu
	}
	if u, ok, err := fullURL(host); err != nil || ok {
		return u, err
	}
	lh, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", err
	}
	scheme := "http"
	cert := getenv("TLS_CERT")
	if cert != "" {
		scheme = "https" // it must connect by the certificate's domain to pass the check
		if host == "" {
			host = certHost(cert)
		}
	}
	if ip := net.ParseIP(lh); host == "" && ip != nil && !ip.IsUnspecified() && !ip.IsLoopback() {
		host = lh
	}
	if host == "" {
		addrs, err := net.InterfaceAddrs()
		if err != nil {
			return "", err
		}
		hosts := pair.Hosts(addrs)
		if len(hosts) == 0 {
			return "", errors.New("폰이 닿을 주소를 찾지 못했다. kyeot pair -host 로 넘겨라")
		}
		host = hosts[0]
	}
	if cert != "" && !certCovers(cert, host) {
		log.Printf("warning: the certificate does not cover %s, so the phone is blocked by the https check. Pass the certificate's domain with -host", host)
	}
	if err := plainCheck(scheme, host); err != nil {
		return "", err
	}
	return scheme + "://" + net.JoinHostPort(host, port), nil
}

// publicURL is KYEOT_PUBLIC_URL: the full address to put in the QR when behind a proxy or domain. Empty if unset.
func publicURL() (string, error) {
	v := getenv("PUBLIC_URL")
	if v == "" {
		return "", nil
	}
	u, ok, err := fullURL(v)
	if err == nil && !ok {
		err = fmt.Errorf("KYEOT_PUBLIC_URL 은 https://kyeot.example.com 처럼 전체 주소로 준다: %q", v)
	}
	if err != nil {
		return "", err
	}
	return u, nil
}

// fullURL, for an address with a scheme, returns it tidied to scheme://host[:port]. ok=false for a bare name.
func fullURL(v string) (string, bool, error) {
	if !strings.Contains(v, "://") {
		return "", false, nil
	}
	u, err := url.Parse(strings.TrimSpace(v))
	if err != nil || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", false, fmt.Errorf("서버 주소는 https://kyeot.example.com 꼴로 준다(경로 없이): %q", v)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false, fmt.Errorf("서버 주소는 https 나 http 로 시작한다: %q", v)
	}
	if err := plainCheck(scheme, u.Hostname()); err != nil {
		return "", false, err
	}
	if cert := getenv("TLS_CERT"); cert != "" && scheme == "https" && !certCovers(cert, u.Hostname()) {
		log.Printf("warning: the certificate does not cover %s. Fine if a proxy handles https", u.Hostname())
	}
	return scheme + "://" + u.Host, true, nil
}

// plainCheck rejects plain http for an internet-reachable address. Same reason as checkPlain at startup.
func plainCheck(scheme, host string) error {
	if scheme == "https" || localHost(host) || truthy(getenv("ALLOW_PLAIN")) {
		return nil
	}
	return fmt.Errorf("%s 는 인터넷에서 닿는 주소라 http 로는 접속 키가 암호화 없이 오간다. https://%s 처럼 https 주소를 준다(그래도 쓰려면 KYEOT_ALLOW_PLAIN=1)", host, host)
}

// localHost reports whether the address is private, Tailscale, or reachable only on this device. Dotless names, .local and .ts.net count too.
func localHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if a, err := netip.ParseAddr(host); err == nil {
		return localPrefix(netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
	}
	return host == "localhost" || !strings.Contains(host, ".") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".ts.net")
}

// isTerminal reports whether the output is a terminal. Output to a log file, pipe or tee never carries the token text. Tests override it.
var isTerminal = func(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// showPair writes the connection QR and the manual-entry guide. The token appears as-is in the QR and manual-entry lines.
func showPair(w io.Writer, server, token, listen string) error {
	art, err := pair.Terminal(pair.Link(server, token))
	if err != nil {
		return err
	}
	fmt.Fprintln(w)
	fmt.Fprint(w, art)
	fmt.Fprintln(w)
	fmt.Fprintln(w, tr("곁 앱의 「QR 찍기」로 찍으세요. 다시 보려면 kyeot pair"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, tr("직접 입력할 때"))
	fmt.Fprintln(w, tr("  서버 주소    "), server)
	if isTerminal(w) {
		fmt.Fprintln(w, tr("  소유자 토큰  "), token)
	} else {
		fmt.Fprintln(w, tr("  소유자 토큰  "), tr("(터미널이 아니라 가렸어요. 터미널에서 kyeot pair 로 확인하세요)"))
	}
	if lh, port, err := net.SplitHostPort(listen); err == nil {
		if ip := net.ParseIP(lh); ip != nil && ip.IsLoopback() && getenv("PUBLIC_URL") == "" {
			fmt.Fprintf(w, tr("주의: 서버가 이 기기 안(%s)에서만 받고 있어 폰이 닿지 않는다. KYEOT_ADDR=0.0.0.0:%s 로 띄워야 한다.\n"), listen, port)
		}
	}
	fmt.Fprintln(w, tr("위 QR 과 토큰은 이 서버의 주인 키예요. 다른 사람에게 보여 주지 마세요."))
	return nil
}

// handedOverNote, after ownership is handed over (before rotate), says this computer's token is no account at all. True if it said so.
func handedOverNote(w io.Writer, handedTo string) bool {
	if handedTo == "" {
		return false
	}
	fmt.Fprintf(w, tr("주인 권한이 %s에게 넘어가 있어요. 넘긴 뒤라 이 토큰으로는 들어갈 수 없어요. 새로 주인이 되려면 kyeot pair --rotate\n"), handedTo)
	return true
}
