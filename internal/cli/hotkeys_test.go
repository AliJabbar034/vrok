package cli

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/AliJabbar034/vrok/internal/config"
	"github.com/AliJabbar034/vrok/internal/sharing"
	"github.com/AliJabbar034/vrok/internal/ui"
)

func newKeyTest(t *testing.T) (*sharer, liveShare, *bytes.Buffer, *bool) {
	t.Helper()
	var out bytes.Buffer
	p := ui.New(&out, &out)
	p.SetColor(false)

	stopped := false
	s := &sharer{
		app:        &app{printer: p, config: config.Default()},
		tunnelKind: "local",
		stop: func() {
			stopped = true
		},
	}
	share := sharing.New(sharing.Spec{ID: "id", Token: "tok", Name: "a.txt"})
	live := liveShare{
		share:     share,
		publicURL: "http://127.0.0.1:8080/s/tok/",
		cooked:    func(fn func()) { fn() },
	}
	return s, live, &out, &stopped
}

func TestHandleKeyStops(t *testing.T) {
	for _, key := range []byte{3, 4, 'x', 'X'} {
		s, live, _, stopped := newKeyTest(t)
		if !s.handleKey(key, live) {
			t.Errorf("key %q did not end the key loop", key)
		}
		if !*stopped {
			t.Errorf("key %q did not stop the share", key)
		}
	}
}

func TestHandleKeyOneTimeKeepsTheURL(t *testing.T) {
	s, live, out, stopped := newKeyTest(t)
	token := live.share.Token()

	if s.handleKey('1', live) {
		t.Fatal("1 ended the key loop")
	}
	if *stopped {
		t.Fatal("1 stopped the share")
	}
	if live.share.Token() != token {
		t.Fatal("1 rotated the URL token")
	}
	if got := live.share.Snapshot().MaxDownloads; got != 1 {
		t.Fatalf("MaxDownloads = %d, want 1", got)
	}
	if !strings.Contains(out.String(), "one-time link · same URL") {
		t.Fatalf("missing confirmation:\n%s", out.String())
	}
}

// Pressing 1 after someone has downloaded must still allow one more,
// rather than closing the share on the spot.
func TestHandleKeyOneTimeCountsFromNow(t *testing.T) {
	s, live, out, _ := newKeyTest(t)
	now := time.Now()
	for range 2 {
		if _, err := live.share.ClaimDownload(now); err != nil {
			t.Fatal(err)
		}
	}

	s.handleKey('1', live)

	if _, err := live.share.ClaimDownload(now); err != nil {
		t.Fatalf("the next download after pressing 1 was refused: %v", err)
	}
	if _, err := live.share.ClaimDownload(now); !errors.Is(err, sharing.ErrDownloadLimit) {
		t.Fatalf("second download after pressing 1 returned %v, want ErrDownloadLimit", err)
	}
	if !strings.Contains(out.String(), "one download left") {
		t.Fatalf("status should say one download is left:\n%s", out.String())
	}
}

func TestHandleKeyIgnoresUnknownKeys(t *testing.T) {
	s, live, out, stopped := newKeyTest(t)
	if s.handleKey('z', live) || *stopped {
		t.Fatal("an unknown key stopped the share")
	}
	if out.Len() != 0 {
		t.Fatalf("an unknown key printed output:\n%s", out.String())
	}
}

func TestHandleKeyRunsPromptsCooked(t *testing.T) {
	s, live, _, _ := newKeyTest(t)
	cooked := 0
	live.cooked = func(fn func()) { cooked++; fn() }
	s.handleKey('h', live)
	if cooked != 1 {
		t.Fatalf("h ran %d times outside raw mode, want 1", cooked)
	}
}

// readLine must stop at the newline so keys typed afterwards still reach
// the hotkey loop.
func TestReadLineDoesNotReadPastTheNewline(t *testing.T) {
	in := strings.NewReader(" 45m \nqx")
	line, err := readLine(in)
	if err != nil {
		t.Fatal(err)
	}
	if line != "45m" {
		t.Fatalf("readLine = %q, want 45m", line)
	}
	rest, _ := io.ReadAll(in)
	if string(rest) != "qx" {
		t.Fatalf("readLine consumed past the newline; left %q", rest)
	}
}

func TestReadLineAtEOF(t *testing.T) {
	if line, err := readLine(strings.NewReader("2h")); err != nil || line != "2h" {
		t.Fatalf("readLine without newline = %q, %v", line, err)
	}
	if _, err := readLine(strings.NewReader("")); !errors.Is(err, io.EOF) {
		t.Fatalf("readLine on empty input = %v, want EOF", err)
	}
}

// A closed terminal guard must never go raw again, so a prompt still open
// when the share ends cannot leave the shell without echo.
func TestRawTerminalStaysCookedAfterClose(t *testing.T) {
	raw := newRawTerminal()
	raw.close()
	if err := raw.enter(); err != nil {
		t.Fatalf("enter after close: %v", err)
	}
	if raw.saved != nil {
		t.Fatal("enter after close switched the terminal to raw")
	}
}
