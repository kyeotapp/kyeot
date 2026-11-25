// Package texttest imitates in tests what the app does (picking text by key table). Test use only.
package texttest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/kyeotapp/kyeot/internal/sensor/text"
)

func Ko(t *testing.T) map[string]string {
	t.Helper()
	_, me, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(me), "..", "testdata", "ko.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]string
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

var hole = regexp.MustCompile(`\{(\w+)\}`)

// Render turns one key received as JSON into text using dict. Array arguments are joined with ", ".
func Render(t *testing.T, v any, dict map[string]string) string {
	t.Helper()
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = Render(t, e, dict)
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		id, _ := x["id"].(string)
		tpl, ok := dict[id]
		if !ok {
			t.Errorf("key %q not in dictionary", id)
			return ""
		}
		args, _ := x["args"].(map[string]any)
		return hole.ReplaceAllStringFunc(tpl, func(h string) string {
			a, ok := args[h[1:len(h)-1]]
			if !ok {
				t.Errorf("%s: missing argument %s", id, h)
				return h
			}
			return Render(t, a, dict)
		})
	}
	t.Errorf("unknown argument %v", v)
	return ""
}

var korean = regexp.MustCompile(`[가-힣]`)

// Check inspects a bundle's (JSON) key table: each key renders the same as the old text, and every text field containing Korean has a key.
func Check(t *testing.T, raw []byte) {
	t.Helper()
	var b map[string]any
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	keys, ok := b[text.Field].(map[string]any)
	if !ok {
		t.Fatalf("%s field missing: %s", text.Field, raw)
	}
	delete(b, text.Field)
	dict := Ko(t)
	for path, m := range keys {
		got, ok := at(b, path).(string)
		if !ok {
			t.Errorf("no text field for key %s", path)
			continue
		}
		if r := Render(t, m, dict); r != got {
			t.Errorf("%s: rendered from key %q ≠ old text %q", path, r, got)
		}
	}
	walk(b, "", func(path, s string) {
		if _, ok := keys[path]; !ok && korean.MatchString(s) {
			t.Errorf("%s: Korean text %q has no key", path, s)
		}
	})
}

func at(v any, path string) any {
	for _, k := range strings.Split(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			v = x[k]
		case []any:
			n, err := strconv.Atoi(k)
			if err != nil || n < 0 || n >= len(x) {
				return nil
			}
			v = x[n]
		default:
			return nil
		}
	}
	return v
}

func walk(v any, path string, f func(string, string)) {
	join := func(k string) string {
		if path == "" {
			return k
		}
		return path + "." + k
	}
	switch x := v.(type) {
	case string:
		f(path, x)
	case map[string]any:
		for k, e := range x {
			walk(e, join(k), f)
		}
	case []any:
		for i, e := range x {
			walk(e, join(fmt.Sprint(i)), f)
		}
	}
}
