package server

import (
	"fmt"
	"os"
)

// Windows has no O_NOFOLLOW, so check for a link before opening.
func openNoFollow(path string) (*os.File, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s: 링크는 따라가지 않는다", path)
	}
	return os.Open(path)
}
