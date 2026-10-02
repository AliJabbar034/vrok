package cli

import (
	"bufio"
	"fmt"
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

	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(printer.Out(), "Password: ")
		entered, err := term.ReadPassword(fd)
		// ReadPassword leaves the cursor on the prompt line because it
		// swallowed the newline the user typed.
		fmt.Fprintln(printer.Out())
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		return strings.TrimSpace(string(entered)), nil
	}

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read password from stdin: %w", err)
	}
	return strings.TrimSpace(line), nil
}
