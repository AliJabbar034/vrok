//go:build !windows

package cli

import "syscall"

// suspend stops vrok the way Ctrl+Z normally would. Raw mode swallows the
// key, so the hotkey loop sends SIGTSTP itself; `fg` resumes the share.
func suspend() { _ = syscall.Kill(syscall.Getpid(), syscall.SIGTSTP) }
