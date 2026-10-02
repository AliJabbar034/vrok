//go:build !windows

package control

import (
	"fmt"
	"os"
	"path/filepath"
)

// defaultStateDir follows the XDG base directory spec, which places runtime
// state that should survive a reboot under ~/.local/state.
func defaultStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("control: locate home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "vrok"), nil
}
