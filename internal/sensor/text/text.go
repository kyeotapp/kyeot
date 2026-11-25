// Package text records a sensor bundle's display text as language-neutral keys.
// The top-level "$text" field of a bundle is a {path in bundle: key} table. The Korean text at those paths is kept as is for old apps.
package text

import "fmt"

// Field is the name of the key table at the top of a bundle. The blueprint doesn't read it, so it triggers no shape warning.
const Field = "$text"

// Msg is one piece of display text. An Args value is a number, raw text, a Msg, or []Msg (the app joins it as a list).
type Msg struct {
	ID   string         `json:"id"`
	Args map[string]any `json:"args,omitempty"`
}

func M(id string, kv ...any) Msg {
	m := Msg{ID: id}
	for i := 0; i+1 < len(kv); i += 2 {
		if m.Args == nil {
			m.Args = map[string]any{}
		}
		m.Args[kv[i].(string)] = kv[i+1]
	}
	return m
}

// Map is the path → key table.
type Map map[string]Msg

// Put records only when v is a Msg. Raw text (e.g. unknown status codes) is shown as is, without a key.
func (t Map) Put(path string, v any) {
	if m, ok := v.(Msg); ok {
		t[path] = m
	}
}

// Path builds an "a.0.b"-style path.
func Path(parts ...any) string {
	s := ""
	for i, p := range parts {
		if i > 0 {
			s += "."
		}
		s += fmt.Sprint(p)
	}
	return s
}
