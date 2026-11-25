package store

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	keepBackups = 3 // backups taken before a schema change
	keepDaily   = 7
	dailyEvery  = 24 * time.Hour
	dailyPrefix = "kyeot-"
	partPrefix  = ".kyeot-part-"
)

var backupClock = time.Now

// backup copies the pre-change DB to <db>.bak-<oldversion>-<time> in the same folder (0600).
func backup(db *sql.DB, path string, from int) (string, error) {
	name := fmt.Sprintf("%s.bak-%d-%s", path, from, backupClock().Format(stampLayout))
	if err := snapshot(db, name); err != nil {
		return "", err
	}
	pruneBackups(filepath.Dir(path), filepath.Base(path)+".bak-", keepBackups)
	return name, nil
}

// snapshot copies db to dst (0600). dst appears only after everything is written under a temp name and flushed to disk. Fails if dst exists.
func snapshot(db *sql.DB, dst string) error {
	dir := filepath.Dir(dst)
	removeStaleParts(dir)
	f, err := os.CreateTemp(dir, partPrefix+"*") // VACUUM INTO writes into an empty file and keeps its permissions
	if err != nil {
		return err
	}
	tmp := f.Name()
	f.Close()
	defer os.Remove(tmp)
	if _, err := db.Exec(`VACUUM INTO ?`, tmp); err != nil {
		return err
	}
	if err := syncFile(tmp); err != nil {
		return err
	}
	if err := os.Link(tmp, dst); err == nil || errors.Is(err, fs.ErrExist) {
		return err
	}
	if _, err := os.Lstat(dst); err == nil { // filesystem without hard links
		return fmt.Errorf("%s: %w", dst, fs.ErrExist)
	}
	return os.Rename(tmp, dst)
}

func syncFile(name string) error {
	f, err := os.OpenFile(name, os.O_RDWR, 0) // Windows needs a write handle to flush
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// removeStaleParts deletes temp files over an hour old left by an aborted backup.
func removeStaleParts(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), partPrefix) {
			continue
		}
		if fi, err := e.Info(); err == nil && backupClock().Sub(fi.ModTime()) > time.Hour {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

type backupFile struct {
	name string
	mod  time.Time
}

// backupsIn returns regular files in dir starting with prefix, newest first.
func backupsIn(dir, prefix string) []backupFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []backupFile
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if fi, err := e.Info(); err == nil {
			out = append(out, backupFile{e.Name(), fi.ModTime()})
		}
	}
	slices.SortFunc(out, func(a, b backupFile) int {
		if c := b.mod.Compare(a.mod); c != 0 {
			return c
		}
		return strings.Compare(b.name, a.name)
	})
	return out
}

// pruneBackups keeps only the latest keep.
func pruneBackups(dir, prefix string, keep int) {
	baks := backupsIn(dir, prefix)
	for _, b := range baks[min(len(baks), keep):] {
		os.Remove(filepath.Join(dir, b.name))
	}
}

// DailyBackup copies the DB to dir/kyeot-<time>.db if the newest backup in dir is over 24 hours old, and keeps only the latest 7.
// Returns "" if none was made. Skipped for an in-memory DB or URI path. Age is measured by the time in the name.
func (s *Store) DailyBackup(dir string) (string, error) {
	if s.path == "" {
		return "", nil
	}
	now := backupClock().Local()
	daily := slices.DeleteFunc(dailyBackups(dir), func(b dailyFile) bool { return b.at.After(now.Add(time.Hour)) }) // names from when the clock ran ahead are left out of the age check
	if len(daily) > 0 && now.Sub(daily[0].at) < dailyEvery {
		return "", nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := filepath.Join(dir, dailyPrefix+now.Format(stampLayout)+".db")
	if _, err := snapshotPath(s.path, name); err != nil {
		return "", err
	}
	after := dailyBackups(dir)
	for _, b := range after[min(len(after), keepDaily):] {
		os.Remove(filepath.Join(dir, b.name))
	}
	return name, nil
}

const stampLayout = "20060102-150405"

type dailyFile struct {
	name string
	at   time.Time
}

// dailyBackups returns files named kyeot-<time>.db, newest first.
func dailyBackups(dir string) []dailyFile {
	entries, _ := os.ReadDir(dir)
	var out []dailyFile
	for _, e := range entries {
		stamp, ok := strings.CutPrefix(e.Name(), dailyPrefix)
		if stamp, ok2 := strings.CutSuffix(stamp, ".db"); ok && ok2 && e.Type().IsRegular() {
			if at, err := time.ParseInLocation(stampLayout, stamp, time.Local); err == nil {
				out = append(out, dailyFile{e.Name(), at})
			}
		}
	}
	slices.SortFunc(out, func(a, b dailyFile) int { return b.at.Compare(a.at) })
	return out
}

// RestorePrefix is the name prefix of the bundle kyeot restore leaves in the daily backup folder before replacing.
const RestorePrefix = "before-restore-"

// temp folders an aborted kyeot backup or restore may leave in the data folder
var workPrefixes = []string{".backup-", ".restore-"}

// RemoveBackups deletes daily backups, pre-restore bundles (dir), schema-change backups (<db>.bak-*), and leftover temp copies.
// Used when deleting the whole server or my records. Files the user made elsewhere with kyeot backup are untouched.
func (s *Store) RemoveBackups(dir string) error {
	if s.path == "" {
		return nil
	}
	var errs []error
	rm := func(dir, name string) {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			errs = append(errs, err)
		}
	}
	for _, b := range dailyBackups(dir) {
		rm(dir, b.name)
	}
	for _, b := range backupsIn(dir, RestorePrefix) {
		rm(dir, b.name)
	}
	for _, b := range backupsIn(dir, ".kyeot-backup-") { // bundle left half-written by an aborted restore
		rm(dir, b.name)
	}
	data := filepath.Dir(s.path)
	for _, b := range backupsIn(data, filepath.Base(s.path)+".bak-") {
		rm(data, b.name)
	}
	entries, _ := os.ReadDir(data)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), partPrefix) || (e.IsDir() && slices.ContainsFunc(workPrefixes, func(p string) bool { return strings.HasPrefix(e.Name(), p) })) {
			rm(data, e.Name())
		}
	}
	return errors.Join(errs...)
}

// Snapshot copies the DB at path to dst (0600) without modifying it. The server may be running. Returns the copied DB's schema version.
func Snapshot(path, dst string) (int, error) {
	if _, err := os.Stat(path); err != nil {
		return 0, err
	}
	if _, err := os.Stat(dst); err == nil {
		return 0, fmt.Errorf("%s: %w", dst, fs.ErrExist)
	}
	return snapshotPath(path, dst)
}

// snapshotPath copies over a separately opened connection, without blocking the write connection. VACUUM INTO is rejected on a query_only connection.
func snapshotPath(path, dst string) (int, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return 0, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var ver int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil {
		return 0, err
	}
	return ver, snapshot(db, dst)
}

// CheckFile checks the DB file is not corrupt and returns its schema version. It does not modify the file.
func CheckFile(path string) (int, error) {
	if _, err := os.Stat(path); err != nil {
		return 0, err
	}
	db, err := sql.Open("sqlite", dsn(path)+"&_pragma=query_only(1)")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var ok string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&ok); err != nil {
		return 0, err
	}
	if ok != "ok" {
		return 0, fmt.Errorf("DB 가 깨졌다: %s", ok)
	}
	var ver int
	err = db.QueryRow(`PRAGMA user_version`).Scan(&ver)
	return ver, err
}

// plainFile reports whether it is a regular file path a backup name can be attached to.
func plainFile(path string) bool {
	return !inMemory(path) && !strings.HasPrefix(path, "file:") && !strings.Contains(path, "?")
}

// privateFiles makes the DB and its side files readable only by the owner.
func privateFiles(path string) error {
	var errs []error
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, 0o600); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
