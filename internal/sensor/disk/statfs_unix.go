//go:build !windows

package disk

import "syscall"

func statfs(path string) (Usage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Usage{}, err
	}
	bs := uint64(st.Bsize)
	return Usage{Used: (uint64(st.Blocks) - uint64(st.Bfree)) * bs, Avail: uint64(st.Bavail) * bs}, nil
}
