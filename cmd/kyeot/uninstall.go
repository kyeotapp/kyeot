package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	deckui "github.com/kyeotapp/kyeot/integrations/hermes/deck-ui"
	"github.com/kyeotapp/kyeot/internal/hermesbridge"
	"github.com/kyeotapp/kyeot/internal/store"
)

// uninstallPlan is what to delete. Tests pass a temp folder.
type uninstallPlan struct {
	server, agent host
	connectFile   string
	dataDirs      []string // only with --purge: owner key, conversation history, connection info
	logFiles      []string // only with --purge
	dbPath        string   // to check for other users. If empty, it does not ask
	tokenFiles    []string // owner token deleted with --purge=mine
	hermesHomes   []string // Hermes folders (default and profiles) where we may have installed the deck-ui skill
	interactive   func() bool
}

func realUninstallPlan() uninstallPlan {
	p := uninstallPlan{server: serverHost(), agent: realHost(), connectFile: connectFile(), interactive: func() bool { return stdinIsTerminal() }}
	if dir, err := configDir(); err == nil {
		p.dataDirs = append(p.dataDirs, dir)
		p.dbPath = filepath.Join(dir, "kyeot.db")
	}
	if db := getenv("DB"); db != "" {
		p.dbPath = db
	}
	p.tokenFiles, _ = tokenFiles()
	for _, h := range hermesbridge.Profiles() {
		p.hermesHomes = append(p.hermesHomes, h.Config.HermesHome)
	}
	if home := p.server.home; home != "" {
		for _, old := range []string{"kyeot", "agent-deck"} {
			p.dataDirs = append(p.dataDirs, filepath.Join(home, ".config", old))
		}
		if p.server.goos == "darwin" {
			for _, sp := range []serviceSpec{serverSpec, connectSpec} {
				p.logFiles = append(p.logFiles, filepath.Join(home, "Library", "Logs", sp.logName))
			}
		}
	}
	return p
}

// runUninstall stops the service and deletes the executable the install script laid down. With --purge it also deletes keys and history.
// It prints everything deleted and everything kept.
func runUninstall(p uninstallPlan, args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(out)
	var purge purgeFlag
	fs.Var(&purge, "purge", "소유자 키, 대화 기록, 로그까지 지운다. 다른 사용자가 있으면 =all(모두) 또는 =mine(내 것만)")
	yes := fs.Bool("yes", false, "--purge 를 묻지 않고 한다")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("모르는 인수 %q. 쓰는 법: kyeot uninstall [--purge[=all|mine] [--yes]]", fs.Arg(0))
	}

	var data []string
	var info hostInfo
	mode := purge.mode
	if mode != "" {
		var err error
		if info, err = readHostInfo(p.dbPath); err != nil {
			return fmt.Errorf("대화 기록을 읽지 못해 멈췄어요. 아무것도 지우지 않았어요: %w", err)
		}
	}
	if mode == "ask" && len(info.others) > 0 {
		if p.interactive == nil || !p.interactive() {
			fmt.Fprintf(out, "멈췄어요: 다른 사용자 %d명이 있는데 물어볼 수 없어요.\n고르는 값을 붙여 다시 실행하세요.\n", len(info.others))
			fmt.Fprintln(out, "  kyeot uninstall --purge=all    모두 지우기")
			fmt.Fprintln(out, "  kyeot uninstall --purge=mine   내 것만 지우기")
			return errors.New("다른 사용자가 있어 아무것도 지우지 않았다")
		}
		if mode = askPurge(info.others, in, out); mode == "" {
			fmt.Fprintln(out, "그만뒀다. 아무것도 지우지 않았다")
			return nil
		}
	}
	switch mode {
	case "all", "ask":
		data = existing(append(append([]string{}, p.dataDirs...), p.logFiles...))
		if mode == "ask" && len(data) > 0 && !*yes {
			fmt.Fprintln(out, "다음을 지운다. 대화 기록과 폰 연결이 사라지고 되살릴 수 없다:")
			for _, d := range data {
				fmt.Fprintln(out, "  "+d)
			}
			fmt.Fprint(out, "계속하려면 yes 를 입력한다: ")
			line, _ := bufio.NewReader(in).ReadString('\n')
			if strings.TrimSpace(line) != "yes" {
				fmt.Fprintln(out, "\n그만뒀다. 아무것도 지우지 않았다")
				return nil
			}
		}
	}
	purgeAll := mode == "all" || mode == "ask"

	var removed, kept []string
	for _, h := range []host{p.server, p.agent} {
		ok, err := removeService(h, io.Discard)
		if err != nil {
			return err
		}
		if ok {
			s, _ := serviceFor(h)
			removed = append(removed, "서비스 등록 "+s.file)
		}
	}
	if p.connectFile != "" {
		if err := os.Remove(p.connectFile); err == nil {
			removed = append(removed, "에이전트 연결 정보 "+p.connectFile)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}

	exe := p.server.exe
	switch {
	case exe == "":
	case p.server.home != "" && exe == filepath.Join(p.server.home, ".local", "bin", "kyeot"):
		if err := os.Remove(exe); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		removed = append(removed, "실행 파일 "+exe)
		if prev := exe + ".prev"; os.Remove(prev) == nil {
			removed = append(removed, "이전 판 "+prev)
		}
	case strings.Contains(exe, "/Cellar/") || strings.Contains(exe, "/opt/kyeot/"):
		kept = append(kept, "실행 파일 "+exe+" — brew 로 깔았다면 brew uninstall kyeot")
	default:
		kept = append(kept, "실행 파일 "+exe+" — 설치 스크립트가 깐 것이 아니라 두었다")
	}

	for _, home := range existing(p.hermesHomes) {
		path := deckui.Path(home)
		switch res, err := deckui.Remove(home); {
		case err != nil:
			kept = append(kept, "화면 스킬 "+path+" — 지우지 못했다: "+err.Error())
		case res == deckui.Removed:
			removed = append(removed, "화면 스킬 "+path)
		case res == deckui.MovedOld:
			removed = append(removed, "옛 판 화면 스킬 "+path+" (.bak-<시각> 으로 옮겼다)")
		case res == deckui.Kept:
			kept = append(kept, "화면 스킬 "+path+" — 손본 사본이거나 링크라 두었다")
		}
	}

	var notes []string
	switch {
	case mode == "mine":
		r, n, err := purgeMine(p, info)
		if err != nil {
			return err
		}
		removed, notes = append(removed, r...), n
	case purgeAll:
		for _, d := range data {
			if err := os.RemoveAll(d); err != nil {
				return err
			}
			removed = append(removed, d)
		}
	default:
		for _, d := range existing(p.dataDirs) {
			kept = append(kept, d+" — 키와 대화 기록. 다시 깔면 그대로 이어진다. 지우려면 kyeot uninstall --purge")
		}
	}

	if len(removed) == 0 {
		fmt.Fprintln(out, "지울 것이 없다")
	} else {
		fmt.Fprintln(out, "지웠다:")
		for _, r := range removed {
			fmt.Fprintln(out, "  "+r)
		}
	}
	if len(kept) > 0 {
		fmt.Fprintln(out, "남겼다:")
		for _, k := range kept {
			fmt.Fprintln(out, "  "+k)
		}
	}
	for _, n := range notes {
		fmt.Fprintln(out, n)
	}
	if len(removed) > 0 && p.connectFile != "" {
		fmt.Fprintln(out, "폰 앱의 연결은 앱에서 지운다")
	}
	return nil
}

func existing(paths []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if _, err := os.Lstat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// purgeFlag accepts --purge, --purge=all and --purge=mine. A bare --purge is "ask".
type purgeFlag struct{ mode string }

func (f *purgeFlag) String() string   { return f.mode }
func (f *purgeFlag) IsBoolFlag() bool { return true }
func (f *purgeFlag) Set(v string) error {
	switch v {
	case "true":
		f.mode = "ask"
	case "false":
		f.mode = ""
	case "all", "mine":
		f.mode = v
	default:
		return fmt.Errorf("--purge 는 값 없이, 또는 --purge=all, --purge=mine")
	}
	return nil
}

// askPurge asks what to delete when there are other users. There is no default. "" if cancelled.
func askPurge(others []otherUser, in io.Reader, out io.Writer) string {
	r := bufio.NewReader(in)
	fmt.Fprintf(out, "이 서버에 다른 사용자 %d명이 있어요.\n", len(others))
	for _, u := range others {
		fmt.Fprintf(out, "  %s   방 %d개, 대화 %d개\n", u.name, u.rooms, u.tasks)
	}
	fmt.Fprintln(out, "무엇을 지울까요?")
	fmt.Fprintln(out, "  1) 모두 지우기     내 것과 다른 사람의 계정, 대화, 방까지")
	fmt.Fprintln(out, "  2) 내 것만 지우기  다른 사람의 계정과 기록은 이 컴퓨터에 남겨요")
	fmt.Fprintln(out, "  3) 그만두기")
	for {
		fmt.Fprint(out, "고르세요 (1/2/3): ")
		line, err := r.ReadString('\n')
		switch strings.TrimSpace(line) {
		case "1":
			fmt.Fprintf(out, "%d명의 계정, 대화, 방이 모두 지워지고 되돌릴 수 없어요.\n정말 지우려면 「지우기」라고 쳐 주세요: ", len(others))
			word, _ := r.ReadString('\n')
			if strings.TrimSpace(word) == "지우기" {
				return "all"
			}
			fmt.Fprintln(out)
			return ""
		case "2":
			return "mine"
		case "3":
			return ""
		}
		if err != nil {
			fmt.Fprintln(out)
			return ""
		}
		fmt.Fprintln(out, "1, 2, 3 가운데 하나를 쳐 주세요")
	}
}

// purgeMine, after stopping the service, deletes only the computer owner's records. Other people's records and the DB are kept.
func purgeMine(p uninstallPlan, info hostInfo) (removed, notes []string, err error) {
	if info.handedTo != "" { // already handed over, so this computer has no account. The current owner's records stay
		notes = append(notes, fmt.Sprintf("주인 권한이 %s에게 넘어가 있어 이 컴퓨터의 계정이 따로 없어요. 기록은 모두 남겨 뒀어요.", info.handedTo))
	} else if len(existing([]string{p.dbPath})) > 0 {
		st, err := store.Open(p.dbPath)
		if err != nil {
			return nil, nil, err
		}
		d, err := st.DeleteHost()
		if err != nil {
			st.Close()
			return nil, nil, err
		}
		defer func() { // after deleting attachments and tokens: empty old copies so none keeps my records, and make one for the remaining people
			backups := filepath.Join(filepath.Dir(p.dbPath), "backups")
			berr := st.RemoveBackups(backups)
			if berr == nil {
				_, berr = st.DailyBackup(backups)
			}
			st.Close()
			if berr != nil {
				notes = append(notes, "옛 백업을 정리하지 못했어요("+berr.Error()+"). "+backups+" 폴더와 kyeot.db.bak-* 를 직접 지워 주세요.")
			}
		}()
		for _, id := range d.Uploads {
			os.Remove(filepath.Join(filepath.Dir(p.dbPath), "uploads", id))
		}
		for _, f := range existing(p.tokenFiles) {
			if err := os.Remove(f); err != nil {
				return nil, nil, err
			}
			removed = append(removed, "소유자 토큰 "+f)
		}
		notes = append(notes, "내 에이전트, 대화, 첨부, 내가 만든 방을 지웠어요.", "다른 사람 방에 쓴 내 글은 「탈퇴한 사용자」로 남아요.")
	}
	names := make([]string, len(info.others))
	for i, u := range info.others {
		names[i] = u.name
	}
	if len(names) > 0 {
		notes = append(notes, strings.Join(names, ", ")+"의 기록은 남겨 뒀어요.", "곁을 다시 깔기 전까지 이 사람들은 접속할 수 없어요.", "남긴 기록까지 지우려면: kyeot uninstall --purge=all")
	}
	return removed, notes, nil
}
