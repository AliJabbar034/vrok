//go:build windows

package cli

// suspend is a no-op: Windows consoles have no job control.
func suspend() {}
