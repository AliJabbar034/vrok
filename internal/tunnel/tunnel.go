// Package tunnel publishes vrok's local HTTP server on a public address.
//
// Everything in this package sits behind the Tunnel interface. Nothing above
// it knows whether a share is reachable through Cloudflare, vrok's own relay
// or nothing at all, which is what keeps vrok from being welded to one
// provider.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// PublicURL is the externally reachable origin a tunnel assigned, such as
// "https://abc123.trycloudflare.com".
type PublicURL string

// String implements fmt.Stringer.
func (u PublicURL) String() string { return string(u) }

// Join appends a path to the public origin.
func (u PublicURL) Join(path string) string {
	if u == "" {
		return path
	}
	return strings.TrimSuffix(string(u), "/") + "/" + strings.TrimPrefix(path, "/")
}

// Tunnel exposes a local target on a public URL.
//
// Implementations must be safe to Stop even if Start failed, so that callers
// can always defer cleanup.
type Tunnel interface {
	// Name is the provider's identifier, as used by --tunnel.
	Name() string
	// Start publishes target, a local origin such as "http://127.0.0.1:8080",
	// and returns the public URL it became reachable on.
	Start(ctx context.Context, target string) (PublicURL, error)
	// Stop tears the tunnel down.
	Stop(ctx context.Context) error
}

// ErrNotInstalled reports that a provider's binary is missing. It carries the
// hint the CLI prints, so the user is told how to fix it rather than just that
// something is absent.
type ErrNotInstalled struct {
	Provider string
	Binary   string
	Hint     string
}

// Error implements error.
func (e *ErrNotInstalled) Error() string {
	return fmt.Sprintf("tunnel: %s requires %q, which is not installed. %s", e.Provider, e.Binary, e.Hint)
}

// ErrUnknownProvider is returned for a --tunnel value with no implementation.
var ErrUnknownProvider = errors.New("tunnel: unknown provider")

// Config carries everything a provider might need to build itself. Providers
// ignore the fields that do not apply to them.
type Config struct {
	// Logger receives provider diagnostics.
	Logger *slog.Logger
	// RelayURL is the vrok relay endpoint, used by the relay provider.
	RelayURL string
	// Label is the share id. The relay turns it into a hostname label.
	Label string
	// ShareToken is the share secret, echoed to the relay so the agent can
	// confirm which share a registration refers to.
	ShareToken string
	// Domain requests a specific custom domain, for providers that support
	// reserved domains.
	Domain string
	// Auth is a provider or relay credential.
	Auth string
	// Notify reports one-time setup progress to the user. A provider that
	// needs to fetch something tells them so through this rather than
	// appearing to hang. Optional.
	Notify func(format string, args ...any)
}

// Factory builds a provider from a Config.
type Factory func(Config) (Tunnel, error)

// providers is the registry of implementations. Each file in this package adds
// itself in an init function, so a new provider is a new file and nothing
// else: no switch statement anywhere needs editing.
var providers = map[string]Factory{}

// Register adds a provider under name. It panics on a duplicate, which can
// only be a programming error at init time.
func Register(name string, factory Factory) {
	if _, exists := providers[name]; exists {
		panic("tunnel: provider registered twice: " + name)
	}
	providers[name] = factory
}

// Open builds the named provider.
func Open(name string, cfg Config) (Tunnel, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	factory, ok := providers[name]
	if !ok {
		return nil, fmt.Errorf("%w %q (available: %s)", ErrUnknownProvider, name, strings.Join(Available(), ", "))
	}
	return factory(cfg)
}

// Available lists the registered provider names.
func Available() []string {
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
