package hermesbridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Profile is one Hermes profile on this computer. Key values live only in Config.
type Profile struct {
	ID      string // "default" or a folder name under profiles/
	Name    string // display_name from profile.yaml, else ID
	Model   string
	Found   bool
	HasKey  bool
	Gateway string // running, stopped; "" if unknown
	Config  Config
}

// Hermes hermes_constants.PROFILE_ID_RE, _PROFILE_IDENTITY_MARKERS
var (
	profileID      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	profileMarkers = []string{"config.yaml", ".env", "SOUL.md", "profile.yaml", "auth.json", "state.db"}
)

// Profiles finds default and the live profiles under <home>/profiles.
// With the old HERMES_HOME setting it returns only that one.
// A non-default profile attaches through /p/<id> of the default gateway (multiplex), and its key is read only from that profile's .env.
func Profiles() []Profile {
	def := FromEnv()
	legacy := os.Getenv("HERMES_HOME") != ""
	p := Profile{ID: "default", Name: "default", Config: def, HasKey: def.HermesKey != ""}
	fi, err := os.Stat(def.HermesHome)
	homeOK := def.HermesHome != "" && err == nil && fi.IsDir()
	p.Found = p.HasKey || homeOK
	if !homeOK {
		return []Profile{p}
	}
	p.Name = cmpOr(displayName(def.HermesHome), "default")
	p.Model = profileModel(def.HermesHome)
	if legacy {
		return []Profile{p}
	}
	hostUp := gatewayAlive(def.HermesHome)
	p.Gateway = upDown(hostUp)
	out := []Profile{p}
	root := filepath.Join(def.HermesHome, "profiles")
	entries, _ := os.ReadDir(root)
	served := servedProfiles(def.HermesHome)
	for _, e := range entries {
		id := e.Name()
		home := filepath.Join(root, id)
		if !e.IsDir() || id == "default" || !profileID.MatchString(id) || !hasMarker(home) || exists(filepath.Join(root, ".deleted", id)) {
			continue
		}
		cfg := Config{HermesKey: FileKey(home), HermesHome: home}
		var up bool
		if standalone(home) {
			port := cmpOr(envFileValue(home+"/.env", "API_SERVER_PORT"), "8642")
			cfg.HermesURL = "http://" + cmpOr(envFileValue(home+"/.env", "API_SERVER_HOST"), "127.0.0.1") + ":" + port
			up = gatewayAlive(home)
		} else {
			cfg.HermesURL = strings.TrimRight(def.HermesURL, "/") + "/p/" + id
			up = hostUp && !exists(filepath.Join(home, "gateway.parked")) && slices.Contains(served, id)
		}
		out = append(out, Profile{
			ID: id, Name: cmpOr(displayName(home), id), Model: profileModel(home),
			Found: true, HasKey: cfg.HermesKey != "", Gateway: upDown(up), Config: cfg,
		})
	}
	return out
}

func upDown(up bool) string {
	if up {
		return "running"
	}
	return "stopped"
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func hasMarker(home string) bool {
	return slices.ContainsFunc(profileMarkers, func(m string) bool { return exists(filepath.Join(home, m)) })
}

// gatewayAlive checks whether the process in <home>/gateway.pid is alive; the file is removed when it stops.
func gatewayAlive(home string) bool {
	raw, err := os.ReadFile(filepath.Join(home, "gateway.pid"))
	if err != nil {
		return false
	}
	var rec struct {
		PID int `json:"pid"`
	}
	pid := 0
	if json.Unmarshal(raw, &rec) == nil {
		pid = rec.PID
	} else {
		pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
	}
	if pid <= 0 {
		return false
	}
	return processAlive(pid)
}

func servedProfiles(home string) []string {
	raw, err := os.ReadFile(filepath.Join(home, "gateway_state.json"))
	if err != nil {
		return nil
	}
	var st struct {
		Served []string `json:"served_profiles"`
	}
	json.Unmarshal(raw, &st)
	return st.Served
}

// yamlScalar reads an unindented "key: value" line. With a section, it reads the key indented two spaces under it.
func yamlScalar(path, section, key string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	top := ""
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		k, v, _ := strings.Cut(trimmed, ":")
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch {
		case indent == 0 && section == "" && k == key:
			return v
		case indent == 0:
			top = k
		case indent == 2 && top == section && k == key:
			return v
		}
	}
	return ""
}

func displayName(home string) string {
	return yamlScalar(filepath.Join(home, "profile.yaml"), "", "display_name")
}

func profileModel(home string) string {
	path := filepath.Join(home, "config.yaml")
	if m := yamlScalar(path, "model", "default"); m != "" {
		return m
	}
	if m := yamlScalar(path, "", "model"); m != "" {
		return m
	}
	return yamlScalar(path, "model", "model")
}

func standalone(home string) bool {
	switch strings.ToLower(yamlScalar(filepath.Join(home, "config.yaml"), "gateway", "standalone")) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}
