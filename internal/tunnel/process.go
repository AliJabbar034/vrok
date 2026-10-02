package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os/exec"
	"regexp"
	"sync"
	"time"
)

// readyTimeout is how long a provider gets to report its public URL before
// vrok gives up. Cold-starting cloudflared on a slow link takes a few seconds;
// thirty is generous without hanging the terminal indefinitely.
const readyTimeout = 30 * time.Second

// maxLogLine caps a single line of provider output. Some providers emit large
// JSON records, and the default scanner limit is too small for them.
const maxLogLine = 1 << 20

// processSpec describes how to drive one external tunnel binary.
//
// A hosted provider that is an external binary differs only in its command
// line and the URL it prints, so it is a configuration of this implementation
// rather than a second one.
type processSpec struct {
	// provider is the --tunnel name.
	provider string
	// binary is the executable to look up on PATH.
	binary string
	// hint tells the user how to install the binary.
	hint string
	// locate resolves the executable. It defaults to a PATH lookup; the auto
	// provider supplies one that can fetch the binary instead, which is the
	// only difference between "cloudflared is installed" and "it is not, yet".
	locate func(ctx context.Context) (string, error)
	// args builds the command line for a local target.
	args func(target *url.URL) []string
	// urlPatterns match the public URL in the provider's output. The first
	// match wins.
	urlPatterns []*regexp.Regexp
}

// resolve finds the executable to run, falling back to a PATH lookup. A
// missing binary is reported as ErrNotInstalled so callers can tell "not
// available here" apart from "tried and failed".
func (s processSpec) resolve(ctx context.Context) (string, error) {
	if s.locate != nil {
		return s.locate(ctx)
	}
	path, err := exec.LookPath(s.binary)
	if err != nil {
		return "", &ErrNotInstalled{Provider: s.provider, Binary: s.binary, Hint: s.hint}
	}
	return path, nil
}

// processTunnel runs an external tunnel client as a child process and scrapes
// its output for the assigned URL.
type processTunnel struct {
	spec   processSpec
	logger *slog.Logger

	mu      sync.Mutex
	cancel  context.CancelFunc
	exited  chan struct{}
	waitErr error
}

func newProcessTunnel(spec processSpec, cfg Config) *processTunnel {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &processTunnel{spec: spec, logger: logger.With(slog.String("tunnel", spec.provider))}
}

// Name implements Tunnel.
func (t *processTunnel) Name() string { return t.spec.provider }

// Start launches the provider and waits for it to announce a public URL.
func (t *processTunnel) Start(ctx context.Context, target string) (PublicURL, error) {
	executable, err := t.spec.resolve(ctx)
	if err != nil {
		return "", err
	}

	parsed, err := url.Parse(target)
	if err != nil {
		return "", fmt.Errorf("tunnel: parse target %q: %w", target, err)
	}

	// The child's lifetime is tied to Stop, not to the context that Start was
	// called with: the caller's context is only used to bound how long we wait
	// for the URL to appear.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))

	cmd := exec.CommandContext(runCtx, executable, t.spec.args(parsed)...)
	// Ask politely first; exec kills the process if it ignores the request.
	cmd.Cancel = func() error { return terminate(cmd.Process) }
	cmd.WaitDelay = 5 * time.Second

	output, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return "", err
	}
	// Providers are inconsistent about which stream they log to, so both are
	// read through the same pipe.
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		cancel()
		return "", fmt.Errorf("tunnel: start %s: %w", t.spec.binary, err)
	}

	urls := make(chan PublicURL, 1)
	exited := make(chan struct{})

	go t.scan(output, urls)
	go func() {
		err := cmd.Wait()
		t.mu.Lock()
		t.waitErr = err
		t.mu.Unlock()
		close(exited)
	}()

	t.mu.Lock()
	t.cancel, t.exited = cancel, exited
	t.mu.Unlock()

	select {
	case u := <-urls:
		t.logger.Debug("tunnel ready", slog.String("url", u.String()))
		return u, nil

	case <-exited:
		_ = t.Stop(context.Background())
		return "", fmt.Errorf("tunnel: %s exited before reporting a URL: %w", t.spec.binary, t.err())

	case <-ctx.Done():
		_ = t.Stop(context.Background())
		return "", ctx.Err()

	case <-time.After(readyTimeout):
		_ = t.Stop(context.Background())
		return "", fmt.Errorf("tunnel: %s did not report a URL within %s", t.spec.binary, readyTimeout)
	}
}

// scan reads provider output, extracts the first public URL it sees and keeps
// draining afterwards. Draining matters: a full pipe would block the child.
func (t *processTunnel) scan(output io.Reader, urls chan<- PublicURL) {
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLogLine)

	found := false
	for scanner.Scan() {
		line := scanner.Text()
		t.logger.Debug("provider output", slog.String("line", line))
		if found {
			continue
		}
		for _, pattern := range t.spec.urlPatterns {
			match := pattern.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			// A pattern may capture the URL out of a larger field such as
			// `url=https://...`; prefer the capture when present.
			public := match[0]
			if len(match) > 1 && match[1] != "" {
				public = match[1]
			}
			found = true
			urls <- PublicURL(public)
			break
		}
	}
}

// Stop terminates the provider. It is safe to call on a tunnel that never
// started.
func (t *processTunnel) Stop(ctx context.Context) error {
	t.mu.Lock()
	cancel, exited := t.cancel, t.exited
	t.cancel = nil
	t.mu.Unlock()

	if cancel == nil {
		return nil
	}

	// A provider that has already exited died on its own, and that error is
	// worth reporting. One that is still running is about to be told to stop,
	// so whatever it reports afterwards is the expected outcome rather than a
	// failure — which also avoids having to ask each platform what a
	// terminated process looks like.
	select {
	case <-exited:
		return t.err()
	default:
	}

	cancel()

	select {
	case <-exited:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(10 * time.Second):
		return errors.New("tunnel: provider did not exit")
	}
}

func (t *processTunnel) err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.waitErr
}
