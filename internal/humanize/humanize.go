// Package humanize formats sizes and durations the way vrok shows them in the
// terminal and in the browser. Both surfaces use it so the numbers always
// agree.
package humanize

import (
	"fmt"
	"time"
)

// Bytes renders a byte count with binary units: 1536 -> "1.5 KB".
func Bytes(n int64) string {
	if n < 0 {
		return "-"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value := float64(n)
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	var suffix string
	for _, u := range units {
		value /= unit
		suffix = u
		if value < unit {
			break
		}
	}
	if value < 10 {
		return fmt.Sprintf("%.1f %s", value, suffix)
	}
	return fmt.Sprintf("%.0f %s", value, suffix)
}

// Duration renders a coarse, compact duration: "1h 42m", "34m", "45s".
// It is used for "expires in" countdowns, where sub-second precision is noise.
func Duration(d time.Duration) string {
	if d <= 0 {
		return "expired"
	}
	d = d.Round(time.Second)

	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	switch {
	case days > 0:
		if hours > 0 {
			return fmt.Sprintf("%dd %dh", days, hours)
		}
		return fmt.Sprintf("%dd", days)
	case hours > 0:
		if minutes > 0 {
			return fmt.Sprintf("%dh %dm", hours, minutes)
		}
		return fmt.Sprintf("%dh", hours)
	case minutes > 0:
		return fmt.Sprintf("%dm", minutes)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

// ClockTime renders a local wall-clock time, or "never" for the zero value.
// vrok uses it for "last access", where the time of day is what people look
// for rather than an elapsed duration.
func ClockTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Local().Format("15:04:05")
}
