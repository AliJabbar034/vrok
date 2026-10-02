package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/AliJabbar034/vrok/internal/ui"
)

// passwordEnv lets automation supply a password without a terminal.
const passwordEnv = "VROK_PASSWORD"

// readPassword obtains a share password without echoing it.
//
// Three sources are tried in order of specificity: the environment, a real
// terminal, and finally piped stdin. The piped case is what makes
// `echo secret | vrok share file --password` work in a script.
func readPassword(printer *ui.Printer) (string, error) {
	if value := os.Getenv(passwordEnv); value != "" {
		return value, nil
	}

	if term.IsTerminal(int(os.Stdin.Fd())) {
		return promptPassword(printer)
	}

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read password from stdin: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// promptPassword asks for a password on the terminal without echoing it.
func promptPassword(printer *ui.Printer) (string, error) {
	fmt.Fprint(printer.Out(), "Password: ")
	entered, err := term.ReadPassword(int(os.Stdin.Fd()))
	// ReadPassword leaves the cursor on the prompt line because it
	// swallowed the newline the user typed.
	fmt.Fprintln(printer.Out())
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return strings.TrimSpace(string(entered)), nil
}

// readLine reads one line a byte at a time. It deliberately does not buffer:
// anything typed after the newline belongs to the live hotkeys.
func readLine(r io.Reader) (string, error) {
	var line []byte
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n == 1 {
			if buf[0] == '\n' {
				break
			}
			line = append(line, buf[0])
		}
		if err != nil {
			if len(line) == 0 {
				return "", err
			}
			break
		}
	}
	return strings.TrimSpace(string(line)), nil
}
