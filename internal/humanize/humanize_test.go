package humanize_test

import (
	"testing"
	"time"

	"github.com/AliJabbar034/vrok/internal/humanize"
)

func TestBytes(t *testing.T) {
	cases := map[int64]string{
		0:          "0 B",
		1:          "1 B",
		1023:       "1023 B",
		1024:       "1.0 KB",
		1536:       "1.5 KB",
		10240:      "10 KB",
		1048576:    "1.0 MB",
		863832178:  "824 MB",
		1073741824: "1.0 GB",
		-1:         "-",
	}
	for input, want := range cases {
		if got := humanize.Bytes(input); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", input, got, want)
		}
	}
}

func TestElapsed(t *testing.T) {
	cases := map[time.Duration]string{
		0:                 "0s",
		12 * time.Second:  "12s",
		108 * time.Second: "1m 48s",
		2*time.Hour + 5*time.Minute + 9*time.Second: "2h 5m",
	}
	for input, want := range cases {
		if got := humanize.Elapsed(input); got != want {
			t.Errorf("Elapsed(%v) = %q, want %q", input, got, want)
		}
	}
}

func TestDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0:                 "expired",
		-time.Second:      "expired",
		45 * time.Second:  "45s",
		90 * time.Second:  "1m",
		34 * time.Minute:  "34m",
		time.Hour:         "1h",
		102 * time.Minute: "1h 42m",
		25 * time.Hour:    "1d 1h",
		48 * time.Hour:    "2d",
	}
	for input, want := range cases {
		if got := humanize.Duration(input); got != want {
			t.Errorf("Duration(%v) = %q, want %q", input, got, want)
		}
	}
}

func TestClockTime(t *testing.T) {
	if got := humanize.ClockTime(time.Time{}); got != "never" {
		t.Errorf("ClockTime(zero) = %q, want never", got)
	}
	when := time.Date(2026, 10, 2, 14, 32, 7, 0, time.Local)
	if got := humanize.ClockTime(when); got != "14:32:07" {
		t.Errorf("ClockTime = %q, want 14:32:07", got)
	}
}
