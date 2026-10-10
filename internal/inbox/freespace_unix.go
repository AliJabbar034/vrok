//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package inbox

import "golang.org/x/sys/unix"

// freeBytes returns the space available to an unprivileged process on the
// disk holding dir, or -1 when it cannot be read.
func freeBytes(dir string) int64 {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}
