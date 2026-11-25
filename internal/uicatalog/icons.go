package uicatalog

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

type iconRules struct {
	Fallback string `json:"fallback"`
	Words    []struct {
		Icon  string   `json:"icon"`
		Words []string `json:"words"`
	} `json:"words"`
	Blocks map[string]string `json:"blocks"`
}

var rules = func() (r struct {
	IconRules iconRules `json:"iconRules"`
}) {
	if err := json.Unmarshal(Raw, &r); err != nil {
		panic("uicatalog: " + err.Error())
	}
	return r
}().IconRules

// KnownIcon reports whether the app can draw the icon on every platform. The .fill pair counts the same.
func KnownIcon(icon string) bool {
	return slices.Contains(cat.Icons, icon) || slices.Contains(cat.Icons, strings.TrimSuffix(icon, ".fill"))
}

// FitIcon replaces an empty or off-catalog icon with one chosen from the title and parts. In-catalog icons are kept.
// Call it only on specs that passed Validate. note is filled only when something changed.
func FitIcon(raw []byte) ([]byte, string) {
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return raw, ""
	}
	var s struct {
		Title  string `json:"title"`
		Icon   string `json:"icon"`
		Blocks []struct {
			Type string `json:"type"`
		} `json:"blocks"`
	}
	json.Unmarshal(raw, &s)
	if KnownIcon(s.Icon) {
		return raw, ""
	}
	icon := inferIcon(s.Title, func(yield func(string) bool) {
		for _, b := range s.Blocks {
			if !yield(b.Type) {
				return
			}
		}
	})
	top["icon"], _ = json.Marshal(icon)
	out, err := json.Marshal(top)
	if err != nil {
		return raw, ""
	}
	if s.Icon == "" {
		return out, fmt.Sprintf("icon 이 없어 %q 로 정했다", icon)
	}
	return out, fmt.Sprintf("icon %q 는 앱 대응표에 없어 %q 로 바꿨다", s.Icon, icon)
}

// inferIcon picks by title word table (earlier wins), then first mapped part, then the default.
func inferIcon(title string, types func(func(string) bool)) string {
	lower := strings.ToLower(title)
	tokens := strings.FieldsFunc(lower, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for _, e := range rules.Words {
		for _, w := range e.Words {
			if titleHas(lower, tokens, strings.ToLower(w)) {
				return e.Icon
			}
		}
	}
	for t := range types {
		if icon, ok := rules.Blocks[t]; ok {
			return icon
		}
	}
	return rules.Fallback
}

// titleHas matches by containment for Korean of two or more characters, and by whole word for English or one character (so "방송" does not match "방" and "brunch" does not match "run").
func titleHas(lower string, tokens []string, w string) bool {
	if w == "" {
		return false
	}
	if utf8.RuneCountInString(w) >= 2 && !isASCII(w) {
		return strings.Contains(lower, w)
	}
	return slices.Contains(tokens, w)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
