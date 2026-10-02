package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/AliJabbar034/vrok/internal/humanize"
)

// ShareView is everything the terminal shows when a share starts.
type ShareView struct {
	// Name is the share's display name.
	Name string
	// URL is the address to hand out.
	URL string
	// LocalURL is the direct address, shown alongside URL when a tunnel is in
	// use so the owner can still test locally.
	LocalURL string
	// TTL is the share lifetime; zero means no expiry.
	TTL time.Duration
	// MaxDownloads is the download cap; zero means unlimited.
	MaxDownloads int
	// Downloads is how many have completed so far.
	Downloads int
	// Protected reports whether a password is required.
	Protected bool
	// Tunnel is the provider name.
	Tunnel string
	// Reach says who can open the URL, and Hint says how to widen that.
	//
	// A loopback URL looks exactly as shareable as a public one, and pasting
	// it into a chat sends the recipient to their own machine. Saying so on
	// the banner is the only moment the owner is paying attention.
	Reach string
	Hint  string
	// Copied is true when the URL was written to the clipboard.
	Copied bool
	// Interactive is true when the process is attached to a terminal, so the
	// hotkey bar is worth printing. Scripts and CI see "Press Ctrl+C" instead.
	Interactive bool
}

// HotkeyBar is the live command strip shown under a share, in the same
// compact style as Vite and Expo.
const HotkeyBar = "c copy · q QR code · p add password · e change expiry · 1 one-time link · x stop"

// Started prints the share banner: URL first, then who can open it, then
// the hotkeys (or Ctrl+C when there is no terminal).
func (p *Printer) Started(v ShareView) {
	p.Success("Sharing %s", p.Bold(v.Name))
	p.Blank()

	copied := ""
	if v.Copied {
		copied = "   " + p.Dim("(copied to clipboard)")
	}
	fmt.Fprintf(p.out, "  %s %s%s\n", p.Dim("URL:  "), p.Link(v.URL), copied)

	p.Info("  %s", p.Dim(StatusLine(v)))

	if v.LocalURL != "" && v.LocalURL != v.URL {
		p.Detail("Local", p.Dim(v.LocalURL))
	}

	p.Blank()
	if v.Hint != "" {
		p.Info("  %s", p.Dim(v.Hint))
		p.Blank()
	}
	if v.Interactive {
		p.Keys()
	} else {
		p.Info("  %s", p.Dim("Press Ctrl+C to stop"))
	}
}

// Keys prints the live hotkey bar.
func (p *Printer) Keys() {
	p.Info("  %s", p.Dim(HotkeyBar))
}

// StatusLine is the compact "Expires in 1h 59m · anyone with the link" row.
func StatusLine(v ShareView) string {
	var parts []string
	if v.TTL > 0 {
		parts = append(parts, "Expires in "+humanize.Duration(v.TTL))
	} else {
		parts = append(parts, "Expires when stopped")
	}
	if v.Reach != "" {
		parts = append(parts, v.Reach)
	}
	if v.MaxDownloads == 1 {
		parts = append(parts, "one-time link")
	} else if v.MaxDownloads > 0 && v.MaxDownloads-v.Downloads == 1 {
		parts = append(parts, "one download left")
	} else if v.MaxDownloads > 0 {
		parts = append(parts, fmt.Sprintf("%d downloads max", v.MaxDownloads))
	}
	if v.Protected {
		parts = append(parts, "password required")
	}
	return strings.Join(parts, " · ")
}

// Stats is the local usage summary shown when a share ends. vrok reports no
// statistics anywhere else: these numbers are counted in this process and
// printed here, and that is all that happens to them.
type Stats struct {
	Name       string
	Downloads  int
	Bytes      int64
	LastAccess time.Time
	Expired    bool
}

// Stopped prints the closing summary.
func (p *Printer) Stopped(s Stats) {
	p.Blank()
	if s.Expired {
		p.Info("%s %s", p.style(yellow, "⌛"), fmt.Sprintf("%s expired", p.Bold(s.Name)))
	} else {
		p.Info("%s %s", p.style(dim, "■"), fmt.Sprintf("Stopped sharing %s", p.Bold(s.Name)))
	}
	p.Detail("Downloads", fmt.Sprintf("%d", s.Downloads))
	p.Detail("Transferred", humanize.Bytes(s.Bytes))
	p.Detail("Last access", humanize.ClockTime(s.LastAccess))
}
