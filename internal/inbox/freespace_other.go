//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly || windows)

package inbox

// freeBytes cannot read free space on this platform, so no upload is refused
// for lack of it; a full disk then surfaces as a write error.
func freeBytes(string) int64 { return -1 }
