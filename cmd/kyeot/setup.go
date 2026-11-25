package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kyeotapp/kyeot/internal/server"
)

// if empty, only this device, Tailscale and private networks are accepted. To open everything, KYEOT_NETS=all
const defaultNets = "loopback,tailscale,lan"

// configDir is kyeot's config and data folder. On a Mac, ~/Library/Application Support/kyeot
func configDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "kyeot"), nil
}

func allowNets() ([]*net.IPNet, error) {
	return server.ParseNets(env("NETS", defaultNets))
}

// serverNets are the networks the server accepts. If the in-server Hermes adapter is on, loopback is always added.
func serverNets() ([]*net.IPNet, error) {
	nets, err := allowNets()
	if err != nil || isOff(getenv("HERMES")) {
		return nets, err
	}
	nets, added := withLoopback(nets)
	if added {
		log.Print("warning: KYEOT_NETS had no loopback, so it was added. It is the path the same-computer Hermes adapter connects through. To block it, KYEOT_HERMES=off")
	}
	return nets, nil
}

// withLoopback adds loopback to the accepted networks if missing. It is the path the same-computer Hermes adapter connects through.
func withLoopback(nets []*net.IPNet) ([]*net.IPNet, bool) {
	if slices.ContainsFunc(nets, func(n *net.IPNet) bool { return n.Contains(net.IPv4(127, 0, 0, 1)) }) {
		return nets, false
	}
	lb, _ := server.ParseNets("loopback")
	return append(nets, lb...), true
}

// Old default DB location, relative to the working directory. Only warned about, never used.
const legacyDB = "data/kyeot.db"

// dbFile is KYEOT_DB, or kyeot.db in the config folder. The folder is created 0700.
// A DB in the working folder is never picked up: a stray data/ there once opened another server's DB.
func dbFile() (string, error) {
	path := getenv("DB")
	if path == "" {
		dir, err := configDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(dir, "kyeot.db")
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			if _, err := os.Stat(legacyDB); err == nil {
				abs, _ := filepath.Abs(legacyDB)
				log.Printf("warning: not using %s in the working folder; the DB is %s. To use that data, start with KYEOT_DB=%s", abs, path, abs)
			}
		}
	}
	return path, os.MkdirAll(filepath.Dir(path), 0o700)
}

// tokenFiles are the candidate owner token files. The first is where a new one is created, the rest are old locations.
func tokenFiles() ([]string, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	files := []string{filepath.Join(dir, "owner-token")}
	if home, err := os.UserHomeDir(); err == nil {
		for _, old := range []string{"kyeot", "agent-deck"} {
			if f := filepath.Join(home, ".config", old, "owner-token"); f != files[0] {
				files = append(files, f)
			}
		}
	}
	return files, nil
}

// readOwnerToken reads from the environment variable, else from the token file. fs.ErrNotExist if neither.
func readOwnerToken() (string, error) {
	token := getenv("OWNER_TOKEN")
	if token == "" {
		files, err := tokenFiles()
		if err != nil {
			return "", err
		}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return "", err
			}
			if token = strings.TrimSpace(string(b)); token == "" {
				return "", fmt.Errorf("소유자 토큰 파일 %s 이 비어 있다. 지우면 다음 시작 때 새로 만든다 (앱 연결도 다시 해야 한다)", f)
			}
			break
		}
		if token == "" {
			return "", fmt.Errorf("소유자 토큰이 없다 (KYEOT_OWNER_TOKEN 또는 %s): %w", files[0], fs.ErrNotExist)
		}
	}
	if len(token) < 16 {
		return "", errors.New("소유자 토큰이 16자 미만이다")
	}
	return token, nil
}

// ownerToken reads the owner token, creating and saving one if missing. created is true if it made one.
func ownerToken() (token string, created bool, err error) {
	token, err = readOwnerToken()
	if !errors.Is(err, fs.ErrNotExist) {
		return token, false, err
	}
	files, err := tokenFiles()
	if err != nil {
		return "", false, err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", false, err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	err = createPrivate(files[0], token+"\n")
	if errors.Is(err, fs.ErrExist) { // another process that started at the same time created it first
		token, err = readOwnerToken()
		return token, false, err
	}
	if err != nil {
		return "", false, err
	}
	return token, true, nil
}

// createPrivate writes a temp file with folder 700 and file 600 and links it in. fs.ErrExist if it already exists.
func createPrivate(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".owner-token-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Link(f.Name(), path)
}
