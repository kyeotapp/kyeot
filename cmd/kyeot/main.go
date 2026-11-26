package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kyeotapp/kyeot/internal/push"
	"github.com/kyeotapp/kyeot/internal/server"
)

// getenv reads KYEOT_<name> first, then the old name AGENT_DECK_<name>.
func getenv(name string) string {
	if v := os.Getenv("KYEOT_" + name); v != "" {
		return v
	}
	return os.Getenv("AGENT_DECK_" + name)
}

// splitList splits a comma list with no surrounding spaces and no empty entries.
func splitList(v string) []string {
	var out []string
	for _, o := range strings.Split(v, ",") {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, o)
		}
	}
	return out
}

func env(name, fallback string) string {
	if v := getenv(name); v != "" {
		return v
	}
	return fallback
}

// phones must reach it, so it listens everywhere. Accepted networks are filtered by defaultNets
const defaultAddr = "0.0.0.0:7700"

// the release build injects it with -ldflags "-X main.version=..."
var version = "dev"

// source address (AGPL section 13). stdout gets only the version line
const sourceURL = "https://github.com/kyeotapp/kyeot"

func usage(w io.Writer) {
	fmt.Fprint(w, tr(`쓰는 법: kyeot [명령]

  (명령 없음)                     서버를 띄운다
  service install|uninstall|restart|status
                                  로그인 때 저절로 뜨는 서비스로 등록, 해제, 재시작, 상태 확인
  pair [-host 주소] [-copy]       폰 앱이 찍을 연결 QR 을 그린다. 주소는 이름이나 https://… 전체
  pair --rotate                   소유자 토큰을 새로 만들고 새 QR 을 그린다. 서버 서비스는 다시 켠다
  pair --rotate-uploads [--yes]   첨부 서명 키를 새로 만든다. 이미 나간 첨부 주소는 무효가 된다
  connect <서버 주소> <코드>      이 컴퓨터의 Hermes 를 다른 컴퓨터의 서버에 붙인다
  disconnect                      connect 로 붙인 것을 뗀다
  uninstall [--purge]             서비스를 내리고 실행 파일을 지운다. --purge 면 키와 대화 기록까지
  backup <파일>                   대화 기록, 첨부, 키를 파일 하나로 백업한다. 서버가 켜져 있어도 된다
  restore [--yes] <파일>          백업으로 되살린다. 서버를 끄고 한다. 지금 데이터는 먼저 백업해 둔다
  update [--check]                최신 릴리스로 올리고 서비스를 다시 켠다. --check 는 확인만
  version                         판을 출력한다
  help                            이 도움말
`))
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version)
		fmt.Fprintln(os.Stderr, "source:", sourceURL)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "connect" {
		if err := runConnect(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "connect:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "disconnect" {
		if err := runDisconnect(realHost(), os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "disconnect:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "service" {
		if err := runService(serverHost(), os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "service:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "pair" {
		if err := runPair(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "pair:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && (os.Args[1] == "backup" || os.Args[1] == "restore") {
		p, err := realBackupPlan()
		if err == nil {
			if os.Args[1] == "backup" {
				err = runBackup(p, os.Args[2:], os.Stdout)
			} else {
				err = runRestore(p, os.Args[2:], os.Stdin, os.Stdout)
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, os.Args[1]+":", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "update" {
		if err := runUpdate(realUpdatePlan(), os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "update:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "uninstall" {
		if err := runUninstall(realUninstallPlan(), os.Args[2:], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "uninstall:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 {
		if a := os.Args[1]; a == "help" || a == "-h" || a == "--help" || a == "-help" {
			usage(os.Stdout)
			return
		}
		fmt.Fprintf(os.Stderr, tr("모르는 명령: %s\n\n"), os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
	token, created, err := ownerToken()
	if err != nil {
		log.Fatal(err)
	}
	dbPath, err := dbFile()
	if err != nil {
		log.Fatal(err)
	}
	if release, err := waitLockData(dbPath, 15*time.Second); errors.Is(err, errDataBusy) {
		log.Fatalf(tr("다른 곁 서버나 kyeot restore 가 이 데이터를 쓰고 있다: %s"), dbPath)
	} else if err != nil {
		log.Printf("could not create the data lock file, starting without a lock: %v", err)
	} else {
		defer release()
	}
	upKey, upCreated, err := uploadKey(token)
	if err != nil {
		log.Fatal(err)
	}
	if upCreated {
		log.Print("created the attachment signing key file. Existing attachment URLs keep working, and changing the owner token no longer breaks them")
	}

	origins := splitList(getenv("ALLOWED_ORIGINS"))
	nets, err := serverNets()
	if err != nil {
		log.Fatal(err)
	}
	cert, key, err := tlsFiles()
	if err != nil {
		log.Fatal(err)
	}
	if err := checkPlain(env("NETS", defaultNets), cert != "", truthy(getenv("ALLOW_PLAIN"))); err != nil {
		log.Fatal(err)
	}
	if _, err := publicURL(); err != nil {
		log.Fatal(err)
	}
	ping, err := time.ParseDuration(env("PING_INTERVAL", "30s"))
	if err != nil {
		log.Fatalf("KYEOT_PING_INTERVAL: %v", err)
	}
	addr := env("ADDR", defaultAddr)
	front, err := net.Listen("tcp", addr) // claim the port before touching the DB. If a server already runs, this exits without overwriting its agent state
	if err != nil {
		log.Fatal(err)
	}
	proxyMark, err := proxyMarkFile()
	if err != nil {
		log.Fatal(err)
	}
	tokenFile, _ := ownerTokenFile() // "" for an environment-variable token, so the app cannot hand over ownership
	totalMB, err := strconv.ParseInt(env("UPLOAD_TOTAL_MB", "0"), 10, 64)
	if err != nil || totalMB < 0 || totalMB > 1<<23 { // upper bound 8TB
		log.Fatalf("KYEOT_UPLOAD_TOTAL_MB: must be an integer (MB) from 0 to 8388608: %q", getenv("UPLOAD_TOTAL_MB"))
	}
	srv, err := server.New(server.Options{UploadTotalQuota: totalMB << 20, DBPath: dbPath, OwnerToken: token, OwnerTokenFile: tokenFile, UploadKey: upKey, AllowedOrigins: origins, AllowNets: nets, WhisperModel: getenv("WHISPER_MODEL"), Push: apnsSender(), Version: version, PingInterval: ping, SensorDir: sensorDir(filepath.Dir(dbPath)), ProxyMarkFile: proxyMark, AccessLog: accessLogOut(), LatestURL: latestURL()})
	if err != nil {
		log.Fatal(err)
	}
	if created {
		firstRun(token, addr)
	}
	httpSrv := newHTTPServer(addr, srv.Handler())
	if cert != "" {
		rl, err := newCertReloader(cert, key)
		if err != nil {
			log.Fatal(err)
		}
		httpSrv.TLSConfig = &tls.Config{GetCertificate: rl.get}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	hermesAddr := addr
	var inside *http.Server
	if cert != "" {
		// the certificate is bound to a domain, so the same-computer adapter cannot use https on 127.0.0.1. Open a separate plain-text entrance reachable only on this device
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			log.Fatal(err)
		}
		inside = newHTTPServer("", srv.Handler())
		go func() {
			if err := inside.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				log.Printf("inner entrance stopped: %v", err)
			}
		}()
		hermesAddr = ln.Addr().String()
	}
	go func() {
		var err error
		if cert != "" {
			log.Printf("kyeot server %s: https://%s", version, front.Addr())
			err = httpSrv.ServeTLS(front, "", "")
		} else {
			log.Printf("kyeot server %s: http://%s", version, front.Addr())
			err = httpSrv.Serve(front)
		}
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	hermesDone := make(chan struct{})
	go func() {
		defer close(hermesDone)
		startHermes(srv, hermesAddr, filepath.Dir(dbPath))
	}()
	<-ctx.Done()

	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdown); err != nil {
		log.Printf("could not wait for in-flight requests to finish: %v", err)
	}
	if inside != nil {
		inside.Shutdown(shutdown)
	}
	select {
	case <-hermesDone:
	case <-shutdown.Done():
	}
	if err := srv.Close(); err != nil {
		log.Print(err)
	}
}

// firstRun shows the connection QR when the token was just created. If not a terminal (log file), no QR is written.
func firstRun(token, addr string) {
	log.Print("first run: created the owner token. Connect the app with kyeot pair")
	if fi, err := os.Stdout.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return
	}
	server, err := pairServer(addr, "")
	if err == nil {
		err = showPair(os.Stdout, server, token, addr)
	}
	if err != nil {
		log.Printf("could not draw the connection QR: %v", err)
	}
}

// apnsSender builds the APNs sender only when KYEOT_APNS_KEY, _KEY_ID and _TEAM_ID are all set. Otherwise nil (push off).
func apnsSender() push.Sender {
	keyPath, keyID, teamID := getenv("APNS_KEY"), getenv("APNS_KEY_ID"), getenv("APNS_TEAM_ID")
	if keyPath == "" || keyID == "" || teamID == "" {
		if keyPath != "" || keyID != "" || teamID != "" {
			log.Printf("push off: KYEOT_APNS_KEY, _KEY_ID and _TEAM_ID must all be set (now KEY=%t KEY_ID=%t TEAM_ID=%t)", keyPath != "", keyID != "", teamID != "")
		}
		return nil
	}
	pem, err := os.ReadFile(keyPath)
	if err != nil {
		log.Printf("push off: could not read the APNs key file (%s)", keyPath)
		return nil
	}
	topic := env("APNS_TOPIC", "app.kyeot")
	a, err := push.NewAPNs(push.Config{KeyPEM: pem, KeyID: keyID, TeamID: teamID, Topic: topic})
	if err != nil {
		log.Printf("push off: %v", err)
		return nil
	}
	log.Printf("push on: APNs (topic %s)", topic)
	return a
}

const (
	readTimeout  = 2 * time.Minute
	writeTimeout = 2 * time.Minute
	idleTimeout  = 2 * time.Minute
	longTimeout  = 15 * time.Minute // a 25MB upload at 30KB/s is fine
)

func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: deadlines(h), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: readTimeout, WriteTimeout: writeTimeout, IdleTimeout: idleTimeout}
}

// deadlines extends the time limits for file transfers and dictation. For websockets, net/http lifts the limit when hijacking.
func deadlines(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.URL.Path; strings.HasPrefix(p, "/api/uploads") || p == "/api/transcribe" || strings.HasPrefix(p, "/updates/files/") {
			rc := http.NewResponseController(w)
			t := time.Now().Add(longTimeout)
			rc.SetReadDeadline(t)
			rc.SetWriteDeadline(t)
		}
		h.ServeHTTP(w, r)
	})
}
