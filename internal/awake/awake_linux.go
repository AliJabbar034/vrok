package awake

import "os/exec"

// hold takes a systemd inhibitor lock. The locked command is `cat` reading a
// pipe from vrok: if vrok dies for any reason the pipe closes, cat exits and
// the lock goes with it, so a crash can never leave the machine sleepless.
func hold() (func(), error) {
	path, err := exec.LookPath("systemd-inhibit")
	if err != nil {
		return nil, ErrUnsupported
	}
	cmd := exec.Command(path,
		"--what=idle:sleep", "--who=vrok", "--why=Sharing a file", "--mode=block",
		"cat")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}, nil
}
