package hermesbridge

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Ported from script_info of the personal dashboard script. It only reads the script and never runs it.

type ScriptModule struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

type ScriptInfo struct {
	Summary string         `json:"summary"`
	Body    string         `json:"body"`
	Sources []string       `json:"sources"`
	Modules []ScriptModule `json:"modules"`
	Files   []string       `json:"files"`
}

var (
	pyDoc      = regexp.MustCompile(`(?s)^\s*(?:#[^\n]*\n\s*)*(?:"""(.*?)"""|'''(.*?)''')`)
	pyImport   = regexp.MustCompile(`(?m)^\s*(?:from\s+([\w.]+)\s+import|import\s+([\w.]+))`)
	urlPattern = regexp.MustCompile(`https?://[^\s"'()<>]+`)
	filePat    = regexp.MustCompile(`["']([\w.-]+\.(?:json|md|txt|csv))["']`)
	secretHint = regexp.MustCompile(`(?i)token|secret|api[_-]?key|webhook|password`)
)

func readText(path string) string {
	b, err := readHead(path, 256<<10)
	if err != nil {
		return ""
	}
	return string(b)
}

// docParts splits a docstring into (first-line summary, rest), dropping a leading "filename:" on the first line.
func docParts(doc, name string) (string, string) {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(doc), "\n") {
		lines = append(lines, strings.TrimSpace(l))
	}
	if len(lines) == 0 || lines[0] == "" {
		return "", ""
	}
	head := strings.TrimSpace(strings.TrimPrefix(lines[0], name+":"))
	var rest []string
	for _, l := range lines[1:] {
		if l == "" && (len(rest) == 0 || rest[len(rest)-1] == "") {
			continue
		}
		rest = append(rest, l)
	}
	return head, strings.TrimSpace(strings.Join(rest, "\n")) // keep line breaks and render as markdown
}

func moduleDoc(src string) string {
	m := pyDoc.FindStringSubmatch(src)
	if m == nil {
		return ""
	}
	return m[1] + m[2]
}

func scriptInfo(home, script string) *ScriptInfo {
	if !filepath.IsLocal(script) {
		return nil
	}
	folder := filepath.Join(home, "scripts")
	src := readText(filepath.Join(folder, script))
	if src == "" {
		return nil
	}
	info := &ScriptInfo{Sources: []string{}, Modules: []ScriptModule{}, Files: []string{}}
	isPy := strings.HasSuffix(script, ".py")
	if isPy {
		info.Summary, info.Body = docParts(moduleDoc(src), script)
	}

	texts := []string{src}
	if isPy {
		var local []string
		for _, m := range pyImport.FindAllStringSubmatch(src, -1) {
			name := m[1] + m[2]
			if _, err := os.Stat(filepath.Join(folder, name+".py")); err == nil && !slices.Contains(local, name) {
				local = append(local, name)
			}
		}
		for _, name := range local {
			modSrc := readText(filepath.Join(folder, name+".py"))
			summary, _ := docParts(moduleDoc(modSrc), name+".py")
			info.Modules = append(info.Modules, ScriptModule{name + ".py", summary})
			texts = append(texts, modSrc)
		}
	}

	seen := map[string]bool{}
	for _, text := range texts {
		for _, u := range urlPattern.FindAllString(text, -1) {
			if i := strings.IndexAny(u, "?{"); i >= 0 {
				u = u[:i]
			}
			u = strings.TrimRight(u, "/.,")
			if secretHint.MatchString(u) || seen[u] || strings.ContainsAny(u, `\[`) {
				continue
			}
			seen[u] = true
			info.Sources = append(info.Sources, u)
		}
	}

	files := map[string]bool{}
	for _, m := range filePat.FindAllStringSubmatch(src, -1) {
		files[m[1]] = true
	}
	var prompt string
	for f := range files {
		if strings.HasSuffix(f, ".md") {
			if _, err := os.Stat(filepath.Join(folder, f)); err == nil {
				prompt = f
				continue
			}
		}
		info.Files = append(info.Files, f)
	}
	slices.Sort(info.Files)
	if info.Summary == "" && prompt != "" {
		first, _, _ := strings.Cut(strings.TrimSpace(readText(filepath.Join(folder, prompt))), "\n")
		info.Summary = firstSentence(first)
	}
	return info
}
