package ui

import (
	"github.com/mdp/qrterminal/v3"
)

// QR prints a scannable QR code for a URL.
//
// This is the fastest path from a terminal to a phone: no retyping a token, no
// sending yourself a message. Half-block rendering is used so the code fits in
// a normal terminal window, since a full-block code is twice as tall.
func (p *Printer) QR(url string) {
	p.Blank()
	p.Info("  %s", p.Dim("Scan to open:"))
	p.Blank()

	qrterminal.GenerateWithConfig(url, qrterminal.Config{
		Level:  qrterminal.M,
		Writer: p.out,
		// Terminals are light-on-dark or dark-on-light depending on the
		// user's theme; half blocks with an explicit quiet zone scan reliably
		// either way.
		HalfBlocks: true,
		BlackChar:  qrterminal.BLACK_BLACK,
		WhiteChar:  qrterminal.WHITE_WHITE,
		QuietZone:  2,
	})
	p.Blank()
}
