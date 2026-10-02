package ui

import (
	"fmt"
	"strings"
	"text/tabwriter"
)

// Table prints aligned columns for `vrok list`.
//
// It uses text/tabwriter rather than fixed widths so a long filename widens
// its column instead of breaking the layout.
type Table struct {
	printer *Printer
	writer  *tabwriter.Writer
	columns int
}

// NewTable starts a table with the given headers.
func (p *Printer) NewTable(headers ...string) *Table {
	w := tabwriter.NewWriter(p.out, 0, 0, 3, ' ', 0)
	upper := make([]string, len(headers))
	for i, h := range headers {
		upper[i] = p.Dim(strings.ToUpper(h))
	}
	fmt.Fprintln(w, strings.Join(upper, "\t"))
	return &Table{printer: p, writer: w, columns: len(headers)}
}

// Row adds a row. Missing cells are padded so the table stays aligned even
// when a caller supplies fewer values than there are headers.
func (t *Table) Row(cells ...string) {
	for len(cells) < t.columns {
		cells = append(cells, "")
	}
	fmt.Fprintln(t.writer, strings.Join(cells[:t.columns], "\t"))
}

// Flush writes the table out.
func (t *Table) Flush() error { return t.writer.Flush() }
