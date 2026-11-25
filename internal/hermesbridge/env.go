package hermesbridge

import (
	"bufio"
	"os"
	"strings"
)

// FromEnv fills the Hermes-side settings from environment variables and the Hermes folder. The caller supplies the deck address and agent token.
// If API_SERVER_KEY is not in the environment, only that one line is read from <HERMES_HOME>/.env.
func FromEnv() Config {
	cfg := Config{
		HermesURL:  os.Getenv("HERMES_URL"),
		HermesKey:  os.Getenv("API_SERVER_KEY"),
		HermesHome: os.Getenv("HERMES_HOME"),
		UIAddr:     "127.0.0.1:7701",
	}
	if cfg.HermesHome == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cfg.HermesHome = home + "/.hermes"
		}
	}
	if cfg.HermesURL == "" {
		cfg.HermesURL = "http://127.0.0.1:8642"
	}
	if cfg.HermesKey == "" && cfg.HermesHome != "" {
		cfg.HermesKey = envFileValue(cfg.HermesHome+"/.env", "API_SERVER_KEY")
	}
	if v, set := os.LookupEnv("BRIDGE_UI_ADDR"); set { // empty means the local address is not opened
		cfg.UIAddr = v
	}
	return cfg
}

// envFileValue reads the last value of key from a dotenv file. It allows an export prefix and spaces around =, and strips only matching quotes.
func envFileValue(path, key string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	val := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if rest, ok := strings.CutPrefix(line, "export"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			line = strings.TrimSpace(rest)
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		v = strings.TrimSpace(v)
		if v != "" && (v[0] == '"' || v[0] == '\'') {
			if end := strings.IndexByte(v[1:], v[0]); end >= 0 { // only inside matching quotes; a trailing comment is dropped
				v = v[1 : end+1]
			}
		} else if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		val = v
	}
	return val
}

// FileKey reads only API_SERVER_KEY written in <home>/.env; the environment is not consulted.
func FileKey(home string) string {
	return envFileValue(home+"/.env", "API_SERVER_KEY")
}
