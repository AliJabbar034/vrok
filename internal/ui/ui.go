// Package ui owns everything vrok prints to a terminal. Keeping it in one
// place means the CLI commands contain decisions, not formatting, and that
// colour and symbol support is decided once.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Printer writes vrok's terminal output.
type Printer struct {
	out   io.Writer
	err   io.Writer
	color bool
}

// New returns a Printer for the given streams, enabling colour only when the
// output really is an interactive terminal.
func New(out, errOut io.Writer) *Printer {
	return &Printer{out: out, err: errOut, color: supportsColor(out)}
}

// NewStd returns a Printer writing to the process's standard streams.
func NewStd() *Printer { return New(os.Stdout, os.Stderr) }

// Out exposes the output stream for callers that write their own formats.
func (p *Printer) Out() io.Writer { return p.out }

// SetColor forces colour on or off, for --no-color and for tests.
func (p *Printer) SetColor(enabled bool) { p.color = enabled }

// supportsColor reports whether styling should be emitted.
//
// Respecting NO_COLOR and piped output matters because vrok's output is
// frequently piped into other tools or captured in CI logs, where escape
// codes are noise.
func supportsColor(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	file, ok := w.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

// ANSI styles. They are applied through style(), which is a no-op when colour
// is disabled, so no call site needs to check.
const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	cyan   = "\033[36m"
)

func (p *Printer) style(code, text string) string {
	if !p.color || text == "" {
		return text
	}
	return code + text + reset
}

// Bold returns text in bold.
func (p *Printer) Bold(text string) string { return p.style(bold, text) }

// Dim returns de-emphasised text.
func (p *Printer) Dim(text string) string { return p.style(dim, text) }

// Link returns a highlighted URL.
func (p *Printer) Link(text string) string { return p.style(cyan, text) }

// Success prints a confirmation line.
func (p *Printer) Success(format string, args ...any) {
	fmt.Fprintf(p.out, "%s %s\n", p.style(green, "✓"), fmt.Sprintf(format, args...))
}

// Info prints a plain line.
func (p *Printer) Info(format string, args ...any) {
	fmt.Fprintf(p.out, "%s\n", fmt.Sprintf(format, args...))
}

// Step reports progress on something slow that the user is waiting through,
// so a one-time download does not look like the tool hanging. It goes to the
// error stream to keep it out of a piped URL.
func (p *Printer) Step(format string, args ...any) {
	fmt.Fprintf(p.err, "%s %s\n", p.style(dim, "→"), fmt.Sprintf(format, args...))
}

// Detail prints an indented secondary line.
func (p *Printer) Detail(label, value string) {
	fmt.Fprintf(p.out, "  %s %s\n", p.Dim(pad(label+":", 12)), value)
}

// Blank prints an empty line.
func (p *Printer) Blank() { fmt.Fprintln(p.out) }

// Warn prints a warning to the error stream.
func (p *Printer) Warn(format string, args ...any) {
	fmt.Fprintf(p.err, "%s %s\n", p.style(yellow, "!"), fmt.Sprintf(format, args...))
}

// Error prints a failure to the error stream.
func (p *Printer) Error(format string, args ...any) {
	fmt.Fprintf(p.err, "%s %s\n", p.style(red, "✗"), fmt.Sprintf(format, args...))
}

// Plural renders a count with the right noun, so messages read as sentences
// rather than as "1 process(es)".
func Plural(count int, singular, plural string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, singular)
	}
	return fmt.Sprintf("%d %s", count, plural)
}

func pad(text string, width int) string {
	if len(text) >= width {
		return text
	}
	return text + strings.Repeat(" ", width-len(text))
}

// Status is the outcome of one diagnostic check.
type Status int

const (
	StatusOK Status = iota
	StatusWarn
	StatusFail
)

// Check prints one diagnostic line. Unlike Warn and Error it writes every
// outcome to the output stream, so a report keeps its order and survives
// being piped or pasted into an issue whole.
func (p *Printer) Check(s Status, label, value string) {
	symbol := p.style(green, "✓")
	switch s {
	case StatusWarn:
		symbol = p.style(yellow, "!")
	case StatusFail:
		symbol = p.style(red, "✗")
	}
	fmt.Fprintf(p.out, "%s %s %s\n", symbol, pad(label, 12), value)
}

// Hint prints a follow-up line under a Check.
func (p *Printer) Hint(format string, args ...any) {
	fmt.Fprintf(p.out, "  %s %s\n", pad("", 12), p.Dim(fmt.Sprintf(format, args...)))
}
