package main

import (
	"os"
	"strings"
)

// langKo decides whether command-line output is Korean. True only when the first set of KYEOT_LANG > LC_ALL > LC_MESSAGES > LANG starts with ko.
func langKo() bool {
	for _, k := range []string{"KYEOT_LANG", "LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" {
			return strings.HasPrefix(strings.ToLower(v), "ko")
		}
	}
	return false
}

// tr looks up the Korean source text as a key in the English dictionary (en). Returns the source if the locale is Korean or the key is missing.
func tr(ko string) string {
	if langKo() {
		return ko
	}
	if s, ok := en[ko]; ok {
		return s
	}
	return ko
}

// trLater marks source text to pass through tr later. The dictionary check finds this literal too.
func trLater(ko string) string { return ko }
