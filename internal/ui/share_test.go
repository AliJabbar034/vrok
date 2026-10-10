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

func TestStartedForAReceiveShare(t *testing.T) {
	var out bytes.Buffer
	p := New(&out, &out)
	p.SetColor(false)

	p.Started(ShareView{
		Name:         "vrok",
		Folder:       "~/Downloads/vrok",
		URL:          "https://example.trycloudflare.com/s/TyEg/",
		Reach:        "anyone with the link",
		MaxDownloads: 3,
		Interactive:  true,
		Receiving:    true,
	})

	got := out.String()
	for _, want := range []string{"✓ Receiving files into ~/Downloads/vrok", "up to 3 files", "x stop"} {
		if !strings.Contains(got, want) {
			t.Errorf("banner missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"Sharing", "downloads max", "one-time link"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("receive banner says %q:\n%s", unwanted, got)
		}
	}
}

func TestStatusLineCountsFilesForAReceiveShare(t *testing.T) {
	cases := []struct {
		limit, used int
		want        string
	}{
		{1, 0, "one file only"},
		{3, 2, "one file left"},
		{5, 1, "up to 5 files"},
	}
	for _, c := range cases {
		got := StatusLine(ShareView{Receiving: true, MaxDownloads: c.limit, Downloads: c.used})
		if !strings.Contains(got, c.want) {
			t.Errorf("limit %d, used %d: StatusLine = %q, want %q", c.limit, c.used, got, c.want)
		}
	}
	if got := StatusLine(ShareView{Receiving: true}); strings.Contains(got, "file") {
		t.Errorf("an unlimited receive share mentions a file limit: %q", got)
	}
}

func TestStatusLineWarnsWhenEveryFileIsAccepted(t *testing.T) {
	got := StatusLine(ShareView{Receiving: true, AcceptAll: true})
	if !strings.Contains(got, "accepts every file without asking") {
		t.Errorf("StatusLine = %q, want it to say files are accepted without asking", got)
	}
}

func TestStoppedForAReceiveShare(t *testing.T) {
	var out bytes.Buffer
	p := New(&out, &out)
	p.SetColor(false)

	p.Stopped(Stats{Receiving: true, Folder: "~/Downloads/vrok", Files: 2, Bytes: 3 << 20})
	got := out.String()
	for _, want := range []string{"Stopped receiving into ~/Downloads/vrok", "Files:", "2", "3.0 MB"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Downloads:") {
		t.Errorf("receive summary counts downloads:\n%s", got)
	}
}

func TestReceived(t *testing.T) {
	p := New(&bytes.Buffer{}, &bytes.Buffer{})
	p.SetColor(false)
	if got := p.Received("holiday.mov", 3<<20); got != "✓ Received holiday.mov (3.0 MB)" {
		t.Errorf("Received = %q", got)
	}
}

func TestOfferListsFilesAndAsks(t *testing.T) {
	p := New(&bytes.Buffer{}, &bytes.Buffer{})
	p.SetColor(false)

	files := make([]OfferFile, 10)
	for i := range files {
		files[i] = OfferFile{Name: "photo.jpg", Size: 1 << 20}
	}
	got := strings.Join(p.Offer(files, 10<<20), "\n")
	for _, want := range []string{"Someone wants to send 10 files (10 MB)", "photo.jpg", "1.0 MB", "… and 2 more", "press y to accept, n to decline"} {
		if !strings.Contains(got, want) {
			t.Errorf("offer missing %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "photo.jpg"); n != maxOfferLines {
		t.Errorf("offer lists %d files, want %d before summing up", n, maxOfferLines)
	}
}
