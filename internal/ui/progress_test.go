package ui

import (
	"strings"
	"testing"
	"time"
)

func TestProgressLineSingleDownload(t *testing.T) {
	got := ProgressLine([]Transfer{{Sent: 1 << 30, Total: 4 << 30}}, 10<<20)
	for _, want := range []string{"1.0 GB / 4.0 GB", "25%", "10 MB/s", "left"} {
		if !strings.Contains(got, want) {
			t.Errorf("ProgressLine = %q, missing %q", got, want)
		}
	}
}

func TestProgressLineUnknownSize(t *testing.T) {
	got := ProgressLine([]Transfer{{Sent: 5 << 20, Total: -1}}, 1<<20)
	if strings.Contains(got, "%") || strings.Contains(got, "left") {
		t.Errorf("a zip of unknown size claimed a percentage: %q", got)
	}
}

func TestProgressLineSeveralDownloads(t *testing.T) {
	got := ProgressLine([]Transfer{{Sent: 1 << 20, Total: 2 << 20}, {Sent: 1 << 20, Total: -1}}, 0)
	if !strings.Contains(got, "2 downloads") || !strings.Contains(got, "2.0 MB sent") {
		t.Errorf("ProgressLine = %q", got)
	}
	if strings.Contains(got, "B/s") {
		t.Errorf("an unknown speed was shown as a number: %q", got)
	}
}

func TestBurstLine(t *testing.T) {
	got := BurstLine(4<<30, 108*time.Second)
	if !strings.Contains(got, "Sent 4.0 GB in 1m 48s") || !strings.Contains(got, "average") {
		t.Errorf("BurstLine = %q", got)
	}
}

func TestFit(t *testing.T) {
	if got := Fit("abcdef", 4); got != "ab…" {
		t.Errorf("Fit = %q", got)
	}
	if got := Fit("abc", 10); got != "abc" {
		t.Errorf("Fit = %q", got)
	}
}
