// Command vrok-relay is the public endpoint for vrok's own tunnel.
//
// It accepts WebSocket tunnels from vrok CLIs and routes inbound HTTPS
// requests into them. It stores nothing: every byte a visitor downloads is
// read from the sharing machine while they wait.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/AliJabbar034/vrok/internal/relay"
)

var (
	version = "dev"
	commit  = "none"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "vrok-relay: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Flags are plain stdlib here: the relay is an operator-facing daemon
	// configured once, not an interactive tool.
	var (
		addr      = flag.String("addr", envOr("VROK_RELAY_ADDR", ":8787"), "listen address")
		domain    = flag.String("domain", os.Getenv("VROK_RELAY_DOMAIN"), "wildcard domain shares appear under, e.g. vrok.example.com")
		scheme    = flag.String("scheme", envOr("VROK_RELAY_SCHEME", "https"), "scheme the relay is reachable on from the internet")
		token     = flag.String("token", os.Getenv("VROK_RELAY_TOKEN"), "require this credential from agents")
		maxAgents = flag.Int("max-agents", 0, "maximum concurrent tunnels (0 for unlimited)")
		tlsCert   = flag.String("tls-cert", os.Getenv("VROK_RELAY_TLS_CERT"), "certificate for *.<domain>; set with -tls-key to terminate TLS here")
		tlsKey    = flag.String("tls-key", os.Getenv("VROK_RELAY_TLS_KEY"), "private key matching -tls-cert")
		verbose   = flag.Bool("verbose", false, "log at debug level")
		showVer   = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Printf("vrok-relay %s (%s)\n", version, commit)
		return nil
	}
	if *domain == "" {
		return fmt.Errorf("-domain is required (a wildcard DNS record must point at this relay)")
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	// An open relay lets anyone on the internet serve content under this
	// domain, which is how a relay ends up hosting someone else's phishing
	// page. Allowed, for a quick test, but never silently.
	if *token == "" {
		logger.Warn("relay is open: any agent can serve content under this domain; " +
			"set -token or VROK_RELAY_TOKEN to require a credential")
	}

	server, err := relay.New(relay.Options{
		Addr:      *addr,
		Domain:    *domain,
		Scheme:    *scheme,
		AuthToken: *token,
		MaxAgents: *maxAgents,
		TLSCert:   *tlsCert,
		TLSKey:    *tlsKey,
		Logger:    logger,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return server.ListenAndServe(ctx)
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
