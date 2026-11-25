package disk

import (
	"os"
	"syscall"
	"unsafe"
)

var getDiskFreeSpaceEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

// statfs treats "/" as the system drive (usually C:\).
func statfs(path string) (Usage, error) {
	if path == "/" {
		path = os.Getenv("SystemDrive") + `\`
		if path == `\` {
			path = `C:\`
		}
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return Usage{}, err
	}
	var avail, total, free uint64
	if r, _, err := getDiskFreeSpaceEx.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&avail)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&free))); r == 0 {
		return Usage{}, err
	}
	return Usage{Used: total - free, Avail: avail}, nil
}
