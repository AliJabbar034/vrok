// Package cli assembles vrok's commands. It is the composition root: this is
// the only package that knows how the domain, HTTP, tunnel and terminal layers
// are wired together, and the only one allowed to depend on all of them.
package cli

import (
	"io"
	"log/slog"
	"os"

	"github.com/AliJabbar034/vrok/internal/config"
	"github.com/AliJabbar034/vrok/internal/ui"
)

// app carries the state shared by every command.
type app struct {
	printer *ui.Printer
	config  config.Config

	// verbose raises the log level to debug, which turns on request logs and
	// tunnel provider output.
	verbose bool
	// noColor disables styling regardless of terminal detection.
	noColor bool
}

func newApp() *app {
	return &app{printer: ui.NewStd(), config: config.Default()}
}

// load reads the user's configuration. A broken config file is reported as a
// warning and ignored: it must not stop someone from sharing a file.
func (a *app) load() {
	cfg, err := config.Load()
	if err != nil {
		a.printer.Warn("%v", err)
		cfg = config.Default()
	}
	a.config = cfg
	if a.noColor {
		a.printer.SetColor(false)
	}
}

// logger returns the structured logger for the HTTP server and tunnels.
//
// Diagnostics go to stderr so that the share URL on stdout stays pipeable, and
// they are quiet unless --verbose: a tool you run in the foreground should
// print the URL, not a log stream.
func (a *app) logger() *slog.Logger {
	level := slog.LevelWarn
	var out io.Writer = os.Stderr
	if a.verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: level}))
}
