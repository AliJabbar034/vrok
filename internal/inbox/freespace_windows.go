//go:build windows

package inbox

import "golang.org/x/sys/windows"

// freeBytes returns the space available to this user on the disk holding
// dir, or -1 when it cannot be read.
func freeBytes(dir string) int64 {
	path, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return -1
	}
	var available uint64
	if err := windows.GetDiskFreeSpaceEx(path, &available, nil, nil); err != nil {
		return -1
	}
	return int64(available)
}
