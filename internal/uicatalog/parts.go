package uicatalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
)

// Assembled part: a named template bundling several base parts. The server expands it, so only base parts reach the app.

type Param struct {
	Kind     string          `json:"kind"`
	Required bool            `json:"required,omitempty"`
	Default  json.RawMessage `json:"default,omitempty"`
	Sample   json.RawMessage `json:"sample,omitempty"`
}

type Part struct {
	Version int               `json:"version"`
	ID      string            `json:"id"`
	Title   string            `json:"title"`
	Params  map[string]Param  `json:"params"`
	Blocks  []json.RawMessage `json:"blocks"`
	sites   map[string][]site // parameter -> slots where it is used
}

type site struct {
	block int
	typ   string
	prop  string
	p     prop
}

func MaxParts() int { return cat.Parts.MaxParts }

var paramName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)

// paramKinds are the kinds a parameter may have; the same as kinds used by base part properties.
func paramKinds() []string {
	var out []string
	for typ, c := range cat.Components {
		if typ == "use" {
			continue
		}
		for _, p := range c.Props {
			if !slices.Contains(out, p.Kind) {
				out = append(out, p.Kind)
			}
		}
	}
	slices.Sort(out)
	return out
}

func paramRef(v json.RawMessage) (string, bool) {
	var ref map[string]json.RawMessage
	if json.Unmarshal(v, &ref) != nil || len(ref) != 1 {
		return "", false
	}
	raw, ok := ref["$param"]
	if !ok {
		return "", false
	}
	var name string
	json.Unmarshal(raw, &name)
	return name, true
}

// ValidatePart returns the parsed assembled part if it follows the rules, else the reason to fix.
func ValidatePart(raw []byte) (Part, error) {
	var p Part
	if len(raw) > cat.Limits.MaxSpecBytes {
		return p, fmt.Errorf("조립 부품이 %d바이트를 넘는다", cat.Limits.MaxSpecBytes)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil || top == nil {
		return p, fmt.Errorf("JSON 객체가 아니다")
	}
	fields := map[string]any{"version": &p.Version, "id": &p.ID, "title": &p.Title, "params": &p.Params, "blocks": &p.Blocks}
	for k := range top {
		if _, ok := fields[k]; !ok {
			return p, fmt.Errorf("조립 부품에 없는 속성 %q", k)
		}
	}
	for k, dst := range fields {
		v, ok := top[k]
		if !ok || isNull(v) {
			return p, fmt.Errorf("%s 가 필요하다", k)
		}
		if err := json.Unmarshal(v, dst); err != nil {
			return p, fmt.Errorf("%s 의 형식이 틀렸다", k)
		}
	}
	lim := cat.Parts
	switch {
	case p.Version != 1:
		return p, fmt.Errorf("version 은 1 이어야 한다")
	case !idPattern.MatchString(p.ID):
		return p, fmt.Errorf("id 는 소문자, 숫자, - 로 32자 이하")
	case !between(p.Title, 1, 20):
		return p, fmt.Errorf("title 은 1~20자")
	case len(p.Params) == 0 || len(p.Params) > lim.MaxParams:
		return p, fmt.Errorf("params 는 1~%d개", lim.MaxParams)
	case len(p.Blocks) == 0 || len(p.Blocks) > lim.MaxBlocks:
		return p, fmt.Errorf("blocks 는 1~%d개", lim.MaxBlocks)
	}
	kinds := paramKinds()
	for _, name := range sortedKeys(p.Params) {
		pm := p.Params[name]
		switch {
		case !paramName.MatchString(name):
			return p, fmt.Errorf("매개변수 이름 %q 는 영문으로 시작하는 영문, 숫자, _ 32자 이하", name)
		case !slices.Contains(kinds, pm.Kind):
			return p, fmt.Errorf("params.%s.kind 는 %v 중 하나", name, kinds)
		case pm.Required && pm.Default != nil:
			return p, fmt.Errorf("params.%s: required 와 default 는 같이 못 쓴다", name)
		}
		if err := strictParamKeys(top["params"], name); err != nil {
			return p, err
		}
	}
	p.sites = map[string][]site{}
	for i, b := range p.Blocks {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(b, &fields); err != nil || fields == nil {
			return p, fmt.Errorf("blocks[%d]: 객체가 아니다", i)
		}
		var typ string
		json.Unmarshal(fields["type"], &typ)
		if typ == "use" {
			return p, fmt.Errorf("blocks[%d]: 조립 부품 안에서는 use 를 못 쓴다. 기본 부품만 넣는다", i)
		}
		comp, ok := cat.Components[typ]
		if !ok {
			return p, fmt.Errorf("blocks[%d]: 모르는 부품 %q", i, typ)
		}
		for _, prop := range sortedKeys(fields) {
			name, ok := paramRef(fields[prop])
			if !ok {
				continue
			}
			pm, declared := p.Params[name]
			cp, known := comp.Props[prop]
			switch {
			case !declared:
				return p, fmt.Errorf("blocks[%d].%s: 선언하지 않은 매개변수 %q. params 에 넣는다", i, prop, name)
			case !known:
				return p, fmt.Errorf("blocks[%d]: %s 에 없는 속성 %q", i, typ, prop)
			case pm.Kind != cp.Kind:
				return p, fmt.Errorf("blocks[%d].%s: 매개변수 %s 는 kind %s 인데 이 자리는 %s", i, prop, name, pm.Kind, cp.Kind)
			}
			p.sites[name] = append(p.sites[name], site{i, typ, prop, cp})
		}
	}
	for _, name := range sortedKeys(p.Params) {
		if len(p.sites[name]) == 0 {
			return p, fmt.Errorf("매개변수 %s 를 선언했는데 blocks 에서 안 쓴다. 빼거나 {\"$param\": %q} 로 쓴다", name, name)
		}
		pm := p.Params[name]
		for _, v := range []struct {
			what string
			raw  json.RawMessage
		}{{"sample", pm.Sample}, {"default", pm.Default}} {
			if v.raw == nil {
				continue
			}
			if err := p.checkValue(name, v.raw); err != nil {
				return p, fmt.Errorf("params.%s.%s: %v", name, v.what, err)
			}
		}
	}
	// expand with sample values and run the base part checks as is
	blocks, err := p.fill(func(name string) (json.RawMessage, bool) { return p.SampleValue(name), true })
	if err != nil {
		return p, err
	}
	for i, b := range blocks {
		if err := validateBlock(b); err != nil {
			return p, fmt.Errorf("blocks[%d](예시 값으로 펼침): %v", i, err)
		}
	}
	return p, nil
}

func strictParamKeys(params json.RawMessage, name string) error {
	var all map[string]map[string]json.RawMessage
	json.Unmarshal(params, &all)
	for k := range all[name] {
		if !slices.Contains([]string{"kind", "required", "default", "sample"}, k) {
			return fmt.Errorf("params.%s 에 없는 속성 %q. kind, required, default, sample 만 쓴다", name, k)
		}
	}
	return nil
}

// checkValue checks the value against the property rules of every slot where the parameter is used.
func (p Part) checkValue(name string, v json.RawMessage) error {
	for _, s := range p.sites[name] {
		if err := checkKind(s.prop, s.p, v); err != nil {
			return fmt.Errorf("%s.%s 자리에 맞지 않는다: %v", s.typ, s.prop, err)
		}
	}
	return nil
}

// SampleValue is the preview value: sample, then default, then the per-kind default example.
func (p Part) SampleValue(name string) json.RawMessage {
	pm := p.Params[name]
	if pm.Sample != nil {
		return pm.Sample
	}
	if pm.Default != nil {
		return pm.Default
	}
	s := p.sites[name][0]
	var v any
	switch pm.Kind {
	case "text":
		v = name
		if s.prop == "url" {
			v = "https://example.com"
		}
	case "number":
		v = 1234
		if r, ok := numberRange[s.prop]; ok {
			v = r.lo
		}
	case "enum":
		v = s.p.Values[0]
	case "bool":
		v = true
	case "color":
		v = cat.Colors[0]
	case "link":
		v = map[string]string{"to": cat.Links.Targets[0]}
	case "items":
		v = sampleItems(s.p.Fields)
	}
	out, _ := json.Marshal(v)
	return out
}

func sampleItems(fields []string) []any {
	out := make([]any, 3)
	for i := range out {
		if len(fields) == 0 {
			out[i] = fmt.Sprintf("예시 %d", i+1)
			continue
		}
		o := map[string]any{}
		for _, f := range []string{"label", "name", "title"} {
			if slices.Contains(fields, f) {
				o[f] = fmt.Sprintf("예시 %d", i+1)
				break
			}
		}
		if slices.Contains(fields, "value") {
			o["value"] = []int{40, 55, 35}[i]
		}
		out[i] = o
	}
	return out
}

// fill returns a copy of blocks with $param replaced by value. If value is false, that property is dropped.
func (p Part) fill(value func(name string) (json.RawMessage, bool)) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, len(p.Blocks))
	for i, b := range p.Blocks {
		var fields map[string]json.RawMessage
		json.Unmarshal(b, &fields)
		changed := false
		for prop, v := range fields {
			name, ok := paramRef(v)
			if !ok {
				continue
			}
			changed = true
			if nv, ok := value(name); ok {
				fields[prop] = nv
			} else {
				delete(fields, prop)
			}
		}
		if !changed {
			out[i] = b
			continue
		}
		raw, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		out[i] = raw
	}
	return out, nil
}

// Preview is blocks expanded with sample values.
func (p Part) Preview() []json.RawMessage {
	blocks, _ := p.fill(func(name string) (json.RawMessage, bool) { return p.SampleValue(name), true })
	return blocks
}

// Uses returns the assembled part ids the spec calls, without duplicates.
func Uses(raw []byte) []string {
	var s struct {
		Blocks []struct {
			Type string `json:"type"`
			Part string `json:"part"`
		} `json:"blocks"`
	}
	json.Unmarshal(raw, &s)
	var out []string
	for _, b := range s.Blocks {
		if b.Type == "use" && !slices.Contains(out, b.Part) {
			out = append(out, b.Part)
		}
	}
	return out
}

// Expand expands the spec's use into assembled-part blocks and Validates the result. Returns raw as is if there is no use.
func Expand(raw []byte, parts map[string]Part) ([]byte, Screen, error) {
	if len(raw) > cat.Limits.MaxSpecBytes {
		return raw, Screen{}, fmt.Errorf("설계도가 %d바이트를 넘는다", cat.Limits.MaxSpecBytes)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil || top == nil {
		return raw, Screen{}, fmt.Errorf("JSON 객체가 아니다")
	}
	var blocks []json.RawMessage
	if json.Unmarshal(top["blocks"], &blocks) != nil || len(Uses(raw)) == 0 {
		s, err := Validate(raw)
		return raw, s, err
	}
	var out []json.RawMessage
	for i, b := range blocks {
		var x struct {
			Type string `json:"type"`
		}
		json.Unmarshal(b, &x)
		if x.Type != "use" {
			out = append(out, b)
			continue
		}
		got, err := expandUse(b, parts)
		if err != nil {
			return raw, Screen{}, fmt.Errorf("blocks[%d]: %v", i, err)
		}
		out = append(out, got...)
	}
	if len(out) > cat.Limits.MaxBlocks {
		return raw, Screen{}, fmt.Errorf("조립 부품을 펼치면 blocks 가 %d개라 %d개를 넘는다", len(out), cat.Limits.MaxBlocks)
	}
	top["blocks"], _ = json.Marshal(out)
	exp, err := json.Marshal(top)
	if err != nil {
		return raw, Screen{}, err
	}
	s, err := Validate(exp)
	if err != nil {
		return raw, s, fmt.Errorf("조립 부품을 펼친 설계도: %v", err)
	}
	return exp, s, nil
}

func expandUse(b json.RawMessage, parts map[string]Part) ([]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	json.Unmarshal(b, &fields)
	for k := range fields {
		if k != "type" && k != "part" && k != "with" {
			return nil, fmt.Errorf("use 에 없는 속성 %q. part, with 만 쓴다", k)
		}
	}
	var id string
	if json.Unmarshal(fields["part"], &id) != nil || id == "" {
		return nil, fmt.Errorf("use 에 part(조립 부품 id) 가 필요하다")
	}
	p, ok := parts[id]
	if !ok {
		return nil, fmt.Errorf("없는 조립 부품 %s. ui.part.put 으로 먼저 올린다", id)
	}
	with := map[string]json.RawMessage{}
	if w, ok := fields["with"]; ok && (json.Unmarshal(w, &with) != nil || with == nil) {
		return nil, fmt.Errorf("use.with 는 {매개변수: 값} 객체여야 한다")
	}
	for _, name := range sortedKeys(with) {
		if _, ok := p.Params[name]; !ok {
			return nil, fmt.Errorf("use %s.with: 없는 매개변수 %q. %v 중에서 쓴다", id, name, sortedKeys(p.Params))
		}
		if err := p.checkValue(name, with[name]); err != nil {
			return nil, fmt.Errorf("use %s.with.%s: %v", id, name, err)
		}
	}
	for _, name := range sortedKeys(p.Params) {
		if _, ok := with[name]; p.Params[name].Required && !ok {
			return nil, fmt.Errorf("use %s.with 에 %s 가 필요하다", id, name)
		}
	}
	return p.fill(func(name string) (json.RawMessage, bool) {
		if v, ok := with[name]; ok {
			return v, true
		}
		if d := p.Params[name].Default; d != nil {
			return d, true
		}
		return nil, false
	})
}

// ExpandStored expands a stored spec into the form given to the app. Returns the original if it cannot expand.
func ExpandStored(raw []byte, parts map[string]Part) []byte {
	if len(Uses(raw)) == 0 {
		return raw
	}
	exp, _, err := Expand(raw, parts)
	if err != nil {
		return raw
	}
	var buf bytes.Buffer
	if json.Compact(&buf, exp) != nil {
		return exp
	}
	return buf.Bytes()
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
