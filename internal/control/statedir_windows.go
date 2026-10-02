//go:build windows

package control

import (
	"fmt"
	"path/filepath"

	"os"
)

// defaultStateDir uses %LOCALAPPDATA%, which is the Windows equivalent of
// per-user state that is not roamed to other machines — the right place for
// sockets that only mean anything to processes on this one.
//
// os.UserCacheDir reports %LOCALAPPDATA% on Windows.
func defaultStateDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("control: locate local app data: %w", err)
	}
	return filepath.Join(dir, "vrok", "state"), nil
}
