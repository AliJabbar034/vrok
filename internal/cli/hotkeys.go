package cli

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/AliJabbar034/vrok/internal/config"
	"github.com/AliJabbar034/vrok/internal/security"
	"github.com/AliJabbar034/vrok/internal/sharing"
	"github.com/AliJabbar034/vrok/internal/ui"
	"golang.org/x/term"
)

// rawTerminal holds stdin in raw mode while live keys are on, and makes sure
// the terminal is restored no matter which goroutine finishes first. Once
// closed it never goes raw again, so a prompt that was open when the share
// ended cannot leave the user's shell without echo.
type rawTerminal struct {
	mu     sync.Mutex
	fd     int
	saved  *term.State // non-nil while raw
	closed bool
}

func newRawTerminal() *rawTerminal { return &rawTerminal{fd: int(os.Stdin.Fd())} }

// enter switches to raw mode. It is a no-op once closed or already raw.
func (t *rawTerminal) enter() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.saved != nil {
		return nil
	}
	saved, err := term.MakeRaw(t.fd)
	if err != nil {
		return err
	}
	t.saved = saved
	return nil
}

// leave restores the terminal so a prompt can read a cooked line.
func (t *rawTerminal) leave() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.restore()
}

// close restores the terminal for good. run calls it before printing the
// final stats: raw mode would staircase them and could outlive the process.
func (t *rawTerminal) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	t.restore()
}

func (t *rawTerminal) restore() {
	if t.saved != nil {
		_ = term.Restore(t.fd, t.saved)
		t.saved = nil
	}
}

// cooked runs fn with the terminal back in normal line mode.
func (t *rawTerminal) cooked(fn func()) {
	t.leave()
	fn()
	_ = t.enter()
}

// liveShare is what a hotkey acts on.
type liveShare struct {
	share     *sharing.Share
	hasher    security.Hasher
	publicURL string
	// cooked runs a prompt outside raw mode.
	cooked func(func())
}

// serveKeys reads single-key commands from a TTY and applies them to the
// live share. Each key changes the share in place: the URL never rotates.
// When stdin or stdout is not a terminal this returns immediately, so scripts
// and CI see an unchanged banner.
func (s *sharer) serveKeys(ctx context.Context, raw *rawTerminal, line *liveLine, share *sharing.Share, hasher security.Hasher, publicURL string) {
	if !hasTerminal() {
		return
	}
	if err := raw.enter(); err != nil {
		return
	}
	defer raw.leave()

	cooked := raw.cooked
	if line != nil {
		// A prompt or a hotkey's output must not be drawn over by progress.
		cooked = func(fn func()) {
			line.pause()
			defer line.resume()
			raw.cooked(fn)
		}
	}
	live := liveShare{share: share, hasher: hasher, publicURL: publicURL, cooked: cooked}

	// The reader only calls Read when we have asked it to, so a password or
	// expiry prompt can take stdin without racing a blocked Read.
	keys := make(chan byte)
	ready := make(chan struct{})
	go func() {
		buf := make([]byte, 1)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ready:
			}
			n, err := os.Stdin.Read(buf)
			if err != nil || n == 0 {
				return
			}
			select {
			case keys <- buf[0]:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case ready <- struct{}{}:
		}

		select {
		case <-ctx.Done():
			return
		case b := <-keys:
			if s.handleKey(b, live) {
				return
			}
		}
	}
}

// handleKey applies one hotkey and reports whether the share should stop.
func (s *sharer) handleKey(b byte, live liveShare) (stop bool) {
	switch b {
	case 3, 4, 'x', 'X': // Ctrl+C, Ctrl+D
		s.shutdown()
		return true
	case 26: // Ctrl+Z
		live.cooked(func() {
			suspend()
			s.app.printer.Keys()
		})
	case 'c', 'C':
		live.cooked(func() { s.copyURL(live.publicURL) })
	case 'q', 'Q':
		live.cooked(func() {
			s.app.printer.QR(live.publicURL)
			s.app.printer.Keys()
		})
	case 'p', 'P':
		live.cooked(func() { s.livePassword(live) })
	case 'e', 'E':
		live.cooked(func() { s.liveExpiry(live) })
	case '1':
		live.cooked(func() { s.liveOneTime(live) })
	case 'h', 'H':
		live.cooked(func() { s.app.printer.Keys() })
	}
	return false
}

func (s *sharer) copyURL(publicURL string) {
	if err := ui.Copy(publicURL); err != nil {
		s.app.printer.Warn("could not copy to clipboard: %v", err)
		return
	}
	s.app.printer.Info("  %s", s.app.printer.Dim("copied to clipboard"))
}

// livePassword always prompts. Unlike --password it ignores VROK_PASSWORD:
// pressing a key should never apply a secret the owner did not just type.
func (s *sharer) livePassword(live liveShare) {
	password, err := promptPassword(s.app.printer)
	if err != nil {
		s.app.printer.Warn("%v", err)
		return
	}
	if password == "" {
		s.app.printer.Info("  %s", s.app.printer.Dim("password unchanged"))
		return
	}
	hash, err := live.hasher.Hash(password)
	if err != nil {
		s.app.printer.Warn("could not set password: %v", err)
		return
	}
	live.share.SetPasswordHash(hash)
	s.confirmLive(live)
}

func (s *sharer) liveExpiry(live liveShare) {
	fmt.Fprint(s.app.printer.Out(), "New lifetime (30m, 2h, 1d, 0): ")
	line, err := readLine(os.Stdin)
	if err != nil {
		s.app.printer.Warn("%v", err)
		return
	}
	if line == "" {
		s.app.printer.Info("  %s", s.app.printer.Dim("expiry unchanged"))
		return
	}
	ttl, err := config.ParseTTL(line)
	if err != nil {
		s.app.printer.Warn("%v", err)
		return
	}
	var expires time.Time
	if ttl > 0 {
		expires = time.Now().Add(ttl)
	}
	live.share.SetExpiresAt(expires)
	s.confirmLive(live)
}

// liveOneTime allows exactly one more download from now, so pressing 1 after
// someone has already downloaded does not close the share on the spot.
func (s *sharer) liveOneTime(live liveShare) {
	live.share.AllowOneMore()
	s.confirmLive(live)
}

func (s *sharer) confirmLive(live liveShare) {
	view := s.shareView(live.share, live.publicURL)
	s.app.printer.Info("  %s", s.app.printer.Dim(ui.StatusLine(view)+" · same URL"))
	s.app.printer.Keys()
}
