package hermesbridge

import "os"

// On Windows FindProcess actually opens the process, so it fails if the process is gone.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	p.Release()
	return true
}
