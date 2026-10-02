package awake

import (
	"os"
	"os/exec"
	"strconv"
)

// hold runs caffeinate, which is part of macOS. -w ties it to vrok's PID, so
// the assertion ends even if vrok is killed before it can release it.
func hold() (func(), error) {
	cmd := exec.Command("caffeinate", "-i", "-w", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}, nil
}
