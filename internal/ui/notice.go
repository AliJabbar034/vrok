package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// UpdateNotice describes a newer release, for the box printed after a command.
type UpdateNotice struct {
	Current string // "0.6.1"
	Latest  string // "0.7.0"
	Command string // what updates this install
	URL     string // the release notes
}

// UpdateLine is the one-line form of the notice, used in the share banner.
func UpdateLine(n UpdateNotice) string {
	return fmt.Sprintf("↑ Update available: %s → %s", n.Current, n.Latest)
}

// UpdateNotice prints the new-release box to the error stream, so it never
// lands in a piped URL. When the box would not fit in width columns it falls
// back to plain lines, since a wrapped box is harder to read than none.
func (p *Printer) UpdateNotice(n UpdateNotice, width int) {
	// Each line is kept as plain text, to measure, and styled text, to print:
	// escape codes have length but no width.
	type line struct{ plain, styled string }
	lines := []line{
		{
			plain:  fmt.Sprintf("Update available  %s → %s", n.Current, n.Latest),
			styled: fmt.Sprintf("%s  %s → %s", p.Bold("Update available"), p.Dim(n.Current), p.style(green, n.Latest)),
		},
		{
			plain:  fmt.Sprintf("Run  %s  to update", n.Command),
			styled: fmt.Sprintf("Run  %s  to update", p.style(cyan, n.Command)),
		},
		{
			plain:  "Changes: " + n.URL,
			styled: p.Dim("Changes: ") + p.Link(n.URL),
		},
	}

	inner := 0
	for _, l := range lines {
		inner = max(inner, utf8.RuneCountInString(l.plain))
	}
	const margin = 3
	boxWidth := inner + 2*margin + 2

	fmt.Fprintln(p.err)
	if width > 0 && boxWidth > width {
		for _, l := range lines {
			fmt.Fprintln(p.err, l.styled)
		}
		return
	}

	edge := func(left, right string) string {
		return p.style(yellow, left+strings.Repeat("─", boxWidth-2)+right)
	}
	side := p.style(yellow, "│")
	empty := side + strings.Repeat(" ", boxWidth-2) + side

	fmt.Fprintln(p.err, edge("╭", "╮"))
	fmt.Fprintln(p.err, empty)
	for _, l := range lines {
		gap := inner - utf8.RuneCountInString(l.plain)
		fmt.Fprintf(p.err, "%s%s%s%s%s%s\n", side, strings.Repeat(" ", margin), l.styled,
			strings.Repeat(" ", gap), strings.Repeat(" ", margin), side)
	}
	fmt.Fprintln(p.err, empty)
	fmt.Fprintln(p.err, edge("╰", "╯"))
}
