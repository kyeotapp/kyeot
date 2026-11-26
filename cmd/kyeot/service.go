package main

import (
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kyeotapp/kyeot/internal/hermesbridge"
)

// serviceSpec is the kind of command to run as a service: the agent connection or the server.
type serviceSpec struct {
	label, unit, desc, logName string
	args                       []string // arguments after the executable
	always                     bool     // restart even after a clean exit (server)
	env                        []string // non-secret environment variables passed to the service
	done, off                  string
}

var connectSpec = serviceSpec{
	label: "app.kyeot.connect", unit: "kyeot-connect", desc: "kyeot connect (Hermes)", logName: "kyeot-connect.log",
	args: []string{"connect"},
	env:  []string{"BRIDGE_UI_ADDR", "HERMES_HOME", "HERMES_URL", "KYEOT_CONNECT_FILE"},
	done: trLater("연결했다. 백그라운드 서비스로 돌기 때문에 터미널을 닫거나 재부팅해도 계속 연결된다"), off: "kyeot disconnect",
}

var serverSpec = serviceSpec{
	label: "app.kyeot.server", unit: "kyeot-server", desc: "kyeot server", logName: "kyeot-server.log",
	always: true,
	env: []string{"BRIDGE_UI_ADDR", "HERMES_HOME", "HERMES_URL", "KYEOT_ACCESS_LOG", "KYEOT_ADDR", "KYEOT_ALLOW_PLAIN", "KYEOT_ALLOWED_ORIGINS", "KYEOT_APNS_KEY", "KYEOT_APNS_KEY_ID", "KYEOT_APNS_TEAM_ID", "KYEOT_APNS_TOPIC",
		"KYEOT_DB", "KYEOT_HERMES", "KYEOT_NETS", "KYEOT_PUBLIC_URL", "KYEOT_TLS_CERT", "KYEOT_TLS_KEY", "KYEOT_UPDATE_CHECK", "KYEOT_UPLOAD_TOTAL_MB", "KYEOT_WHISPER_MODEL", "XDG_CONFIG_HOME"}, // the owner token lives in a file, so it is not passed
	done: trLater("서버를 켰다. 백그라운드 서비스로 돌기 때문에 터미널을 닫거나 재부팅해도 계속 뜬다"), off: "kyeot service uninstall",
}

// host is this computer's values and command runner used for service registration. Tests pass a fake.
type host struct {
	goos, exe, home, path string
	uid                   int
	tmp                   []string
	env                   map[string]string // values passed to the service
	hermesHome            string
	shellKey, fileKey     bool // where API_SERVER_KEY is
	run                   func(name string, args ...string) error
	wait                  time.Duration                    // start retry interval
	spec                  serviceSpec                      // empty means the agent connection
	ensureToken           func() (created bool, err error) // server service: creates the owner key in advance
	proxyMark             func() string                    // server service: record of a same-PC proxy detection ("time header"), empty if none
}

func (h host) sp() serviceSpec {
	if h.spec.label == "" {
		return connectSpec
	}
	return h.spec
}

func realHost() host { return realHostFor(connectSpec) }

func realHostFor(spec serviceSpec) host {
	h := host{goos: runtime.GOOS, path: cleanPath(os.Getenv("PATH")), uid: os.Getuid(), tmp: []string{os.TempDir(), "/tmp"}, wait: time.Second, env: map[string]string{}, spec: spec}
	if exe, err := os.Executable(); err == nil {
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			exe = r
		}
		h.exe = stableExe(exe, func(p string) bool { _, err := os.Stat(p); return err == nil })
	}
	h.home, _ = os.UserHomeDir()
	for _, k := range spec.env {
		if v, ok := os.LookupEnv(k); ok {
			h.env[k] = v
		}
	}
	h.hermesHome = hermesbridge.FromEnv().HermesHome
	h.shellKey = os.Getenv("API_SERVER_KEY") != ""
	h.fileKey = h.hermesHome != "" && hermesbridge.FileKey(h.hermesHome) != ""
	h.run = func(name string, args ...string) error {
		out, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s %s: %v %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	return h
}

// stableExe turns a brew versioned path <prefix>/Cellar/<name>/<version>/… into <prefix>/opt/<name>/…. Unchanged otherwise.
func stableExe(path string, exists func(string) bool) string {
	i := strings.Index(path, "/Cellar/")
	if i < 0 {
		return path
	}
	parts := strings.Split(path[i+len("/Cellar/"):], "/")
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" {
		return path
	}
	cand := path[:i] + "/opt/" + parts[0] + "/" + strings.Join(parts[2:], "/")
	if exists(cand) {
		return cand
	}
	return path
}

// cleanPath removes empty entries and relative paths from PATH.
func cleanPath(p string) string {
	var keep []string
	for _, d := range strings.Split(p, ":") {
		if strings.HasPrefix(d, "/") {
			keep = append(keep, d)
		}
	}
	return strings.Join(keep, ":")
}

// envPairs appends the values to pass after PATH, sorted by name.
func envPairs(h host) [][2]string {
	pairs := [][2]string{{"PATH", h.path}}
	keys := make([]string, 0, len(h.env))
	for k := range h.env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		pairs = append(pairs, [2]string{k, h.env[k]})
	}
	return pairs
}

// service is the registration file and the commands to run around it. stop and cleanup may fail without stopping the flow.
type service struct {
	file, content, log, note string
	logDir                   string
	stop, start, cleanup     [][]string
	alt                      []string // once, if the last start fails
	altDone                  string
}

// serviceFor builds the registration for each operating system. false if unsupported.
func serviceFor(h host) (service, bool) {
	switch h.goos {
	case "darwin":
		return launchdService(h), true
	case "linux":
		return systemdService(h), true
	}
	return service{}, false
}

func launchdService(h host) service {
	sp := h.sp()
	plist := filepath.Join(h.home, "Library", "LaunchAgents", sp.label+".plist")
	logFile := filepath.Join(h.home, "Library", "Logs", sp.logName)
	x := html.EscapeString
	var env strings.Builder
	for _, p := range envPairs(h) {
		env.WriteString("\t\t<key>" + x(p[0]) + "</key>\n\t\t<string>" + x(p[1]) + "</string>\n")
	}
	var args strings.Builder
	for _, a := range sp.args {
		args.WriteString("\t\t<string>" + x(a) + "</string>\n")
	}
	keep := "\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>\n"
	if sp.always {
		keep = "\t<true/>\n"
	}
	content := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + sp.label + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + x(h.exe) + `</string>
` + args.String() + `	</array>
	<key>EnvironmentVariables</key>
	<dict>
` + env.String() + `	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
` + keep + `	<key>ThrottleInterval</key>
	<integer>30</integer>
	<key>StandardOutPath</key>
	<string>` + x(logFile) + `</string>
	<key>StandardErrorPath</key>
	<string>` + x(logFile) + `</string>
</dict>
</plist>
`
	gui, user := "gui/"+strconv.Itoa(h.uid), "user/"+strconv.Itoa(h.uid)
	return service{
		file: plist, content: content, log: logFile, logDir: filepath.Dir(logFile),
		stop:    [][]string{{"launchctl", "bootout", gui + "/" + sp.label}, {"launchctl", "bootout", user + "/" + sp.label}},
		start:   [][]string{{"launchctl", "bootstrap", gui, plist}},
		alt:     []string{"launchctl", "bootstrap", user, plist},
		altDone: trLater("화면 로그인 세션이 없어 사용자 도메인(user)에 등록했다. 터미널을 닫아도 돌지만, 재부팅 뒤에는 이 계정으로 로그인해야 다시 켜질 수 있다"),
	}
}

func systemdService(h host) service {
	sp := h.sp()
	name := sp.unit + ".service"
	unit := filepath.Join(h.home, ".config", "systemd", "user", name)
	q := func(s string) string { // systemd quoting and % specifiers
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		return `"` + strings.ReplaceAll(s, "%", "%%") + `"`
	}
	var env strings.Builder
	for _, p := range envPairs(h) {
		env.WriteString("Environment=" + q(p[0]+"="+p[1]) + "\n")
	}
	restart := "on-failure"
	if sp.always {
		restart = "always"
	}
	exec := strings.ReplaceAll(q(h.exe), "$", "$$")
	for _, a := range sp.args {
		exec += " " + a
	}
	content := `[Unit]
Description=` + sp.desc + `

[Service]
ExecStart=` + exec + `
` + env.String() + `Restart=` + restart + `
RestartSec=30

[Install]
WantedBy=default.target
`
	return service{
		file: unit, content: content, log: "journalctl --user -u " + sp.unit,
		note:    trLater("로그인하지 않은 동안에도 돌게 하려면 `loginctl enable-linger $USER` 를 한 번 실행한다(관리자 권한이 필요할 수 있다)"),
		stop:    [][]string{{"systemctl", "--user", "disable", "--now", name}},
		start:   [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", "--now", name}},
		cleanup: [][]string{{"systemctl", "--user", "daemon-reload"}},
	}
}

// inTemp reports whether the executable ran from go run or a temp folder.
func inTemp(exe string, tmp []string) bool {
	if strings.Contains(exe, "go-build") {
		return true
	}
	for _, d := range tmp {
		if d == "" {
			continue
		}
		if r, err := filepath.EvalSymlinks(d); err == nil {
			d = r
		}
		if rel, err := filepath.Rel(d, exe); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

// plan builds the service to register. skip is a reason to run in the foreground, err a reason to refuse.
func plan(h host) (s service, skip string, err error) {
	s, ok := serviceFor(h)
	switch {
	case !ok:
		return s, tr("이 운영체제는 자동 등록을 지원하지 않는다. 터미널을 열어 둬야 연결이 유지된다"), nil
	case h.exe == "" || h.home == "":
		return s, tr("실행 파일이나 홈 폴더를 찾지 못해 등록하지 않는다. 터미널을 열어 둬야 연결이 유지된다"), nil
	case inTemp(h.exe, h.tmp):
		return s, fmt.Sprintf(tr("주의: 실행 파일이 임시 폴더에 있다(%s). 지워질 수 있어 등록하지 않고 여기서 돌린다. 설치한 kyeot 로 다시 연결한다"), h.exe), nil
	case h.uid == 0:
		return s, "", errors.New(tr("관리자 권한(sudo) 없이 실행한다. root 로는 서비스를 등록하지 않는다"))
	case hasControl(h.exe):
		return s, "", errors.New(tr("실행 파일 경로에 개행이나 제어문자가 있어 서비스로 등록하지 않는다"))
	}
	for _, p := range envPairs(h) {
		if hasControl(p[1]) {
			return s, "", fmt.Errorf(tr("%s 에 개행이나 제어문자가 있어 서비스로 등록하지 않는다"), p[0])
		}
	}
	return s, "", nil
}

// preflight checks the whole path can succeed before spending the code (token exchange).
// registered reports whether a connection service is registered on this computer.
func registered(h host) bool {
	s, ok := serviceFor(h)
	if !ok || h.home == "" {
		return false
	}
	_, err := os.Stat(s.file)
	return err == nil
}

func preflight(h host, foreground bool) error {
	if foreground && registered(h) {
		return errors.New("이미 서비스로 연결돼 있다. 먼저 kyeot disconnect")
	}
	if !h.shellKey && !h.fileKey {
		return fmt.Errorf("이 컴퓨터에서 Hermes 를 찾지 못했다 (API_SERVER_KEY 가 환경변수나 %s/.env 에 없다)", h.hermesHome)
	}
	if foreground {
		return nil
	}
	if _, skip, err := plan(h); err != nil || skip != "" {
		return err
	}
	if !h.fileKey {
		return fmt.Errorf("서비스는 셸 변수를 못 본다. API_SERVER_KEY 를 %s/.env 에 넣고 다시 연결하거나 --foreground 로 실행한다", h.hermesHome)
	}
	return nil
}

// startService registers and starts the saved connection as a background service. false means run in the foreground.
func startService(h host, foreground bool, out io.Writer) (bool, error) {
	if foreground {
		return false, nil
	}
	s, skip, err := plan(h)
	if err != nil {
		return false, err
	}
	if skip != "" {
		fmt.Fprintln(out, skip)
		return false, nil
	}
	for _, c := range s.stop {
		h.run(c[0], c[1:]...)
	}
	if s.logDir != "" {
		if err := os.MkdirAll(s.logDir, 0o755); err != nil {
			return false, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.file), 0o755); err != nil {
		return false, err
	}
	_, statErr := os.Stat(s.file)
	existed := statErr == nil
	if err := os.WriteFile(s.file, []byte(s.content), 0o644); err != nil {
		return false, err
	}
	viaAlt := false
	for i, c := range s.start {
		var err error
		for try := 0; try < 3; try++ { // if the old process has not fully gone down, bootstrap fails briefly
			if err = h.run(c[0], c[1:]...); err == nil {
				break
			}
			time.Sleep(h.wait)
		}
		if err != nil && i == len(s.start)-1 && s.alt != nil {
			if altErr := h.run(s.alt[0], s.alt[1:]...); altErr == nil {
				err, viaAlt = nil, true
			} else {
				err = fmt.Errorf("%w; %v", err, altErr)
			}
		}
		if err != nil {
			if !existed {
				os.Remove(s.file)
				return false, fmt.Errorf(tr("서비스를 켜지 못했다: %w"), err)
			}
			return false, fmt.Errorf(tr("서비스를 다시 켜지 못했다: %w\n등록 파일은 남겼다(%s). 다음 로그인 때 저절로 뜬다. 지금 켜려면 잠시 뒤 다시: kyeot service restart"), err, s.file)
		}
	}
	if viaAlt {
		fmt.Fprintln(out, tr(s.altDone))
	} else {
		fmt.Fprintln(out, tr(h.sp().done))
	}
	fmt.Fprintln(out, tr("끄려면:"), h.sp().off)
	fmt.Fprintln(out, tr("기록:"), s.log)
	if s.note != "" {
		fmt.Fprintln(out, tr(s.note))
	}
	return true, nil
}

// restartKeepingEnv re-registers and restarts a registered service. Settings saved in the
// existing service file are kept; values in the current shell override them.
func restartKeepingEnv(h host, out io.Writer) (bool, error) {
	h = withSavedEnv(h)
	return startService(h, false, out)
}

// withSavedEnv returns h with the allowed variables from its service file as the base of h.env.
func withSavedEnv(h host) host {
	s, ok := serviceFor(h)
	if !ok || h.home == "" {
		return h
	}
	b, err := os.ReadFile(s.file)
	if err != nil {
		return h
	}
	merged := map[string]string{}
	for k, v := range savedEnv(h.goos, string(b)) {
		if k != "PATH" && slices.Contains(h.sp().env, k) {
			merged[k] = v
		}
	}
	for k, v := range h.env {
		merged[k] = v
	}
	h.env = merged
	return h
}

var plistEnvRe = regexp.MustCompile(`(?s)<key>EnvironmentVariables</key>\s*<dict>(.*?)</dict>`)
var plistPairRe = regexp.MustCompile(`(?s)<key>([^<]*)</key>\s*<string>([^<]*)</string>`)

// savedEnv reads the environment written by launchdService or systemdService.
func savedEnv(goos, content string) map[string]string {
	env := map[string]string{}
	switch goos {
	case "darwin":
		m := plistEnvRe.FindStringSubmatch(content)
		if m == nil {
			return env
		}
		for _, p := range plistPairRe.FindAllStringSubmatch(m[1], -1) {
			env[html.UnescapeString(p[1])] = html.UnescapeString(p[2])
		}
	case "linux":
		for _, line := range strings.Split(content, "\n") {
			v, ok := strings.CutPrefix(line, "Environment=")
			if !ok {
				continue
			}
			if k, val, ok := strings.Cut(unquoteSystemd(v), "="); ok {
				env[k] = val
			}
		}
	}
	return env
}

// unquoteSystemd undoes the quoting in systemdService: "…", \\ → \, \" → ", %% → %.
func unquoteSystemd(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	s = s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s):
			i++
			b.WriteByte(s[i])
		case s[i] == '%' && i+1 < len(s) && s[i+1] == '%':
			i++
			b.WriteByte('%')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// removeService stops the service and deletes the registration file. false if none is registered.
func removeService(h host, out io.Writer) (bool, error) {
	s, ok := serviceFor(h)
	if !ok || h.home == "" {
		return false, nil
	}
	if _, err := os.Stat(s.file); err != nil {
		return false, nil
	}
	var errs []string
	for _, c := range s.stop {
		if err := h.run(c[0], c[1:]...); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if err := os.Remove(s.file); err != nil {
		return false, err
	}
	for _, c := range s.cleanup {
		h.run(c[0], c[1:]...)
	}
	if len(errs) == len(s.stop) {
		fmt.Fprintf(out, tr("서비스를 멈추지 못했다: %s. 로그아웃하거나 재부팅하면 멈춘다\n"), strings.Join(errs, "; "))
		fmt.Fprintln(out, tr("등록 파일은 지웠다:"), s.file)
	} else {
		fmt.Fprintln(out, tr("서비스를 멈추고 지웠다:"), s.file)
	}
	return true, nil
}

// runDisconnect stops the service and deletes the registration file and saved connection. Missing ones are skipped.
func runDisconnect(h host, out io.Writer) error {
	removed, err := removeService(h, out)
	if err != nil {
		return err
	}
	if f := connectFile(); f != "" {
		err := os.Remove(f)
		if err == nil {
			fmt.Fprintln(out, "저장된 연결을 지웠다:", f)
			removed = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if !removed {
		fmt.Fprintln(out, "지울 연결이 없다")
		return nil
	}
	fmt.Fprintln(out, "서버 쪽 연결까지 끊으려면 앱에서 이 에이전트를 지운다")
	return nil
}
