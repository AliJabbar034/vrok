//go:build windows

package tunnel

import "os"

// terminate stops a provider. Windows has no portable equivalent of SIGTERM
// for a child process: os.Process.Signal rejects everything except Kill, and
// generating a console control event would hit vrok's own console too. The
// providers keep no local state that an abrupt exit can corrupt, and they drop
// their tunnel when the connection dies.
func terminate(p *os.Process) error { return p.Kill() }
