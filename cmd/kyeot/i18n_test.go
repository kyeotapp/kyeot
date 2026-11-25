package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// The existing tests expect Korean text. Only tests that look at English switch it with t.Setenv.
func TestMain(m *testing.M) {
	os.Setenv("KYEOT_LANG", "ko")
	os.Exit(m.Run())
}

func setLang(t *testing.T, kyeot, lcAll, lcMessages, lang string) {
	t.Helper()
	t.Setenv("KYEOT_LANG", kyeot)
	t.Setenv("LC_ALL", lcAll)
	t.Setenv("LC_MESSAGES", lcMessages)
	t.Setenv("LANG", lang)
}

func hasHangul(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.Is(unicode.Hangul, r) }) >= 0
}

func TestLangDecision(t *testing.T) {
	cases := []struct {
		kyeot, lcAll, lcMessages, lang string
		ko                             bool
	}{
		{"", "", "", "", false},
		{"", "", "", "ko_KR.UTF-8", true},
		{"", "", "", "en_US.UTF-8", false},
		{"", "", "", "C", false},
		{"", "", "", "POSIX", false},
		{"", "", "ko_KR.UTF-8", "en_US.UTF-8", true},
		{"", "C", "ko_KR.UTF-8", "ko_KR.UTF-8", false},
		{"", "ko_KR.UTF-8", "", "en_US.UTF-8", true},
		{"ko", "en_US.UTF-8", "", "en_US.UTF-8", true},
		{"en", "ko_KR.UTF-8", "", "ko_KR.UTF-8", false},
		{"KO", "", "", "", true},
	}
	for _, c := range cases {
		setLang(t, c.kyeot, c.lcAll, c.lcMessages, c.lang)
		if got := langKo(); got != c.ko {
			t.Errorf("KYEOT_LANG=%q LC_ALL=%q LC_MESSAGES=%q LANG=%q: ko=%v, want %v", c.kyeot, c.lcAll, c.lcMessages, c.lang, got, c.ko)
		}
	}
}

// trKeys collects string literals passed to tr( and trLater( in non-test sources.
func trKeys(t *testing.T) map[string]string {
	t.Helper()
	files, _ := filepath.Glob("*.go")
	keys := map[string]string{}
	fs := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fs, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := c.Fun.(*ast.Ident)
			if !ok || (id.Name != "tr" && id.Name != "trLater") || len(c.Args) != 1 {
				return true
			}
			if b, ok := c.Args[0].(*ast.BasicLit); ok && b.Kind == token.STRING {
				s, err := strconv.Unquote(b.Value)
				if err != nil {
					t.Fatal(err)
				}
				keys[s] = fs.Position(b.Pos()).String()
			}
			return true
		})
	}
	if len(keys) < 40 {
		t.Fatalf("only %d tr literals found. The lookup is broken", len(keys))
	}
	return keys
}

var verbRe = regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z%]`)

func TestEnglishDictionary(t *testing.T) {
	keys := trKeys(t)
	for k, at := range keys {
		e, ok := en[k]
		if !ok {
			t.Errorf("%s: not in the dictionary: %q", at, k)
			continue
		}
		if !slices.Equal(verbRe.FindAllString(k, -1), verbRe.FindAllString(e, -1)) {
			t.Errorf("%s: format verbs differ: %q → %q", at, k, e)
		}
		if strings.HasSuffix(k, "\n") != strings.HasSuffix(e, "\n") {
			t.Errorf("%s: trailing newline differs: %q", at, e)
		}
	}
	for k, e := range en {
		if _, ok := keys[k]; !ok {
			t.Errorf("dictionary entry unused in the source: %q", k)
		}
		if hasHangul(e) {
			t.Errorf("Hangul on the English side: %q", e)
		}
	}
}

func TestShowPairEnglish(t *testing.T) {
	setLang(t, "", "", "", "en_US.UTF-8")
	t.Setenv("KYEOT_PUBLIC_URL", "")
	forceTerminal(t, true)
	var b bytes.Buffer
	if err := showPair(&b, "http://192.0.2.1:7700", "fake-owner-token-0000", "127.0.0.1:7700"); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if hasHangul(out) {
		t.Fatalf("Hangul in an English locale:\n%s", out)
	}
	for _, want := range []string{"“Scan QR”", "To enter manually", "Server address http://192.0.2.1:7700", "Owner token   fake-owner-token-0000", "can't reach it", "owner key"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is missing:\n%s", want, out)
		}
	}
	b.Reset()
	usage(&b)
	if hasHangul(b.String()) || !strings.Contains(b.String(), "Usage: kyeot") {
		t.Fatalf("English help:\n%s", b.String())
	}
}

// Korean-locale output is identical, character for character, to before the English version was added.
func TestShowPairKoreanUnchanged(t *testing.T) {
	setLang(t, "", "", "", "ko_KR.UTF-8")
	t.Setenv("KYEOT_PUBLIC_URL", "")
	forceTerminal(t, true)
	var b bytes.Buffer
	if err := showPair(&b, "http://192.0.2.1:7700", "fake-owner-token-0000", "127.0.0.1:7700"); err != nil {
		t.Fatal(err)
	}
	_, after, _ := strings.Cut(b.String(), "\n곁 앱의")
	want := ` 「QR 찍기」로 찍으세요. 다시 보려면 kyeot pair

직접 입력할 때
  서버 주소     http://192.0.2.1:7700
  소유자 토큰   fake-owner-token-0000
주의: 서버가 이 기기 안(127.0.0.1:7700)에서만 받고 있어 폰이 닿지 않는다. KYEOT_ADDR=0.0.0.0:7700 로 띄워야 한다.
위 QR 과 토큰은 이 서버의 주인 키예요. 다른 사람에게 보여 주지 마세요.
`
	if after != want {
		t.Fatalf("Korean output changed:\n%q\nwant:\n%q", after, want)
	}
}
