package control_test

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// shortStateDir returns a state directory with a deliberately short path.
//
// t.TempDir() names a directory after the test, which easily pushes a socket
// path past the kernel's 104-byte sun_path limit on macOS. Socket paths have
// to stay short, so the test environment has to as well.
func shortStateDir(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(os.TempDir(), "vk"+strconv.FormatInt(time.Now().UnixNano()%1e8, 36))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create state dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
