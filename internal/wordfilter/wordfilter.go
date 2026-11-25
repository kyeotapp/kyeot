// Package wordfilter checks room messages for banned words. The list is a minimal one written in this repo (no external list).
package wordfilter

import (
	"strings"
	"unicode"
)

// Korean is matched as a substring after removing spaces and symbols. Words that overlap common phrases, like "보지 마", are left out.
var korean = []string{
	"시발", "씨발", "씨빨", "시빨", "씨팔", "시팔", "ㅅㅂ", "ㅆㅂ", "ㅅㅂㄹㅁ",
	"병신", "븅신", "ㅂㅅ", "개새끼", "개새기", "개색기", "개색끼", "새끼야",
	"좆", "존나", "ㅈㄴ", "지랄", "ㅈㄹ", "닥쳐", "미친놈", "미친년",
	"엠창", "느금마", "니애미", "썅", "쌍년", "걸레년", "창녀",
	"섹스",
}

// common words containing a flagged word; removed before checking
var allowed = []string{"시발점", "시발역", "시발택시"}

// English is matched per word. prefix matches a word's start, exact matches the whole word.
var (
	prefix = []string{"fuck", "motherfuck", "shit", "bitch", "cunt", "asshole", "nigger", "nigga", "faggot", "whore", "slut", "bastard"}
	exact  = []string{"fck", "fk", "stfu", "wtf", "dickhead", "cock", "pussy", "retard", "retarded", "porn"}
)

// Match returns true if a banned word is present.
func Match(text string) bool {
	if text == "" {
		return false
	}
	lower := strings.ToLower(text)
	var squeezed strings.Builder
	for _, r := range lower {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			squeezed.WriteRune(r)
		}
	}
	s := squeezed.String()
	for _, ok := range allowed {
		s = strings.ReplaceAll(s, ok, "")
	}
	for _, w := range korean {
		if strings.Contains(s, w) {
			return true
		}
	}
	for _, word := range strings.FieldsFunc(lower, func(r rune) bool { return !unicode.IsLetter(r) }) {
		for _, p := range prefix {
			if strings.HasPrefix(word, p) {
				return true
			}
		}
		for _, e := range exact {
			if word == e {
				return true
			}
		}
	}
	return false
}

// Size is the number of words in the default list (for app settings text).
func Size() int { return len(korean) + len(prefix) + len(exact) }
