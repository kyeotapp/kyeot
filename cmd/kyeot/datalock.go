package main

import (
	"errors"
	"os"
	"time"
)

// errDataBusy means another kyeot server or kyeot restore is using the same data.
var errDataBusy = errors.New("busy")

// dataLockFile is the lock file next to the DB. The server holds it while running, restore while swapping.
func dataLockFile(dbPath string) string { return dbPath + ".lock" }

// lockData takes the lock immediately. errDataBusy if someone else holds it. Call release once.
func lockData(dbPath string) (release func(), err error) {
	f, err := os.OpenFile(dataLockFile(dbPath), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := tryLock(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() { unlock(f); f.Close() }, nil
}

// waitLockData waits briefly, for when an old process is just exiting, e.g. on a service restart.
func waitLockData(dbPath string, wait time.Duration) (func(), error) {
	end := time.Now().Add(wait)
	for {
		release, err := lockData(dbPath)
		if !errors.Is(err, errDataBusy) || time.Now().After(end) {
			return release, err
		}
		time.Sleep(200 * time.Millisecond)
	}
}
