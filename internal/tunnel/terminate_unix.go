//go:build !windows

package tunnel

import (
	"os"
	"syscall"
)

// terminate asks a provider to shut down. cloudflared closes its tunnel
// cleanly on SIGTERM, which matters because an abandoned tunnel can keep
// answering for a while after vrok has gone. os/exec kills the process anyway
// if it ignores the signal.
func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
