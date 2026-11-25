package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kyeotapp/kyeot/internal/store"
)

const (
	bundleManifest = "kyeot-backup.json"
	bundleDB       = "kyeot.db"
	bundleFormat   = 1
)

// Folders in the data folder (next to the DB) that go into the backup. Link-preview cache, daily backup, connect.json and the signing private key are left out.
var bundleDirs = []string{"uploads", "sensors", "updates/ios", "updates/android", "updates/files"}

var bundleKeys = []string{"owner-token", "uploads-key"}

type manifest struct {
	Format  int      `json:"format"`
	Version string   `json:"version"`
	Schema  int      `json:"schema"`
	Created string   `json:"created"`
	Keys    []string `json:"keys"`
}

// backupPlan holds the paths backup and restore read and write. Tests pass a temp folder.
type backupPlan struct {
	dbPath      string
	dataDir     string
	keys        map[string]string // name inside the bundle → file
	running     func() string     // reason if the server is running
	lock        func() (func(), error)
	interactive func() bool
	now         func() time.Time
}

func realBackupPlan() (backupPlan, error) {
	dbPath, err := dbFile()
	if err != nil {
		return backupPlan{}, err
	}
	p := backupPlan{dbPath: dbPath, dataDir: filepath.Dir(dbPath), keys: map[string]string{}, interactive: func() bool { return stdinIsTerminal() }, now: time.Now}
	if getenv("OWNER_TOKEN") == "" {
		if files, err := tokenFiles(); err == nil {
			p.keys["owner-token"] = files[0]
		}
	}
	if f, err := uploadKeyFile(); err == nil {
		p.keys["uploads-key"] = f
	}
	addr := env("ADDR", defaultAddr)
	p.running = func() string { return serverRunning(serverHost(), addr) }
	p.lock = func() (func(), error) { return lockData(dbPath) }
	return p, nil
}

// serverRunning checks whether a server runs, via a service registration or a listening port.
func serverRunning(h host, addr string) string {
	if registered(h) {
		return tr("서버가 서비스로 등록돼 있다. kyeot service uninstall 로 먼저 내린다")
	}
	hostPart, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	if hostPart == "" || hostPart == "0.0.0.0" || hostPart == "::" {
		hostPart = "127.0.0.1"
	}
	if c, err := net.DialTimeout("tcp", net.JoinHostPort(hostPart, port), time.Second); err == nil {
		c.Close()
		return fmt.Sprintf(tr("서버가 %s 에서 돌고 있다. 먼저 끈다"), addr)
	}
	return ""
}

func runBackup(p backupPlan, args []string, out io.Writer) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return errors.New(tr("쓰는 법: kyeot backup <파일>"))
	}
	dst := args[0]
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf(tr("%s 가 이미 있다. 다른 이름을 준다"), dst)
	}
	if _, err := os.Stat(p.dbPath); err != nil {
		return fmt.Errorf(tr("백업할 데이터가 없다: %w"), err)
	}
	m, files, err := writeBundle(p, dst)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, tr("백업했다: %s (DB 구조 %d, 파일 %d개, 키 %d개)\n"), dst, m.Schema, files, len(m.Keys))
	if slices.Contains(m.Keys, "owner-token") {
		fmt.Fprintln(out, tr("이 파일로 앱 연결 없이 서버에 들어갈 수 있다. 남에게 주지 않는다"))
	}
	return nil
}

// writeBundle writes the DB copy, data folder and keys into one tar.gz (600). dst exists only after everything is written.
func writeBundle(p backupPlan, dst string) (manifest, int, error) {
	work, err := os.MkdirTemp(p.dataDir, ".backup-")
	if err != nil {
		return manifest{}, 0, err
	}
	defer os.RemoveAll(work)
	snap := filepath.Join(work, bundleDB)
	schema := 0
	if regular(p.dbPath) { // the pre-restore safety bundle is made even without a DB; kyeot backup checked the DB earlier
		if schema, err = store.Snapshot(p.dbPath, snap); err != nil { // before attachments: attachments the DB points to are already on disk
			return manifest{}, 0, err
		}
	}
	m := manifest{Format: bundleFormat, Version: version, Schema: schema, Created: p.now().UTC().Format(time.RFC3339)}
	for _, k := range bundleKeys {
		if f, ok := p.keys[k]; ok && regular(f) {
			m.Keys = append(m.Keys, k)
		}
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".kyeot-backup-*")
	if err != nil {
		return manifest{}, 0, err
	}
	done := false
	defer func() {
		tmp.Close()
		if !done {
			os.Remove(tmp.Name())
		}
	}()
	gz := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gz)
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := addBytes(tw, bundleManifest, mb, p.now()); err != nil {
		return manifest{}, 0, err
	}
	if regular(snap) {
		if err := addFile(tw, bundleDB, snap); err != nil {
			return manifest{}, 0, err
		}
	}
	files := 0
	for _, d := range bundleDirs {
		root := filepath.Join(p.dataDir, filepath.FromSlash(d))
		err := filepath.WalkDir(root, func(f string, e fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil || !e.Type().IsRegular() {
				return err
			}
			rel, err := filepath.Rel(p.dataDir, f)
			if err != nil {
				return err
			}
			err = addFile(tw, "data/"+filepath.ToSlash(rel), f)
			if errors.Is(err, fs.ErrNotExist) { // attachment removed during the scan; nothing was written to the bundle before opening
				return nil
			}
			if err == nil {
				files++
			}
			return err
		})
		if err != nil {
			return manifest{}, 0, err
		}
	}
	entries, err := os.ReadDir(p.dataDir)
	if err != nil {
		return manifest{}, 0, err
	}
	for _, e := range entries {
		if agentIDFile(e.Name()) && e.Type().IsRegular() {
			if err := addFile(tw, "data/"+e.Name(), filepath.Join(p.dataDir, e.Name())); err != nil {
				return manifest{}, 0, err
			}
		}
	}
	for _, k := range m.Keys {
		if err := addFile(tw, "keys/"+k, p.keys[k]); err != nil {
			return manifest{}, 0, err
		}
	}
	if err := tw.Close(); err != nil {
		return manifest{}, 0, err
	}
	if err := gz.Close(); err != nil {
		return manifest{}, 0, err
	}
	if err := tmp.Sync(); err != nil {
		return manifest{}, 0, err
	}
	if _, err := os.Lstat(dst); err == nil {
		return manifest{}, 0, fmt.Errorf(tr("%s 가 이미 있다. 다른 이름을 준다"), dst)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return manifest{}, 0, err
	}
	done = true
	return m, files, nil
}

// agentIDFile reports whether this is the agent id file of the same-computer Hermes (hermes-agent, hermes-agent.<profile>).
func agentIDFile(name string) bool {
	return name == "hermes-agent" || strings.HasPrefix(name, "hermes-agent.")
}

func regular(f string) bool {
	fi, err := os.Lstat(f)
	return err == nil && fi.Mode().IsRegular()
}

func addBytes(tw *tar.Writer, name string, b []byte, mod time.Time) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b)), ModTime: mod, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

func addFile(tw *tar.Writer, name, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: fi.Size(), ModTime: fi.ModTime(), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err = io.CopyN(tw, f, fi.Size()) // a log file growing during the copy is read only up to its size when opened
	return err
}

// bundleName reports whether a name inside the bundle is acceptable. Names that escape the folder are rejected.
func bundleName(name string) bool {
	if name != path.Clean(name) || path.IsAbs(name) || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") {
		return false
	}
	if name == bundleManifest || name == bundleDB {
		return true
	}
	if k, ok := strings.CutPrefix(name, "keys/"); ok {
		return slices.Contains(bundleKeys, k)
	}
	rel, ok := strings.CutPrefix(name, "data/")
	if !ok {
		return false
	}
	if !strings.Contains(rel, "/") {
		return agentIDFile(rel)
	}
	return slices.ContainsFunc(bundleDirs, func(d string) bool { return strings.HasPrefix(rel, d+"/") })
}

// extractBundle unpacks the bundle into stage and returns the manifest. Stops on unknown entries or non-regular files.
func extractBundle(src, stage string) (manifest, error) {
	var m manifest
	f, err := os.Open(src)
	if err != nil {
		return m, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return m, fmt.Errorf(tr("곁 백업 파일이 아니다: %w"), err)
	}
	tr0 := tar.NewReader(gz)
	seen := map[string]bool{}
	for {
		h, err := tr0.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return m, fmt.Errorf(tr("곁 백업 파일이 아니다: %w"), err)
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Typeflag != tar.TypeReg || !bundleName(h.Name) || seen[h.Name] {
			return m, fmt.Errorf(tr("백업 파일 안에 받을 수 없는 항목이 있다: %q"), h.Name)
		}
		seen[h.Name] = true
		dst := filepath.Join(stage, filepath.FromSlash(h.Name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return m, err
		}
		w, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return m, err
		}
		_, err = io.Copy(w, tr0)
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return m, err
		}
	}
	b, err := os.ReadFile(filepath.Join(stage, bundleManifest))
	if err != nil || json.Unmarshal(b, &m) != nil || !seen[bundleDB] {
		return m, errors.New(tr("곁 백업 파일이 아니다(목록이나 DB 가 없다)"))
	}
	if m.Format > bundleFormat {
		return m, errors.New(tr("더 새 판 곁이 만든 백업 형식이다. kyeot 을 업데이트한 뒤 다시 한다"))
	}
	for _, k := range bundleKeys { // if the manifest and the actual keys disagree, stop before deleting anything
		if slices.Contains(m.Keys, k) != seen["keys/"+k] {
			return m, fmt.Errorf(tr("백업 파일 안에 받을 수 없는 항목이 있다: %q"), "keys/"+k)
		}
	}
	if slices.ContainsFunc(m.Keys, func(k string) bool { return !slices.Contains(bundleKeys, k) }) {
		return m, errors.New(tr("곁 백업 파일이 아니다(목록이나 DB 가 없다)"))
	}
	return m, nil
}

func runRestore(p backupPlan, args []string, in io.Reader, out io.Writer) error {
	fsFlags := flag.NewFlagSet("restore", flag.ContinueOnError)
	fsFlags.SetOutput(out)
	yes := fsFlags.Bool("yes", false, tr("묻지 않고 바꾼다"))
	if err := fsFlags.Parse(args); err != nil {
		return err
	}
	if fsFlags.NArg() != 1 {
		return errors.New(tr("쓰는 법: kyeot restore [--yes] <파일>"))
	}
	src := fsFlags.Arg(0)
	if why := p.running(); why != "" {
		return fmt.Errorf(tr("되살리지 않았다: %s"), why)
	}
	if err := os.MkdirAll(p.dataDir, 0o700); err != nil {
		return err
	}
	release, err := p.lock()
	if errors.Is(err, errDataBusy) {
		return fmt.Errorf(tr("되살리지 않았다: %s"), tr("다른 곁 서버나 kyeot restore 가 이 데이터를 쓰고 있다"))
	}
	if err != nil {
		return err
	}
	defer release() // a new server cannot start while the swap runs
	stage, err := os.MkdirTemp(p.dataDir, ".restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	m, err := extractBundle(src, stage)
	if err != nil {
		return err
	}
	ver, err := store.CheckFile(filepath.Join(stage, bundleDB))
	if err != nil {
		return fmt.Errorf(tr("백업 속 DB 를 읽지 못했다: %w"), err)
	}
	if ver > store.SchemaVersion {
		return fmt.Errorf(tr("이 백업은 더 새 판 곁이 만들었다(구조 %d, 이 판은 %d). kyeot 을 업데이트한 뒤 다시 한다"), ver, store.SchemaVersion)
	}
	fmt.Fprintf(out, tr("백업: %s 에 판 %s 가 만듦, DB 구조 %d, 키 %d개\n"), m.Created, m.Version, ver, len(m.Keys))
	if !*yes {
		if p.interactive == nil || !p.interactive() {
			return errors.New(tr("지금 데이터를 바꿀지 물어볼 수 없다. --yes 를 붙여 다시 한다"))
		}
		if !confirm(in, out, tr("이 컴퓨터의 곁 데이터를 백업 내용으로 바꿀까요? 지금 데이터는 먼저 따로 백업해 둔다")) {
			return errors.New(tr("그만뒀다. 아무것도 바꾸지 않았다"))
		}
		if why := p.running(); why != "" { // the server may have started while the prompt was open
			return fmt.Errorf(tr("되살리지 않았다: %s"), why)
		}
	}
	if err := noLinkedDirs(p.dataDir); err != nil {
		return err
	}
	if p.hasData() {
		dir := filepath.Join(p.dataDir, "backups")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		before := filepath.Join(dir, store.RestorePrefix+p.now().Format("20060102-150405")+".tar.gz")
		if _, _, err := writeBundle(p, before); err != nil {
			return fmt.Errorf(tr("지금 데이터를 백업하지 못해 바꾸지 않았다: %w"), err)
		}
		fmt.Fprintf(out, tr("지금 데이터를 먼저 백업했다: %s\n"), before)
		pruneRestoreBundles(dir, before)
	}
	if err := installStaged(p, stage, m); err != nil {
		return fmt.Errorf(tr("바꾸다 멈췄다. 앞의 백업 파일로 다시 되살린다: %w"), err)
	}
	fmt.Fprintln(out, tr("되살렸다. 서버를 띄우면(kyeot service install 또는 kyeot) 옛 구조는 새 구조로 바뀌고 그 전 DB 를 따로 남긴다"))
	if !slices.Contains(m.Keys, "owner-token") {
		fmt.Fprintln(out, tr("백업에 소유자 토큰이 없어 지금 토큰을 그대로 둔다. 폰 앱은 kyeot pair 로 다시 잇는다"))
	}
	return nil
}

// keepRestoreBundles is how many pre-restore bundles are kept. Each bundle holds all attachments.
const keepRestoreBundles = 3

// pruneRestoreBundles deletes the oldest by name (timestamp), keeping the most recent, never the one just made.
func pruneRestoreBundles(dir, keep string) {
	names, _ := filepath.Glob(filepath.Join(dir, store.RestorePrefix+"*.tar.gz"))
	slices.Sort(names)
	for len(names) > keepRestoreBundles {
		if names[0] != keep {
			os.Remove(names[0])
		}
		names = names[1:]
	}
}

// hasData reports whether restore has anything at all to delete or overwrite.
func (p backupPlan) hasData() bool {
	if regular(p.dbPath) {
		return true
	}
	for _, d := range bundleDirs {
		if _, err := os.Lstat(filepath.Join(p.dataDir, filepath.FromSlash(d))); err == nil {
			return true
		}
	}
	for _, f := range p.keys {
		if regular(f) {
			return true
		}
	}
	entries, _ := os.ReadDir(p.dataDir)
	return slices.ContainsFunc(entries, func(e fs.DirEntry) bool { return agentIDFile(e.Name()) })
}

// noLinkedDirs stops if a symlink sits in the path of a folder to delete and write, so nothing outside the data folder is removed.
func noLinkedDirs(dataDir string) error {
	for _, d := range bundleDirs {
		cur := dataDir
		for _, part := range strings.Split(d, "/") {
			cur = filepath.Join(cur, part)
			if fi, err := os.Lstat(cur); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
				return fmt.Errorf(tr("되살리지 않았다: %s"), cur+" → symlink")
			}
		}
	}
	return nil
}

// installStaged deletes the current DB and data folder entries and puts the staged ones in place.
func installStaged(p backupPlan, stage string, m manifest) error {
	for _, f := range []string{p.dbPath, p.dbPath + "-wal", p.dbPath + "-shm"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	for _, d := range bundleDirs {
		if err := os.RemoveAll(filepath.Join(p.dataDir, filepath.FromSlash(d))); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(p.dataDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if agentIDFile(e.Name()) {
			if err := os.Remove(filepath.Join(p.dataDir, e.Name())); err != nil {
				return err
			}
		}
	}
	if err := moveFile(filepath.Join(stage, bundleDB), p.dbPath); err != nil {
		return err
	}
	data := filepath.Join(stage, "data")
	err = filepath.WalkDir(data, func(f string, e fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && f == data {
			return filepath.SkipDir
		}
		if err != nil || e.IsDir() {
			return err
		}
		rel, err := filepath.Rel(data, f)
		if err != nil {
			return err
		}
		return moveFile(f, filepath.Join(p.dataDir, rel))
	})
	if err != nil {
		return err
	}
	for _, k := range m.Keys {
		dst, ok := p.keys[k]
		if !ok {
			continue
		}
		if err := moveFile(filepath.Join(stage, "keys", k), dst); err != nil {
			return err
		}
	}
	return nil
}

// moveFile renames on the same disk, otherwise copies then deletes. Folders 700, files 600.
func moveFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		return err
	}
	return os.Remove(src)
}
