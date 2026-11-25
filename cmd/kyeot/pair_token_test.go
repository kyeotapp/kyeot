package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

const fakeToken = "fake-owner-token-0000"

func forceTerminal(t *testing.T, on bool) {
	t.Helper()
	old := isTerminal
	isTerminal = func(io.Writer) bool { return on }
	t.Cleanup(func() { isTerminal = old })
}

// If not a terminal, the QR is left as is and only the token text is hidden.
func TestShowPairHidesTokenOffTerminal(t *testing.T) {
	for _, lang := range []string{"ko_KR.UTF-8", "en_US.UTF-8"} {
		setLang(t, "", "", "", lang)
		t.Setenv("KYEOT_PUBLIC_URL", "")
		forceTerminal(t, false)
		var b bytes.Buffer
		if err := showPair(&b, "http://192.0.2.1:7700", fakeToken, "0.0.0.0:7700"); err != nil {
			t.Fatal(err)
		}
		out := b.String()
		if strings.Contains(out, fakeToken) {
			t.Errorf("%s: the token appeared although not a terminal", lang)
		}
		if !strings.Contains(out, "kyeot pair") || !strings.Contains(out, "▀") {
			t.Errorf("%s: no guide or QR:\n%s", lang, out)
		}
	}
}

// The real isTerminal: a buffer is not a terminal, and neither is a regular file.
func TestIsTerminalPlainWriters(t *testing.T) {
	if isTerminal(&bytes.Buffer{}) {
		t.Error("treated a buffer as a terminal")
	}
	f, err := os.Create(t.TempDir() + "/out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Error("treated a regular file as a terminal")
	}
}

// showPair prints the token and the combined warning on the manual-entry line.
func TestShowPairPrintsToken(t *testing.T) {
	setLang(t, "", "", "", "ko_KR.UTF-8")
	t.Setenv("KYEOT_PUBLIC_URL", "")
	forceTerminal(t, true)
	var b bytes.Buffer
	if err := showPair(&b, "http://192.0.2.1:7700", fakeToken, "0.0.0.0:7700"); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	mustContain(t, out, "소유자 토큰   "+fakeToken, "위 QR 과 토큰은 이 서버의 주인 키예요")
	if strings.Contains(out, "이 QR 에는") {
		t.Error("the old warning sentence remains")
	}
}

// After handing over ownership, only the notice appears, with no token (runPair ends via handedOverNote before showPair).
func TestHandedOverShowsNoToken(t *testing.T) {
	setLang(t, "", "", "", "ko_KR.UTF-8")
	var b bytes.Buffer
	if !handedOverNote(&b, "Alice") {
		t.Fatal("handed over but no notice")
	}
	if strings.Contains(b.String(), fakeToken) {
		t.Error("the token appeared after handing over")
	}
}

// Even if copying fails, it does not end in an error but tells the user to take it from the screen.
func TestCopyOwnerTokenFallback(t *testing.T) {
	setLang(t, "", "", "", "ko_KR.UTF-8")
	var b bytes.Buffer
	err := copyOwnerToken(&b, fakeToken, "http://192.0.2.1:7700", func(string) error { return errors.New("no pbcopy") })
	if err != nil {
		t.Fatalf("a copy failure ended in an error: %v", err)
	}
	mustContain(t, b.String(), "클립보드에 복사하지 못했어요", "kyeot pair 화면")
	if strings.Contains(b.String(), fakeToken) {
		t.Error("the -copy notice printed the token")
	}
	var got string
	b.Reset()
	_ = copyOwnerToken(&b, fakeToken, "x", func(s string) error { got = s; return nil })
	if got != fakeToken {
		t.Errorf("copied value %q", got)
	}
	mustContain(t, b.String(), "복사했어요")
}
