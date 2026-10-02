// Package control lets one vrok process inspect and manage the shares of
// another.
//
// Shares live in the memory of the process that created them, which is what
// makes them disappear when that process exits. `vrok list` and `vrok revoke`
// therefore cannot read a database — there is none — so instead every sharing
// process exposes a small API on a Unix socket in the user's own state
// directory, and the management commands talk to those sockets.
package control

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// API paths exposed on the session socket.
const (
	pathShares = "/shares"
	pathRevoke = "/revoke"
	pathStop   = "/stop"
)

// ShareInfo is a share as seen from another process.
type ShareInfo struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Source    string    `json:"source"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
	// ExpiresAt is zero for a share with no TTL.
	ExpiresAt    time.Time `json:"expires_at"`
	Downloads    int       `json:"downloads"`
	MaxDownloads int       `json:"max_downloads"`
	Bytes        int64     `json:"bytes"`
	LastAccess   time.Time `json:"last_access"`
	Protected    bool      `json:"protected"`
	Tunnel       string    `json:"tunnel"`
	// PID identifies the process serving this share, filled in by the client.
	PID int `json:"pid,omitempty"`
}

// Provider is the sharing process's side of the control API. The CLI
// implements it over its own registry.
type Provider interface {
	// Shares returns the live shares of this process.
	Shares() []ShareInfo
	// Revoke stops one share, reporting whether it existed.
	Revoke(id string) bool
	// Stop shuts the whole process down.
	Stop()
}

// StateDir returns the directory holding session sockets. XDG_STATE_HOME wins
// on every platform, so the location can be overridden and tested anywhere;
// otherwise each platform's own convention applies.
//
// It is created with owner-only permissions: anything in it can stop another
// process's shares.
func StateDir() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "vrok"), nil
	}
	return defaultStateDir()
}

// sessionDir returns the directory holding one socket per running process.
func sessionDir() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sessions"), nil
}

// maxSocketPath is the practical limit on a Unix socket path. The kernel's
// sun_path field is 104 bytes on macOS and the BSDs, 108 on Linux; exceeding
// it fails with a bare "invalid argument" that explains nothing.
const maxSocketPath = 100

// socketPath returns the socket path for a process id.
//
// The state directory is kept shallow and the filename is just a pid, because
// the whole path has to fit in a kernel structure far smaller than PATH_MAX.
func socketPath(pid int) (string, error) {
	dir, err := sessionDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%d.sock", pid))
	if len(path) > maxSocketPath {
		return "", fmt.Errorf(
			"control: socket path %q is %d bytes, over the %d byte limit; set XDG_STATE_HOME to a shorter directory",
			path, len(path), maxSocketPath)
	}
	return path, nil
}
