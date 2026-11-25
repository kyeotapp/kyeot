package hermesbridge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

// auxRole maps a name under Hermes auxiliary to a role key the app knows; unknown names stay as they are.
var auxRole = map[string]string{"title_generation": "title"}

// hermesModels extracts per-role models from config.yaml, with chat first.
// It does not parse all of YAML; it only looks at model.default, delegation.model and auxiliary.<name>.model.
func hermesModels(config string) []protocol.AgentModel {
	var out []protocol.AgentModel
	top, aux := "", ""
	for _, line := range strings.Split(config, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		key, val, _ := strings.Cut(trimmed, ":")
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		switch {
		case indent == 0:
			top, aux = key, ""
		case top == "model" && indent == 2 && key == "default" && val != "":
			out = append([]protocol.AgentModel{{Role: "chat", Name: val}}, out...)
		case top == "delegation" && indent == 2 && key == "model" && val != "":
			out = append(out, protocol.AgentModel{Role: "delegate", Name: val})
		case top == "auxiliary" && indent == 2:
			aux = key
		case top == "auxiliary" && indent == 4 && aux != "" && key == "model" && val != "":
			role := auxRole[aux]
			if role == "" {
				role = aux
			}
			out = append(out, protocol.AgentModel{Role: role, Name: val})
		}
	}
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

var versionRe = regexp.MustCompile(`v?(\d[\w.\-]*)`)

// hermesVersion extracts the version from `hermes --version`. If there is no version, as in "vunknown (2026.9.24)", it uses the date in parentheses.
func hermesVersion(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "hermes", "--version").Output()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if i := strings.Index(line, "("); i >= 0 && strings.Contains(line, "unknown") {
		line = line[i:]
	}
	if m := versionRe.FindStringSubmatch(line); m != nil && len(m[1]) <= 40 {
		return m[1]
	}
	return ""
}

// sendHello tells the server the agent kind and models right after connecting.
// Running a routine now works only when jobs.json is readable, so it is declared only then. A declaration means missing features are absent, so list everything supported
func (b *Bridge) sendHello(ctx context.Context) {
	info := protocol.AgentMessage{Type: "hello", Kind: "Hermes", Version: hermesVersion(ctx)}
	if raw, err := os.ReadFile(filepath.Join(b.cfg.HermesHome, "config.yaml")); err == nil {
		info.Models = hermesModels(string(raw))
	}
	if b.cfg.HermesHome != "" {
		info.Features = []string{protocol.FeatureRoutines, protocol.FeatureCosts, protocol.FeatureUI, protocol.FeatureRunRoutine, protocol.FeatureManageRoutines}
	}
	if len(b.sensors) > 0 {
		if b.cfg.HermesHome == "" {
			info.Features = append(info.Features, protocol.FeatureUI)
		}
		info.Features = append(info.Features, protocol.FeatureDataRefresh)
		for _, s := range b.sensors {
			info.Features = append(info.Features, s.feature)
		}
	}
	b.send(info)
}
