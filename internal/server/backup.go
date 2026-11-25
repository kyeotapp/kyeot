package server

import (
	"log"
	"path/filepath"
	"time"
)

const backupCheckEvery = time.Hour

func (s *Server) backupDir() string {
	if s.opts.BackupDir != "" {
		return s.opts.BackupDir
	}
	return filepath.Join(filepath.Dir(s.opts.DBPath), "backups")
}

// backupLoop checks on start and hourly whether the daily backup is due.
func (s *Server) backupLoop() {
	defer s.bg.Done()
	s.dailyBackup()
	t := time.NewTicker(backupCheckEvery)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.dailyBackup()
		}
	}
}

// dailyBackup only logs failures. A closed server is not backed up.
func (s *Server) dailyBackup() {
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	if s.closed.Load() {
		return
	}
	name, err := s.store.DailyBackup(s.backupDir())
	if err != nil {
		log.Printf("daily backup failed (server keeps running): %v", err)
	} else if name != "" {
		log.Printf("daily backup: %s", name)
	}
}

// removeBackups keeps deleted records out of backups when the server is closed.
func (s *Server) removeBackups() {
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	if err := s.store.RemoveBackups(s.backupDir()); err != nil {
		log.Printf("server close: removing backups failed: %v", err)
	}
}
