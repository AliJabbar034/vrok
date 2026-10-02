package ui

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

// ErrNoClipboard means this machine has no clipboard tool vrok knows.
var ErrNoClipboard = errors.New("clipboard: no copy command available")

// Copy writes text to the system clipboard. A missing tool is not fatal:
// the URL is still printed, and the owner can copy it by hand.
func Copy(text string) error {
	cmd, err := clipboardCommand()
	if err != nil {
		return err
	}
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

func clipboardCommand() (*exec.Cmd, error) {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("pbcopy"), nil
	case "windows":
		return exec.Command("clip"), nil
	}

	for _, candidate := range [][]string{
		{"wl-copy"},
		{"xclip", "-selection", "clipboard"},
		{"xsel", "--clipboard", "--input"},
	} {
		if _, err := exec.LookPath(candidate[0]); err == nil {
			return exec.Command(candidate[0], candidate[1:]...), nil
		}
	}
	return nil, ErrNoClipboard
}
