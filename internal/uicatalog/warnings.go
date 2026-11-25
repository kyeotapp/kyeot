package uicatalog

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

const maxWarnings = 5

var dayPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// timeLayouts are ISO forms the app's Date.parse reads the same everywhere.
var timeLayouts = []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"}

// Warnings reports values that are stored but will look different in the app. It does not reject; a new rejection is a new demand on agents.
// Call it only on specs that passed Validate. Bundle values pointed to by $data cannot be known here.
func Warnings(raw []byte) []string {
	var s struct {
		Blocks []json.RawMessage `json:"blocks"`
	}
	if json.Unmarshal(raw, &s) != nil {
		return nil
	}
	var out []string
	add := func(format string, a ...any) {
		if len(out) < maxWarnings {
			out = append(out, fmt.Sprintf(format, a...))
		}
	}
	for i, b := range s.Blocks {
		var x struct {
			Type  string `json:"type"`
			To    any    `json:"to"`
			Items any    `json:"items"`
		}
		json.Unmarshal(b, &x)
		switch x.Type {
		case "countdown":
			if to, ok := x.To.(string); ok && !isTime(to) {
				add("blocks[%d]: countdown.to %q 는 시각으로 읽히지 않아 「—」로 보인다. 2026-12-24T18:00:00+09:00 처럼 쓴다", i, to)
			}
		case "calendar":
			items, _ := x.Items.([]any)
			for j, it := range items {
				m, _ := it.(map[string]any)
				if d, ok := m["date"].(string); !ok || !dayPattern.MatchString(d) {
					add("blocks[%d].items[%d]: calendar 날짜 %v 는 YYYY-MM-DD 가 아니라 그 칸이 빠진다", i, j, m["date"])
					break
				}
			}
		}
	}
	return out
}

func isTime(s string) bool {
	for _, l := range timeLayouts {
		if _, err := time.Parse(l, s); err == nil {
			return true
		}
	}
	return false
}
