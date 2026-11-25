package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kyeotapp/kyeot/internal/store"
)

// sharedPlan puts a real DB into the installed layout: the computer owner's agent and two other users.
func sharedPlan(t *testing.T, tty bool) (uninstallPlan, string) {
	t.Helper()
	p, _ := installedPlan(t)
	data := p.dataDirs[0]
	p.dbPath, p.tokenFiles = filepath.Join(data, "kyeot.db"), []string{filepath.Join(data, "owner-token")}
	p.interactive = func() bool { return tty }
	os.Remove(p.dbPath)
	st, err := store.Open(p.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateAgent(store.Owner, "Agent A"); err != nil {
		t.Fatal(err)
	}
	other, _, _ := st.CreateOwner("Alice")
	st.CreateOwner("Bob")
	st.Close()
	return p, other
}

func ownerName(t *testing.T, db, id string) string {
	t.Helper()
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	return st.OwnerName(id)
}

func hostAgents(t *testing.T, db string) int {
	t.Helper()
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, _ := st.Agents(store.Owner)
	return len(a)
}

func TestPurgeWithOthersStopsWhenItCannotAsk(t *testing.T) {
	p, _ := sharedPlan(t, false)
	var out bytes.Buffer
	if err := runUninstall(p, []string{"--purge"}, strings.NewReader("1\n지우기\n"), &out); err == nil {
		t.Fatal("it must stop with an error when it cannot ask")
	}
	if !exists(p.server.exe) || !exists(p.dbPath) || !exists(launchdService(p.server).file) {
		t.Fatal("deleted something although it stopped")
	}
	mustContain(t, out.String(), "다른 사용자 2명", "--purge=all", "--purge=mine")
	if strings.Contains(out.String(), "고르세요") {
		t.Fatal("asked although it cannot ask")
	}
}

func TestPurgeWithOthersAsksAndCancels(t *testing.T) {
	for name, input := range map[string]string{
		"cancel":                 "3\n",
		"enter is not a default": "\n\n",
		"all but wrong word":     "1\n delete\n",
		"end of input":           "",
	} {
		t.Run(name, func(t *testing.T) {
			p, _ := sharedPlan(t, true)
			var out bytes.Buffer
			if err := runUninstall(p, []string{"--purge"}, strings.NewReader(input), &out); err != nil {
				t.Fatal(err)
			}
			if !exists(p.server.exe) || !exists(p.dbPath) || hostAgents(t, p.dbPath) != 1 {
				t.Fatal("deleted although cancelled")
			}
			mustContain(t, out.String(), "Alice   방 0개, 대화 0개", "1) 모두 지우기", "그만뒀다")
		})
	}
}

func TestPurgeWithOthersAllNeedsWord(t *testing.T) {
	p, _ := sharedPlan(t, true)
	var out bytes.Buffer
	if err := runUninstall(p, []string{"--purge"}, strings.NewReader("1\n지우기\n"), &out); err != nil {
		t.Fatal(err)
	}
	if exists(p.dataDirs[0]) || exists(p.server.exe) {
		t.Fatal("something remained after delete-all")
	}
	mustContain(t, out.String(), "2명의 계정, 대화, 방이 모두 지워지고")
}

func TestPurgeWithOthersMineKeepsOthers(t *testing.T) {
	for name, run := range map[string]struct {
		args  []string
		input string
		tty   bool
	}{
		"choose": {[]string{"--purge"}, "2\n", true},
		"script": {[]string{"--purge=mine"}, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			p, other := sharedPlan(t, run.tty)
			backups := filepath.Join(filepath.Dir(p.dbPath), "backups")
			old := filepath.Join(backups, "kyeot-20261120-010203.db")
			os.MkdirAll(backups, 0o700)
			os.WriteFile(old, []byte("old copy holding my records"), 0o600)
			bak := p.dbPath + ".bak-8-20261120-010203"
			os.WriteFile(bak, []byte("old copy holding my records"), 0o600)
			var out bytes.Buffer
			if err := runUninstall(p, run.args, strings.NewReader(run.input), &out); err != nil {
				t.Fatal(err)
			}
			if exists(old) || exists(bak) {
				t.Fatal("an old backup holding my records remains")
			}
			if m, _ := filepath.Glob(filepath.Join(backups, "kyeot-*.db")); len(m) != 1 || hostAgents(t, m[0]) != 0 {
				t.Fatalf("there must be one new backup for the remaining people and it must not hold my agent: %v", m)
			}
			if exists(p.server.exe) || exists(p.tokenFiles[0]) {
				t.Fatal("the program and my key must be deleted")
			}
			if !exists(p.dbPath) || ownerName(t, p.dbPath, other) != "Alice" || hostAgents(t, p.dbPath) != 0 {
				t.Fatal("other people's records must stay and only my agent must go")
			}
			mustContain(t, out.String(), "내 에이전트, 대화, 첨부, 내가 만든 방을 지웠어요", "곁을 다시 깔기 전까지 이 사람들은 접속할 수 없어요", "--purge=all")
		})
	}
}

func TestPurgeAllFlagSkipsQuestions(t *testing.T) {
	p, _ := sharedPlan(t, false)
	var out bytes.Buffer
	if err := runUninstall(p, []string{"--purge=all"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if exists(p.dataDirs[0]) {
		t.Fatal("something remained after --purge=all")
	}
	if err := runUninstall(p, []string{"--purge=some"}, strings.NewReader(""), &out); err == nil {
		t.Fatal("an unknown value is rejected")
	}
}

// A single-user server (real DB, no other users) needs just one yes, as now
func TestPurgeAloneStillAsksYes(t *testing.T) {
	p, _ := installedPlan(t)
	data := p.dataDirs[0]
	p.dbPath = filepath.Join(data, "kyeot.db")
	os.Remove(p.dbPath)
	st, _ := store.Open(p.dbPath)
	st.Close()
	p.interactive = func() bool { return false }
	var out bytes.Buffer
	if err := runUninstall(p, []string{"--purge"}, strings.NewReader("yes\n"), &out); err != nil {
		t.Fatal(err)
	}
	if exists(data) {
		t.Fatal("something remained after yes")
	}
	if strings.Contains(out.String(), "고르세요") {
		t.Fatal("asked to choose on a single-user server")
	}
}

// After handing over, pair --rotate asks once. If the answer is no, the token stays
func TestRotateAsksAfterHandOver(t *testing.T) {
	tf, _, token := rotateHome(t)
	var out bytes.Buffer
	noRestart := func(w io.Writer) (bool, error) { return false, nil }
	if err := rotateFlow(true, false, true, "Alice", strings.NewReader("n\n"), &out, noRestart); err == nil {
		t.Fatal("proceeded although the answer was no")
	}
	if b, _ := os.ReadFile(tf); strings.TrimSpace(string(b)) != token {
		t.Fatal("the token changed although cancelled")
	}
	mustContain(t, out.String(), "주인 권한이 Alice에게 넘어가 있어요", "새 주인 계정이 생기고 Alice은(는) 관리자가 돼요")
	out.Reset()
	if err := rotateFlow(true, false, false, "Alice", strings.NewReader("y\n"), &out, noRestart); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(tf); strings.TrimSpace(string(b)) == token {
		t.Fatal("the token stayed although the answer was y")
	}
	out.Reset()
	if err := rotateFlow(true, false, false, "", strings.NewReader(""), &out, noRestart); err != nil || strings.Contains(out.String(), "넘어가 있어요") {
		t.Fatalf("a server not handed over does not ask: %v %q", err, out.String())
	}
}

func TestReadHostInfoAfterHandOver(t *testing.T) {
	p, other := sharedPlan(t, true)
	st, _ := store.Open(p.dbPath)
	if _, err := st.HandOver(other, "host-token-0123456789"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	info, err := readHostInfo(p.dbPath)
	if err != nil || info.handedTo != "Alice" || len(info.others) != 2 {
		t.Fatalf("%+v %v", info, err)
	}
	var out bytes.Buffer
	if err := runUninstall(p, []string{"--purge=mine"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if !exists(p.tokenFiles[0]) || ownerName(t, p.dbPath, other) != "Alice" {
		t.Fatal("after handing over, deleting only mine leaves the DB and token as is")
	}
}

// After handing over, kyeot pair prints a notice instead of the QR. Both Korean and English
func TestPairNoteAfterHandOver(t *testing.T) {
	var out bytes.Buffer
	if handedOverNote(&out, "") || out.Len() != 0 {
		t.Fatal("prints nothing if not handed over")
	}
	t.Setenv("KYEOT_LANG", "ko")
	if !handedOverNote(&out, "Alice") {
		t.Fatal("true after handing over")
	}
	mustContain(t, out.String(), "주인 권한이 Alice에게 넘어가 있어요", "kyeot pair --rotate")
	out.Reset()
	t.Setenv("KYEOT_LANG", "en")
	handedOverNote(&out, "Jisu")
	mustContain(t, out.String(), "handed over to Jisu", "kyeot pair --rotate")
}
