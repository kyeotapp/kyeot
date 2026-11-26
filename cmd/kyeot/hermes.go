package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	deckui "github.com/kyeotapp/kyeot/integrations/hermes/deck-ui"
	"github.com/kyeotapp/kyeot/internal/hermesbridge"
	"github.com/kyeotapp/kyeot/internal/server"
)

// startHermes finds this computer's Hermes profiles and tells the server. With KYEOT_HERMES=off (0, false, no) it does not look.
// If an agent id file exists (default: KYEOT_HERMES_AGENT_FILE, hermes-agent in the data folder by default; other profiles append .<profile>), consent is assumed and it connects at once. Otherwise it connects when the app's connect is tapped.
func startHermes(srv *server.Server, addr, dataDir string) {
	if isOff(getenv("HERMES")) {
		return
	}
	idPath := env("HERMES_AGENT_FILE", env("HERMES_TOKEN_FILE", filepath.Join(dataDir, "hermes-agent")))
	name := env("HERMES_NAME", "Hermes")
	var router *hermesbridge.UIRouter
	if ui := hermesbridge.FromEnv().UIAddr; ui != "" {
		router = hermesbridge.NewUIRouter(ui)
	}
	srv.SetLocalHermesList(func() []server.LocalHermes {
		var out []server.LocalHermes
		for _, p := range hermesbridge.Profiles() {
			out = append(out, localProfile(p, addr, idPath, name, router))
		}
		return out
	})
	if !slices.ContainsFunc(hermesbridge.Profiles(), func(p hermesbridge.Profile) bool { return p.HasKey }) {
		log.Print("Hermes adapter off: API_SERVER_KEY not found (environment, HERMES_HOME/.env, or the profile's .env)")
		return
	}
	consented, err := srv.ResumeLocalHermes()
	switch {
	case err != nil:
		log.Printf("Hermes adapter off: %v", err)
	case !consented:
		log.Print("Hermes found. It connects when you connect from the app")
	}
}

// localProfile turns one profile into the server's LocalHermes. The on-Mac screen address shares one router:
// default uses /ui/… (the deck-ui skill knows that address literally), other profiles use /h/<key>/ui/… and the skill is installed with that address.
func localProfile(p hermesbridge.Profile, addr, idPath, defaultName string, router *hermesbridge.UIRouter) server.LocalHermes {
	cfg := p.Config
	cfg.UIRouter = router
	l := server.LocalHermes{ID: p.ID, Name: defaultName, Label: p.Name, Model: p.Model, Gateway: p.Gateway, IDPath: idPath, NoUI: router == nil}
	if p.ID != "default" {
		l.Name, l.IDPath = p.Name, idPath+"."+p.ID
	}
	l.Find = func() (bool, bool) { return p.Found, p.HasKey }
	l.Probe = func(ctx context.Context) string {
		pr := probeHermes(ctx, cfg)
		if pr == "unreachable" && p.Gateway == "stopped" {
			return "gateway_off"
		}
		return pr
	}
	keyPath := l.IDPath + ".ui-key"
	l.Start = func(ctx context.Context, token string) { runHermes(ctx, addr, token, profileUI(cfg, p.ID, keyPath)) }
	return l
}

// profileUI adds a screen key to non-default profiles. If the key cannot be made, screen upload is turned off, so it never leaks to the default address.
func profileUI(cfg hermesbridge.Config, id, keyPath string) hermesbridge.Config {
	if id == "default" || cfg.UIRouter == nil {
		return cfg
	}
	cfg.UIProfile = true
	key, err := uiKey(keyPath)
	if err != nil {
		log.Printf("Hermes %s: could not create screen key, screen upload is off: %v", id, err)
		cfg.UIRouter, cfg.UIAddr, key = nil, "", ""
	}
	cfg.UIKey = key
	return cfg
}

// uiKey reads or creates the profile's screen address key and keeps it at 600.
func uiKey(path string) (string, error) {
	if raw, err := os.ReadFile(path); err == nil {
		if k := strings.TrimSpace(string(raw)); hermesbridge.UIKeyPattern.MatchString(k) {
			return k, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	k := base64.RawURLEncoding.EncodeToString(buf)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	return k, os.WriteFile(path, []byte(k+"\n"), 0o600)
}

// probeHermes checks whether the Hermes API is reachable and returns a problem code.
func probeHermes(ctx context.Context, cfg hermesbridge.Config) string {
	err := hermesbridge.Ping(ctx, cfg)
	switch {
	case err == nil:
		return ""
	case errors.Is(err, hermesbridge.ErrBadKey):
		return "bad_key"
	default:
		log.Printf("Hermes unreachable: %v", err)
		return "unreachable"
	}
}

// runHermes connects Hermes on the same computer. The server measures sensors itself, so the bridge does not start them.
func runHermes(ctx context.Context, addr, token string, cfg hermesbridge.Config) {
	cfg.DeckURL = localURL(addr)
	cfg.AgentToken = token
	log.Printf("Hermes adapter: %s → %s", redact(cfg.HermesURL), cfg.DeckURL)
	installSkill(cfg)
	if err := hermesbridge.New(cfg).Run(ctx); err != nil && ctx.Err() == nil {
		log.Printf("Hermes adapter stopped: %v", err)
	}
}

// installSkill installs the deck-ui skill on Hermes that opens the screen address (7701), or replaces an outdated version. A failure does not stop the conversation.
// A profile gets it in its own profile folder, with its own key address.
func installSkill(cfg hermesbridge.Config) {
	if cfg.UIAddr == "" {
		return
	}
	switch res, err := deckui.InstallKey(cfg.HermesHome, cfg.UIKey); {
	case err != nil:
		log.Printf("could not install the deck-ui skill: %v", err)
	case res == deckui.Installed || res == deckui.Updated:
		log.Printf("deck-ui skill %s version %s: %s", res, deckui.Version(deckui.Skill), deckui.Path(cfg.HermesHome))
	}
}

func isOff(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "0", "false", "no":
		return true
	}
	return false
}

// latestURL is where the server reads the newest release number once a day. KYEOT_UPDATE_CHECK=off turns it off.
func latestURL() string {
	if isOff(getenv("UPDATE_CHECK")) {
		return ""
	}
	return server.DefaultLatestURL
}

// sensorDir is the sensor (network and disk) record folder. With KYEOT_SENSORS=off (0, false, no) it is empty, so sensors stay off.
func sensorDir(dataDir string) string {
	if dataDir == "" || isOff(getenv("SENSORS")) {
		return ""
	}
	return filepath.Join(dataDir, "sensors")
}

// localURL turns the address the server listens on into one to call from the same computer. If listening everywhere, loopback.
func localURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// redact hides account info in an address.
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(주소 형식 오류)"
	}
	return u.Redacted()
}
