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
	// Update, when set, adds a new-release line to the banner.
	Update *UpdateNotice
	// Receiving marks a receive share: visitors send files into Folder,
	// and MaxDownloads and Downloads count files received.
	Receiving bool
	Folder    string
	// AcceptAll is set when a receive share takes every file without asking
	// first, which the owner should not be able to forget.
	AcceptAll bool
}

// HotkeyBar is the live command strip shown under a share, in the same
// compact style as Vite and Expo.
const HotkeyBar = "c copy · q QR code · p add password · e change expiry · 1 one-time link · x stop"

// ReceiveHotkeyBar is the strip under a receive share, which has no
// one-time link: its limit is a number of files.
const ReceiveHotkeyBar = "c copy · q QR code · p add password · e change expiry · x stop"

// Started prints the share banner: URL first, then who can open it, then
// the hotkeys (or Ctrl+C when there is no terminal).
func (p *Printer) Started(v ShareView) {
	if v.Receiving {
		p.Success("Receiving files into %s", p.Bold(v.Folder))
	} else {
		p.Success("Sharing %s", p.Bold(v.Name))
	}
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
	if v.Update != nil {
		p.Info("  %s %s", p.style(yellow, UpdateLine(*v.Update)), p.Dim("· run")+" "+p.style(cyan, v.Update.Command))
	}

	p.Blank()
	if v.Hint != "" {
		p.Info("  %s", p.Dim(v.Hint))
		p.Blank()
	}
	if v.Interactive {
		p.Keys(v.Receiving)
	} else {
		p.Info("  %s", p.Dim("Press Ctrl+C to stop"))
	}
}

// Keys prints the live hotkey bar for a share, or for a receive share.
func (p *Printer) Keys(receiving bool) {
	bar := HotkeyBar
	if receiving {
		bar = ReceiveHotkeyBar
	}
	p.Info("  %s", p.Dim(bar))
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
	if v.Receiving {
		parts = append(parts, fileLimit(v.MaxDownloads, v.Downloads)...)
		if v.AcceptAll {
			parts = append(parts, "accepts every file without asking")
		}
	} else if v.MaxDownloads == 1 {
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

func fileLimit(limit, used int) []string {
	switch {
	case limit == 0:
		return nil
	case limit == 1:
		return []string{"one file only"}
	case limit-used == 1:
		return []string{"one file left"}
	}
	return []string{fmt.Sprintf("up to %d files", limit)}
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
	// Receiving switches the summary to a receive share: Files were taken
	// in completely, into Folder.
	Receiving bool
	Folder    string
	Files     int
}

// Stopped prints the closing summary.
func (p *Printer) Stopped(s Stats) {
	p.Blank()
	if s.Receiving {
		p.stoppedReceiving(s)
		return
	}
	if s.Expired {
		p.Info("%s %s", p.style(yellow, "⌛"), fmt.Sprintf("%s expired", p.Bold(s.Name)))
	} else {
		p.Info("%s %s", p.style(dim, "■"), fmt.Sprintf("Stopped sharing %s", p.Bold(s.Name)))
	}
	p.Detail("Downloads", fmt.Sprintf("%d", s.Downloads))
	p.Detail("Transferred", humanize.Bytes(s.Bytes))
	p.Detail("Last access", humanize.ClockTime(s.LastAccess))
}

func (p *Printer) stoppedReceiving(s Stats) {
	if s.Expired {
		p.Info("%s %s", p.style(yellow, "⌛"), "The receive link expired")
	} else {
		p.Info("%s %s", p.style(dim, "■"), fmt.Sprintf("Stopped receiving into %s", p.Bold(s.Folder)))
	}
	p.Detail("Files", fmt.Sprintf("%d", s.Files))
	p.Detail("Received", humanize.Bytes(s.Bytes))
	p.Detail("Last access", humanize.ClockTime(s.LastAccess))
}

// Received is the permanent line left when a file has arrived:
//
//	✓ Received holiday.mov (1.2 GB)
func (p *Printer) Received(name string, size int64) string {
	return fmt.Sprintf("%s Received %s %s", p.style(green, "✓"), p.Bold(name), p.Dim("("+humanize.Bytes(size)+")"))
}

// OfferFile is one file someone asks to send.
type OfferFile struct {
	Name string
	Size int64
}

// maxOfferLines is how many files an offer lists before summing up the rest.
const maxOfferLines = 8

// Offer is the question shown when someone asks to send files:
//
//	📥 Someone wants to send 3 files (1.2 GB)
//	     holiday.mov   1.2 GB
//	     notes.txt     8 B
//	   Accept? press y to accept, n to decline
func (p *Printer) Offer(files []OfferFile, total int64) []string {
	lines := []string{fmt.Sprintf("%s %s", p.style(yellow, "📥"),
		p.Bold(fmt.Sprintf("Someone wants to send %s (%s)", fileCount(len(files)), humanize.Bytes(total))))}

	width := 0
	for i, f := range files {
		if i == maxOfferLines {
			break
		}
		width = max(width, len([]rune(f.Name)))
	}
	width = min(width, 48)
	for i, f := range files {
		if i == maxOfferLines {
			lines = append(lines, p.Dim(fmt.Sprintf("     … and %d more", len(files)-maxOfferLines)))
			break
		}
		name := Fit(f.Name, width+1)
		lines = append(lines, fmt.Sprintf("     %-*s  %s", width, name, p.Dim(humanize.Bytes(f.Size))))
	}
	lines = append(lines, fmt.Sprintf("   %s %s %s %s %s", p.Bold("Accept?"),
		p.Dim("press"), p.style(cyan, "y"), p.Dim("to accept,"), p.style(cyan, "n")+p.Dim(" to decline")))
	return lines
}

// OfferAnswered is the line left once an offer is accepted or declined.
func (p *Printer) OfferAnswered(count int, accepted bool) string {
	if accepted {
		return fmt.Sprintf("%s Accepted %s", p.style(green, "✓"), fileCount(count))
	}
	return fmt.Sprintf("%s Declined %s", p.style(dim, "✗"), fileCount(count))
}

// OfferExpired is the line left when nobody answered an offer in time.
func (p *Printer) OfferExpired(count int) string {
	return fmt.Sprintf("%s %s", p.style(yellow, "⌛"), p.Dim("No answer, so the request to send "+fileCount(count)+" was dropped"))
}

func fileCount(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}
