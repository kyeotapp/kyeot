package main

import (
	"io"
	"os"
	"strings"
)

// accessLogOut turns on the request log when KYEOT_ACCESS_LOG is 1, on, true, or yes. Off by default.
func accessLogOut() io.Writer {
	switch strings.ToLower(strings.TrimSpace(getenv("ACCESS_LOG"))) {
	case "1", "on", "true", "yes":
		return os.Stderr
	}
	return nil
}
