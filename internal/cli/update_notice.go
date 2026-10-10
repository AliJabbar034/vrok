package cli

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/AliJabbar034/vrok/internal/config"
	"github.com/AliJabbar034/vrok/internal/ui"
	"github.com/AliJabbar034/vrok/internal/update"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const (
	// updateCheckTimeout bounds the background request, so a slow network
	// can never hold the notice back for long.
	updateCheckTimeout = 3 * time.Second
	// updateCheckGrace is how long a command that has already finished waits
	// for a check still in flight. Short commands like `vrok list` would
	// otherwise exit before the check ever completes; a cold connection to
	// GitHub takes most of a second. It is only spent on the run that checks.
	updateCheckGrace = 1500 * time.Millisecond
	// updateRetryAfter is how soon a check that did not finish is tried again.
	updateRetryAfter = time.Hour
)

// quietCommands report versions themselves or print machine-read output, so a
// notice after them would be noise or would corrupt the output.
var quietCommands = map[string]bool{
	"update":                        true,
	"doctor":                        true,
	"completion":                    true,
	"help":                          true,
	cobra.ShellCompRequestCmd:       true,
	cobra.ShellCompNoDescRequestCmd: true,
}

// updateNotifier tells someone at a terminal that a newer release exists. The
// check runs in the background at most once a day, so no command waits on
// GitHub. A share shows the notice in its banner, the way ngrok does; any
// other command, or a share that started before the check finished, gets it
// after the command. Either way it appears once per run.
type updateNotifier struct {
	current string
	now     func() time.Time

	mu        sync.Mutex
	active    bool
	path      string
	state     update.State
	result    chan string
	collected bool
	shown     bool
	dirty     bool
}

func newUpdateNotifier(version string) *updateNotifier {
	return &updateNotifier{current: releaseVersion(version), now: time.Now}
}

// noticeAllowed decides whether this run may show a notice at all.
func noticeAllowed(getenv func(string) string, terminal bool, command, current string, cfg config.Config) bool {
	if getenv("VROK_NO_UPDATE_NOTIFIER") != "" || getenv("CI") != "" {
		return false
	}
	if !terminal || quietCommands[command] {
		return false
	}
	if cfg.UpdateCheck != nil && !*cfg.UpdateCheck {
		return false
	}
	// A "dev" build has no release to compare against.
	_, err := update.Parse(current)
	return err == nil
}

// topLevelName names the subcommand directly under the root, so that
// `vrok completion zsh` is recognised as completion.
func topLevelName(cmd *cobra.Command) string {
	for cmd.HasParent() && cmd.Parent().HasParent() {
		cmd = cmd.Parent()
	}
	return cmd.Name()
}

// start runs before the command. It reads the cached state and, when the last
// check is a day old, asks GitHub in the background.
func (n *updateNotifier) start(cmd *cobra.Command) {
	cfg, _ := config.Load()
	terminal := hasTerminal() && term.IsTerminal(int(os.Stderr.Fd()))
	if !noticeAllowed(os.Getenv, terminal, topLevelName(cmd), n.current, cfg) {
		return
	}
	path, err := update.StatePath()
	if err != nil {
		return
	}
	n.begin(path)
}

// begin loads the cached state from path and starts a check if it is stale.
func (n *updateNotifier) begin(path string) {
	n.active, n.path = true, path
	n.state = update.LoadState(path)
	if !n.state.Stale(n.now()) {
		return
	}

	n.result = make(chan string, 1)
	go func() {
		// Not the command's context: Ctrl+C ends a share, and the check
		// should still be able to finish during the grace period.
		ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
		defer cancel()
		tag, err := latestRelease(ctx, networkClient)
		if err != nil {
			tag = ""
		}
		n.result <- tag
	}()
}

// collect folds a finished check into the state, waiting up to wait for one
// still in flight. It reports whether no check is outstanding afterwards.
// The caller holds n.mu.
func (n *updateNotifier) collect(wait time.Duration) bool {
	if n.result == nil || n.collected {
		return true
	}
	var tag string
	select {
	case tag = <-n.result:
	default:
		if wait <= 0 {
			return false
		}
		select {
		case tag = <-n.result:
		case <-time.After(wait):
			return false
		}
	}
	// A failed check still counts, so an offline machine tries again
	// tomorrow rather than on every command.
	n.collected, n.dirty = true, true
	n.state.CheckedAt = n.now()
	if tag != "" {
		n.state.Latest = tag
	}
	return true
}

// take returns the notice to show now and records it as shown, or false when
// there is none or it has already been shown this run. The caller holds n.mu.
func (n *updateNotifier) take() (ui.UpdateNotice, bool) {
	if n.shown {
		return ui.UpdateNotice{}, false
	}
	now := n.now()
	latest := n.state.Pending(n.current, now)
	if latest == "" {
		return ui.UpdateNotice{}, false
	}
	n.shown, n.dirty = true, true
	n.state.NotifiedFor, n.state.NotifiedAt = latest, now
	return ui.UpdateNotice{
		Current: strings.TrimPrefix(n.current, "v"),
		Latest:  strings.TrimPrefix(latest, "v"),
		Command: updateCommand(),
		URL:     update.ReleaseURL(latest),
	}, true
}

// claim hands the notice to the share banner if one is ready, without
// waiting: a banner is never held back for GitHub. Once claimed, finish
// prints nothing, so the notice is not repeated when the share stops.
func (n *updateNotifier) claim() (ui.UpdateNotice, bool) {
	if n == nil {
		return ui.UpdateNotice{}, false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.active {
		return ui.UpdateNotice{}, false
	}
	n.collect(0)
	return n.take()
}

// finish runs after the command, whatever its outcome. It prints the notice
// unless the banner already showed it, and saves what was learned.
func (n *updateNotifier) finish(p *ui.Printer) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.active {
		return
	}
	if !n.collect(updateCheckGrace) {
		n.state.CheckedAt = n.now().Add(updateRetryAfter - update.CheckInterval)
		n.dirty = true
	}
	if notice, ok := n.take(); ok {
		p.UpdateNotice(notice, stderrWidth())
	}
	if n.dirty {
		_ = update.SaveState(n.path, n.state)
	}
}

// updateCommand is what updates this install: the package manager's command
// when one owns it, `vrok update` otherwise.
func updateCommand() string {
	exe, err := update.Executable()
	if err != nil {
		return "vrok update"
	}
	if cmd := update.Detect(exe).Command(); cmd != "" {
		return cmd
	}
	return "vrok update"
}

func stderrWidth() int {
	width, _, err := term.GetSize(int(os.Stderr.Fd()))
	if err != nil {
		return 0
	}
	return width
}
