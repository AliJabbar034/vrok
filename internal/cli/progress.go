package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/AliJabbar034/vrok/internal/awake"
	"github.com/AliJabbar034/vrok/internal/sharing"
	"github.com/AliJabbar034/vrok/internal/ui"
	"golang.org/x/term"
)

// progressTick is how often transfers are sampled. Once a second is smooth
// enough to read and cheap enough not to matter.
const progressTick = time.Second

// minBurst is the shortest stretch of downloading worth a permanent line.
// Shorter ones are page views and small files; a line for each would bury
// the banner.
const minBurst = 3 * time.Second

// liveLine is the one terminal line redrawn in place while downloads run.
//
// It always writes "\r" and "\r\n" itself because the terminal is in raw
// mode for the hotkeys, where a bare "\n" moves down without returning to
// the first column.
type liveLine struct {
	mu      sync.Mutex
	out     io.Writer
	printer *ui.Printer
	shown   bool
	paused  bool
}

// show replaces the line with text. Nothing is drawn while a prompt has the
// terminal.
func (l *liveLine) show(text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.paused {
		return
	}
	if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		text = ui.Fit(text, width)
	}
	fmt.Fprint(l.out, "\r\033[2K"+l.printer.Dim(text))
	l.shown = true
}

// print leaves a permanent line above where the live line is drawn.
func (l *liveLine) print(text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.paused {
		return
	}
	l.clear()
	fmt.Fprint(l.out, "  "+l.printer.Dim(text)+"\r\n")
}

// hide removes the line.
func (l *liveLine) hide() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.clear()
}

// pause hides the line and keeps it hidden until resume, so a hotkey's
// output and prompts are never drawn over.
func (l *liveLine) pause() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.clear()
	l.paused = true
}

func (l *liveLine) resume() {
	l.mu.Lock()
	l.paused = false
	l.mu.Unlock()
}

func (l *liveLine) clear() {
	if l.shown {
		fmt.Fprint(l.out, "\r\033[2K")
		l.shown = false
	}
}

// watchTransfers samples the share once a tick. It keeps the machine awake
// while anything is downloading and, when line is not nil, draws live
// progress. It returns when ctx ends, with the line hidden and the machine
// allowed to sleep.
func (s *sharer) watchTransfers(ctx context.Context, share *sharing.Share, line *liveLine) {
	keeper := awake.New()
	defer keeper.Close()
	if line != nil {
		defer line.hide()
	}

	// Sampling starts now, not at the first tick, so a download that begins
	// straight away has a speed on its first line.
	var (
		rate       float64
		lastBytes  = share.Snapshot().BytesTransferred
		lastAt     = time.Now()
		burstStart time.Time
		burstBase  int64
	)
	ticker := time.NewTicker(progressTick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			snap := share.Snapshot()
			busy := snap.ActiveTransfers > 0
			keeper.Update(busy, now)

			if elapsed := now.Sub(lastAt).Seconds(); elapsed > 0 {
				rate = smooth(rate, float64(snap.BytesTransferred-lastBytes)/elapsed)
			}

			switch {
			case busy && burstStart.IsZero():
				// The burst began some time since the previous sample, and
				// the bytes sent in between belong to it.
				burstStart, burstBase = lastAt, lastBytes
			case !busy && !burstStart.IsZero():
				took := now.Sub(burstStart)
				if line != nil && took >= minBurst {
					line.print(ui.BurstLine(snap.BytesTransferred-burstBase, took))
				}
				burstStart, rate = time.Time{}, 0
			}
			lastBytes, lastAt = snap.BytesTransferred, now

			if line == nil {
				continue
			}
			if !busy {
				line.hide()
				continue
			}
			transfers := make([]ui.Transfer, len(snap.Transfers))
			for i, t := range snap.Transfers {
				transfers[i] = ui.Transfer{Sent: t.Sent, Total: t.Total}
			}
			line.show(ui.ProgressLine(transfers, rate))
		}
	}
}

// smooth blends a new speed sample into the running one, so the figure does
// not jump with every burst of network buffering.
func smooth(current, sample float64) float64 {
	if current == 0 {
		return sample
	}
	return 0.3*sample + 0.7*current
}
