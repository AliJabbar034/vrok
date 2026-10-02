package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestStartedPutsTheURLFirst(t *testing.T) {
	var out bytes.Buffer
	p := New(&out, &out)
	p.SetColor(false)

	p.Started(ShareView{
		Name:         "dumy.dump",
		URL:          "https://example.trycloudflare.com/s/TyEg/",
		TTL:          2*time.Hour - time.Minute,
		Reach:        "anyone with the link",
		Copied:       true,
		Interactive:  true,
		MaxDownloads: 0,
	})

	got := out.String()
	if !strings.Contains(got, "✓ Sharing dumy.dump") {
		t.Fatalf("banner missing the title:\n%s", got)
	}
	urlAt := strings.Index(got, "URL:")
	statusAt := strings.Index(got, "Expires in")
	keysAt := strings.Index(got, "c copy")
	if urlAt < 0 || statusAt < 0 || keysAt < 0 {
		t.Fatalf("banner is missing URL, status or hotkeys:\n%s", got)
	}
	if !(urlAt < statusAt && statusAt < keysAt) {
		t.Fatalf("want URL, then status, then hotkeys; got:\n%s", got)
	}
	if !strings.Contains(got, "(copied to clipboard)") {
		t.Fatalf("TTY banner should say the URL was copied:\n%s", got)
	}
	if strings.Contains(got, "Press Ctrl+C") {
		t.Fatalf("TTY banner should not tell the user to press Ctrl+C:\n%s", got)
	}
}

func TestStartedHidesHotkeysAndCopyWithoutATerminal(t *testing.T) {
	var out bytes.Buffer
	p := New(&out, &out)
	p.SetColor(false)

	p.Started(ShareView{
		Name:        "dumy.dump",
		URL:         "https://example.trycloudflare.com/s/TyEg/",
		TTL:         2 * time.Hour,
		Reach:       "anyone with the link",
		Copied:      false,
		Interactive: false,
	})

	got := out.String()
	if !strings.Contains(got, "Press Ctrl+C to stop") {
		t.Fatalf("non-TTY banner should say Press Ctrl+C:\n%s", got)
	}
	if strings.Contains(got, "(copied to clipboard)") {
		t.Fatalf("non-TTY banner must not claim a clipboard copy:\n%s", got)
	}
	if strings.Contains(got, "c copy") {
		t.Fatalf("non-TTY banner must hide the hotkey bar:\n%s", got)
	}
}

func TestStatusLineNamesOneTimeAndPassword(t *testing.T) {
	got := StatusLine(ShareView{
		TTL:          30 * time.Minute,
		Reach:        "anyone with the link",
		MaxDownloads: 1,
		Protected:    true,
	})
	for _, want := range []string{"Expires in 30m", "anyone with the link", "one-time link", "password required"} {
		if !strings.Contains(got, want) {
			t.Errorf("StatusLine = %q, missing %q", got, want)
		}
	}
}
