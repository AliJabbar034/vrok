package tunnel

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

func init() { Register("auto", newAuto) }

// newAuto builds the default provider: the one that gets a public URL without
// the user choosing, configuring or installing anything.
func newAuto(cfg Config) (Tunnel, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &autoTunnel{cfg: cfg, logger: logger.With(slog.String("tunnel", "auto"))}, nil
}

// autoTunnel tries the available ways out to the internet in order and keeps
// the first that works.
//
// Order matters. A relay the user configured is theirs and is preferred over
// anyone else's service. An already-installed client is preferred over
// downloading one. Fetching cloudflared is last, because it is the only
// option that costs the user a download.
type autoTunnel struct {
	cfg    Config
	logger *slog.Logger

	mu       sync.Mutex
	delegate Tunnel
}

// Name reports the provider that was actually chosen, so the banner and
// `vrok list` name the real route rather than the word "auto".
func (a *autoTunnel) Name() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.delegate == nil {
		return "auto"
	}
	return a.delegate.Name()
}

// candidates lists the providers to try, in order, for this configuration.
func (a *autoTunnel) candidates() []string {
	names := make([]string, 0, 4)
	if a.cfg.RelayURL != "" {
		names = append(names, "relay")
	}
	// cloudflared on PATH is preferred over fetching a copy.
	return append(names, "cloudflare")
}

// Start publishes target on the first route that works.
func (a *autoTunnel) Start(ctx context.Context, target string) (PublicURL, error) {
	var failures []error

	for _, name := range a.candidates() {
		provider, err := Open(name, a.cfg)
		if err != nil {
			failures = append(failures, err)
			continue
		}

		public, err := provider.Start(ctx, target)
		if err == nil {
			a.mu.Lock()
			a.delegate = provider
			a.mu.Unlock()
			a.logger.Debug("selected provider", slog.String("provider", name))
			return public, nil
		}

		// A missing client is not a failure worth reporting: it is the normal
		// state of a machine that has never used that provider. Anything else
		// is worth keeping in case every route fails.
		var missing *ErrNotInstalled
		if !errors.As(err, &missing) {
			a.logger.Debug("provider failed", slog.String("provider", name), slog.String("error", err.Error()))
			failures = append(failures, err)
		}
		// The context being gone means the user interrupted; trying the next
		// provider would just produce the same error more slowly.
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}

	// Nothing was installed and no relay was configured, so fetch a client.
	public, err := a.startProvisioned(ctx, target)
	if err == nil {
		return public, nil
	}
	failures = append(failures, err)
	return "", &ErrNoRoute{Causes: failures}
}

// ErrNoRoute reports that no route to the internet could be opened.
//
// It does not fall back to a loopback URL. The user asked to share, and
// handing them a link only they can open — after they watched it try to go
// public — would be a downgrade disguised as success.
type ErrNoRoute struct {
	Causes []error
}

// Error implements error.
func (e *ErrNoRoute) Error() string {
	return "tunnel: could not open a public URL, and vrok will not hand you a link " +
		"that only works on this machine.\n\n" +
		"If you are offline or behind a restrictive network:\n" +
		"    vrok <path> --local            share on your network instead\n" +
		"    vrok <path> --tunnel local     share with this machine only\n\n" +
		"If you would rather use a provider you control:\n" +
		"    vrok config set relay-url https://relay.example.com\n\n" +
		"To see what is blocking it, run:  vrok doctor\n\n" +
		"Underlying failure: " + errors.Join(e.Causes...).Error()
}

// Unwrap exposes the individual failures to errors.Is and errors.As.
func (e *ErrNoRoute) Unwrap() []error { return e.Causes }

// startProvisioned downloads cloudflared and tunnels through it. The quick
// tunnel it opens needs no Cloudflare account, which is what makes a public
// URL possible for a user who has set nothing up.
func (a *autoTunnel) startProvisioned(ctx context.Context, target string) (PublicURL, error) {
	fetcher := &provisioner{logger: a.logger, notify: a.cfg.Notify}
	provider := newProcessTunnel(quickTunnelSpec(fetcher), a.cfg)

	public, err := provider.Start(ctx, target)
	if err != nil {
		return "", err
	}

	a.mu.Lock()
	a.delegate = provider
	a.mu.Unlock()
	return public, nil
}

// quickTunnelSpec is the cloudflare provider with its binary resolved by
// fetching rather than by a PATH lookup.
func quickTunnelSpec(fetcher *provisioner) processSpec {
	spec := cloudflareSpec()
	spec.locate = fetcher.executable
	return spec
}

// Stop tears down whichever provider was chosen.
func (a *autoTunnel) Stop(ctx context.Context) error {
	a.mu.Lock()
	delegate := a.delegate
	a.mu.Unlock()

	if delegate == nil {
		return nil
	}
	return delegate.Stop(ctx)
}
