package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/AliJabbar034/vrok/internal/humanize"
)

// Transfer is one download in flight, as the progress line sees it.
type Transfer struct {
	Sent int64
	// Total is the expected size, or -1 when unknown (a streamed zip).
	Total int64
}

// ProgressLine is the live status shown while files are being downloaded:
//
//	↓ 1.2 GB / 4.0 GB · 30% · 3.1 MB/s · 18m left
//	↓ 3 downloads · 9.4 MB/s · 4.5 GB sent
//
// rate is in bytes per second; zero leaves the speed out rather than
// claiming a stall.
func ProgressLine(transfers []Transfer, rate float64) string {
	return transferLine("↓", "downloads", "sent", transfers, rate)
}

// ReceiveLine is the live status while files are being sent to a receive
// share:
//
//	↑ 1.2 GB / 4.0 GB · 30% · 3.1 MB/s · 18m left
//	↑ 3 uploads · 9.4 MB/s · 4.5 GB received
func ReceiveLine(transfers []Transfer, rate float64) string {
	return transferLine("↑", "uploads", "received", transfers, rate)
}

func transferLine(arrow, many, moved string, transfers []Transfer, rate float64) string {
	var parts []string
	speed := func() {
		if rate > 0 {
			parts = append(parts, humanize.Bytes(int64(rate))+"/s")
		}
	}

	if len(transfers) == 1 {
		t := transfers[0]
		if t.Total <= 0 {
			parts = append(parts, arrow+" "+humanize.Bytes(t.Sent))
			speed()
			return strings.Join(parts, " · ")
		}
		parts = append(parts,
			fmt.Sprintf("%s %s / %s", arrow, humanize.Bytes(t.Sent), humanize.Bytes(t.Total)),
			fmt.Sprintf("%d%%", percent(t.Sent, t.Total)))
		speed()
		if left := t.Total - t.Sent; rate > 0 && left > 0 {
			parts = append(parts, humanize.Duration(time.Duration(float64(left)/rate*float64(time.Second)))+" left")
		}
		return strings.Join(parts, " · ")
	}

	var sent int64
	for _, t := range transfers {
		sent += t.Sent
	}
	parts = append(parts, fmt.Sprintf("%s %d %s", arrow, len(transfers), many))
	speed()
	parts = append(parts, humanize.Bytes(sent)+" "+moved)
	return strings.Join(parts, " · ")
}

// BurstLine is the permanent line left behind when a stretch of downloading
// ends, so the owner can see a large transfer finished after the live line
// has gone.
func BurstLine(sent int64, took time.Duration) string {
	line := fmt.Sprintf("↓ Sent %s in %s", humanize.Bytes(sent), humanize.Elapsed(took))
	if secs := took.Seconds(); secs >= 1 {
		line += fmt.Sprintf(" · %s/s average", humanize.Bytes(int64(float64(sent)/secs)))
	}
	return line
}

func percent(sent, total int64) int {
	if total <= 0 {
		return 0
	}
	p := int(sent * 100 / total)
	return min(max(p, 0), 100)
}

// Fit shortens text to width columns, so a progress line redrawn with a
// carriage return never wraps onto a second line it cannot erase.
func Fit(text string, width int) string {
	if width <= 0 {
		return text
	}
	r := []rune(text)
	if len(r) < width {
		return text
	}
	if width <= 1 {
		return ""
	}
	return string(r[:width-2]) + "…"
}
