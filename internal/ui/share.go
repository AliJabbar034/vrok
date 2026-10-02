package ui

import (
	"fmt"
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
}

// Started prints the share banner.
func (p *Printer) Started(v ShareView) {
	p.Success("Sharing %s", p.Bold(v.Name))
	p.Blank()
	p.Detail("URL", p.Link(v.URL))

	if v.LocalURL != "" && v.LocalURL != v.URL {
		p.Detail("Local", p.Dim(v.LocalURL))
	}
	if v.Reach != "" {
		p.Detail("Reachable", v.Reach)
	}
	if v.TTL > 0 {
		p.Detail("Expires", humanize.Duration(v.TTL))
	} else {
		p.Detail("Expires", "when stopped")
	}
	if v.MaxDownloads > 0 {
		p.Detail("Downloads", fmt.Sprintf("%d max", v.MaxDownloads))
	}
	if v.Protected {
		p.Detail("Password", "required")
	}
	if v.Tunnel != "" && v.Tunnel != "local" {
		p.Detail("Tunnel", v.Tunnel)
	}

	p.Blank()
	if v.Hint != "" {
		p.Info("  %s", p.Dim(v.Hint))
		p.Blank()
	}
	p.Info("  %s", p.Dim("Press Ctrl+C to stop"))
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
