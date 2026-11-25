package uicatalog

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// DataWarning is why a Path slot read by tab Screen's spec disagrees with the bundle.
type DataWarning struct{ Screen, Path, Reason string }

func (w DataWarning) String() string { return w.Screen + " 탭: " + w.Reason }

// DataWarnings checks the bundle against the spec slots that point to it. It does not reject; it reports slots that would end up empty.
func DataWarnings(screens []json.RawMessage, name string, raw []byte) []DataWarning {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	var out []DataWarning
	add := func(screen, path, msg string) {
		w := DataWarning{screen, path, msg}
		if len(out) < maxWarnings && !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	for _, sc := range screens {
		var s struct {
			ID     string                       `json:"id"`
			Blocks []map[string]json.RawMessage `json:"blocks"`
		}
		if json.Unmarshal(sc, &s) != nil {
			continue
		}
		for _, b := range s.Blocks {
			var typ string
			json.Unmarshal(b["type"], &typ)
			comp, ok := cat.Components[typ]
			if !ok {
				continue
			}
			for key, v := range b {
				p, ok := comp.Props[key]
				if !ok {
					continue
				}
				if path, ok := refIn(v, name); ok {
					if msg := checkData(value, path, p, typ); msg != "" {
						add(s.ID, path, msg)
					}
					continue
				}
				if p.Kind != "items" {
					continue
				}
				var items []map[string]json.RawMessage
				json.Unmarshal(v, &items)
				for _, it := range items { // slots like stats that put $data inside a field
					for _, fv := range it {
						if path, ok := refIn(fv, name); ok {
							if msg := checkData(value, path, prop{Kind: "scalar"}, typ); msg != "" {
								add(s.ID, path, msg)
							}
						}
					}
				}
			}
		}
	}
	return out
}

// refIn returns the path if v is a {"$data": …} pointing at bundle name.
func refIn(v json.RawMessage, name string) (string, bool) {
	if !isDataRef(v) {
		return "", false
	}
	var ref map[string]string
	json.Unmarshal(v, &ref)
	path := ref["$data"]
	return path, path == name || strings.HasPrefix(path, name+".")
}

func checkData(value any, path string, p prop, typ string) string {
	got, msg := resolve(value, path)
	if msg != "" {
		return msg
	}
	switch p.Kind {
	case "number":
		if _, ok := got.(float64); !ok {
			return fmt.Sprintf("`%s` 자리는 숫자인데 온 값은 %s", path, kindOf(got))
		}
	case "scalar":
		switch got.(type) {
		case float64, string:
		default:
			return fmt.Sprintf("`%s` 자리는 숫자나 글인데 온 값은 %s", path, kindOf(got))
		}
	case "items":
		arr, ok := got.([]any)
		if !ok {
			return fmt.Sprintf("`%s` 자리는 %s 목록(배열)인데 온 값은 %s", path, typ, kindOf(got))
		}
		for i, el := range arr {
			raw, _ := json.Marshal(el)
			if err := checkItem(p.Fields, raw); err != nil {
				return fmt.Sprintf("`%s.%d`: %s 칸은 %v", path, i, typ, err)
			}
			if o, ok := el.(map[string]any); ok && len(p.Fields) > 0 {
				if _, ok := o[p.Fields[0]]; !ok {
					return fmt.Sprintf("`%s.%d` 에 %s 키가 없다. %s 칸은 {%s} 꼴이다", path, i, p.Fields[0], typ, strings.Join(p.Fields, ","))
				}
			}
		}
	}
	return ""
}

// resolve reads paths the way the app does: objects by key, arrays by numeric segment. If not found, says where it broke off.
func resolve(value any, path string) (any, string) {
	seg := strings.Split(path, ".")
	cur := value
	for i := 1; i < len(seg); i++ {
		at := strings.Join(seg[:i], ".")
		switch x := cur.(type) {
		case map[string]any:
			next, ok := x[seg[i]]
			if !ok {
				keys := make([]string, 0, len(x))
				for k := range x {
					keys = append(keys, k)
				}
				slices.Sort(keys)
				return nil, fmt.Sprintf("`%s` 가 묶음에 없다. `%s` 의 키는 %s", path, at, strings.Join(keys, ", "))
			}
			cur = next
		case []any:
			n, err := strconv.Atoi(seg[i])
			if err != nil || n < 0 || n >= len(x) {
				return nil, fmt.Sprintf("`%s` 가 묶음에 없다. `%s` 는 %d개짜리 배열이다", path, at, len(x))
			}
			cur = x[n]
		default:
			return nil, fmt.Sprintf("`%s` 가 묶음에 없다. `%s` 는 %s", path, at, kindOf(x))
		}
	}
	return cur, ""
}

// kindOf returns a value kind phrased to end in a Korean sentence ending.
func kindOf(v any) string {
	switch v.(type) {
	case nil:
		return "null 이다"
	case float64:
		return "숫자다"
	case string:
		return "글이다"
	case bool:
		return "참거짓이다"
	case []any:
		return "배열이다"
	default:
		return "객체다"
	}
}
