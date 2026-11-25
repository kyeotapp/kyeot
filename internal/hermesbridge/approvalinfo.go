package hermesbridge

import (
	"github.com/kyeotapp/kyeot/internal/protocol"
	"regexp"
	"slices"
	"strings"
)

// Common classifications (pattern_key) from Hermes 0.22.4 tools/approval_detection.py
var kindKo = map[string]string{
	"script execution via -e/-c flag":                    "스크립트를 명령줄에서 바로 실행",
	"script execution via heredoc":                       "스크립트를 명령줄에서 바로 실행",
	"shell execution via heredoc":                        "셸 명령 묶음을 바로 실행",
	"recursive delete":                                   "폴더째 지우기",
	"recursive delete (long flag)":                       "폴더째 지우기",
	"recursive delete (flags after operands)":            "폴더째 지우기",
	"find -delete":                                       "찾은 파일 지우기",
	"find -exec/-execdir rm":                             "찾은 파일 지우기",
	"xargs with rm":                                      "목록의 파일 지우기",
	"force kill processes":                               "프로그램 강제 종료",
	"kill all processes":                                 "프로그램 모두 종료",
	"git reset --hard (destroys uncommitted changes)":    "커밋 안 한 코드 변경 버리기",
	"git clean with force (deletes untracked files)":     "git 이 모르는 파일 지우기",
	"git force push (rewrites remote history)":           "원격 저장소 이력 덮어쓰기",
	"package manager uninstall":                          "설치된 프로그램 지우기",
	"pipe remote content to shell":                       "인터넷에서 받은 명령 바로 실행",
	"overwrite project env/config file":                  "프로젝트 설정 파일 덮어쓰기",
	"overwrite project env/config via redirection":       "프로젝트 설정 파일 덮어쓰기",
	"stop/restart hermes gateway (kills running agents)": "Hermes 다시 시작(진행 중인 일이 끊김)",
	"docker restart/stop/kill (container lifecycle)":     "도커 컨테이너 멈추기나 다시 시작",
	"stop/restart system service":                        "시스템 서비스 멈추기나 다시 시작",
	"world/other-writable permissions":                   "아무나 쓸 수 있게 권한 열기",
	"execute remote content via command substitution":    "인터넷에서 받은 명령 바로 실행",
	"execute remote script via process substitution":     "인터넷에서 받은 명령 바로 실행",
}

var deckCalls = []struct{ path, text string }{
	{"/ui/put", "곁 앱 화면을 만들거나 고쳐요"},
	{"/ui/remove", "곁 앱 화면을 지워요"},
	{"/ui/layout", "곁 앱 탭 순서를 바꿔요"},
	{"/data/put", "곁 앱 화면 데이터를 바꿔요"},
	{"/data/remove", "곁 앱 화면 데이터를 지워요"},
}

var (
	writeTarget = regexp.MustCompile(`(?:^|[^>&0-9])>>?\s*([~/][^\s;|&"'<>()]+)`)
	remoteHost  = regexp.MustCompile(`https?://([A-Za-z0-9.-]+)`)
)

// approvalContext is the card's top line: the last paragraph the agent wrote this run, else the original request.
func approvalContext(said, ask string) string {
	if p := lastParagraph(said); p != "" {
		return clip(p, 160)
	}
	if ask = clip(firstLine(ask), 80); ask != "" {
		return "「" + ask + "」 요청을 처리하는 중이에요"
	}
	return ""
}

// approvalImpact names what changes in plain words; only what the command clearly shows.
func approvalImpact(kinds []string, desc, command string) []string {
	var out []string
	add := func(s string) {
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	if len(kinds) == 0 && desc != "" {
		kinds = []string{desc}
	}
	for _, k := range kinds {
		if ko := kindKo[k]; ko != "" {
			add("종류: " + ko + " (" + k + ")")
		} else {
			add("종류: " + k)
		}
	}
	for _, c := range deckCalls {
		if strings.Contains(command, ":7701"+c.path) {
			add(c.text)
		}
	}
	var files []string
	for _, m := range writeTarget.FindAllStringSubmatch(command, -1) {
		if f := m[1]; f != "/dev/null" && !slices.Contains(files, f) {
			files = append(files, f)
		}
	}
	if len(files) > 0 {
		add("파일을 써요: " + strings.Join(files[:min(len(files), 3)], ", "))
	}
	var hosts []string
	for _, m := range remoteHost.FindAllStringSubmatch(command, -1) {
		if h := m[1]; h != "127.0.0.1" && h != "localhost" && !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) > 0 {
		add("인터넷 주소에 접속해요: " + strings.Join(hosts[:min(len(hosts), 3)], ", "))
	}
	return out
}

// Hermes 0.22.4: session is per run (approval_session_key = run_id); always persists in config.yaml command_allowlist
var scopeNotes = map[string]string{
	"session": "이번 답이 끝날 때까지 같은 종류의 명령은 다시 묻지 않아요",
	"always":  "앞으로 모든 대화에서 같은 종류의 명령을 묻지 않아요. Hermes 설정에 남아요",
}

func lastParagraph(text string) string {
	paras := strings.Split(strings.TrimSpace(draftText(text)), "\n\n")
	return strings.Join(strings.Fields(paras[len(paras)-1]), " ")
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// askOf is the first line of the user's words for the approval card "request"; the note line of an edited message is dropped.
func askOf(text string) string {
	return clip(firstLine(strings.TrimPrefix(text, protocol.EditNote)), 200)
}
