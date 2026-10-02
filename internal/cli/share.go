package cli

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/AliJabbar034/vrok/internal/config"
	"github.com/AliJabbar034/vrok/internal/control"
	"github.com/AliJabbar034/vrok/internal/security"
	"github.com/AliJabbar034/vrok/internal/server"
	"github.com/AliJabbar034/vrok/internal/sharing"
	"github.com/AliJabbar034/vrok/internal/tunnel"
	"github.com/AliJabbar034/vrok/internal/ui"
	"github.com/spf13/cobra"
)

// defaultLocalPort is the port used by --local. A fixed, memorable port makes
// a LAN URL easy to read off a screen and type into a phone.
const defaultLocalPort = 8080

// shutdownGrace is how long in-flight downloads get to finish when a share is
// stopped. Stopping is meant to be immediate, so this is short.
const shutdownGrace = 2 * time.Second

// shareOptions are the flags of the share command.
type shareOptions struct {
	ttl         string
	downloads   int
	password    bool
	name        string
	qr          bool
	local       bool
	port        int
	tunnelName  string
	relayURL    string
	relayToken  string
	domain      string
	listenAddr  string
	interactive bool
}

// sharer runs one sharing session: it owns the registry, the HTTP server and
// the tunnel for the life of the command.
//
// It also implements control.Provider, which is how `vrok list` and
// `vrok revoke` reach the shares that live in this process's memory.
type sharer struct {
	app  *app
	opts shareOptions

	registry   *sharing.Registry
	server     *server.Server
	publicURL  string
	tunnelKind string

	stopOnce sync.Once
	stop     context.CancelFunc
}

// newShareCommand returns the share command and the runner its flags are
// bound to, so the root command can reuse both.
func newShareCommand(a *app) (*cobra.Command, *sharer) {
	s := &sharer{app: a}

	cmd := &cobra.Command{
		Use:   "share [flags] <path>... | <localhost:port>",
		Short: "Share files, folders or a local HTTP server over a temporary URL",
		Long: `Share exposes local files, directories and development servers through a
temporary URL. Nothing is uploaded: requests are served from this machine for
as long as the command runs.

The URL stops working as soon as the share expires, its download limit is
reached, or you press Ctrl+C.`,
		Example: `  vrok share ./demo.mp4
  vrok share ./playwright-report
  vrok share report.pdf screenshot.png demo.mp4
  vrok share localhost:3000
  vrok share ./video.mp4 --ttl 30m --downloads 5 --password
  vrok share ./build --local --qr
  vrok share ./report.pdf --tunnel cloudflare
  vrok share -i ./demo.mp4`,
		Args:          cobra.ArbitraryArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			a.load()
			return s.run(cmd.Context(), args)
		},
	}

	bindShareFlags(cmd, &s.opts)
	return cmd, s
}

// bindShareFlags is shared by `vrok share` and the root command, so that
// `vrok ./file.mp4` accepts exactly the same flags as the long form.
func bindShareFlags(cmd *cobra.Command, opts *shareOptions) {
	f := cmd.Flags()
	f.StringVar(&opts.ttl, "ttl", "", "how long the share lives, e.g. 30m, 2h, 1d (default: until stopped)")
	f.IntVar(&opts.downloads, "downloads", 0, "stop sharing after this many downloads (0 for unlimited)")
	f.BoolVar(&opts.password, "password", false, "ask for a password that visitors must enter")
	f.StringVar(&opts.name, "name", "", "display name for the share")
	f.BoolVar(&opts.qr, "qr", false, "print a QR code for the share URL")
	f.BoolVar(&opts.local, "local", false, "serve on the local network only, with no public tunnel")
	f.IntVar(&opts.port, "port", 0, "local port to listen on (default: a free port, or 8080 with --local)")
	f.StringVar(&opts.tunnelName, "tunnel", "", "tunnel provider: "+joinProviders()+" (default "+config.DefaultTunnel+")")
	f.StringVar(&opts.relayURL, "relay-url", "", "relay to connect to when --tunnel relay is used")
	f.StringVar(&opts.relayToken, "relay-token", "", "credential for a private relay")
	f.StringVar(&opts.domain, "domain", "", "custom domain to request, where the provider supports it")
	f.StringVar(&opts.listenAddr, "listen", "", "explicit listen address, e.g. 127.0.0.1:9000")
	f.BoolVarP(&opts.interactive, "interactive", "i", false, "ask who can open it, how long it lasts, and the download limit")
}

func joinProviders() string { return strings.Join(tunnel.Available(), ", ") }

// run is the whole sharing flow: build the share, serve it, publish it, and
// wait until it is stopped or expires.
func (s *sharer) run(parent context.Context, args []string) error {
	if s.opts.interactive {
		if err := s.promptInteractive(args); err != nil {
			return err
		}
	}

	ttl, err := s.resolveTTL()
	if err != nil {
		return err
	}

	source, err := sharing.Classify(args)
	if err != nil {
		return err
	}

	password, err := s.resolvePassword()
	if err != nil {
		return err
	}
	if s.opts.downloads > 0 && source.Kind == sharing.KindHTTP {
		s.app.printer.Warn("--downloads has no effect when sharing an HTTP server; every asset request would count.")
		s.opts.downloads = 0
	}

	// Signals are handled here rather than deeper down so that every layer
	// below sees a single, ordinary context cancellation.
	ctx, cancel := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	s.stop = cancel

	hasher := security.NewArgon2Hasher(security.DefaultArgon2Params())
	factory := sharing.NewFactory(security.NewCryptoTokenSource(), hasher, sharing.SystemClock{})

	share, err := factory.Create(source, sharing.Options{
		TTL:          ttl,
		MaxDownloads: s.opts.downloads,
		Password:     password,
		Name:         s.opts.name,
	})
	if err != nil {
		return err
	}

	s.registry = sharing.NewRegistry()
	if err := s.registry.Add(share); err != nil {
		return err
	}

	if err := s.startServer(hasher); err != nil {
		return err
	}
	defer s.shutdownServer()

	publicURL, activeTunnel, err := s.startTunnel(ctx, share)
	if err != nil {
		return err
	}
	if activeTunnel != nil {
		defer func() {
			stopCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer done()
			if err := activeTunnel.Stop(stopCtx); err != nil {
				s.app.printer.Warn("tunnel did not shut down cleanly: %v", err)
			}
		}()
	}
	s.publicURL = publicURL

	controlServer := s.startControl()
	defer controlServer.Close()

	s.announce(share, publicURL)
	s.watchExpiry(ctx, share)
	s.server.Warm(share.Spec())

	raw := newRawTerminal()
	// Live progress needs the terminal to itself; with --verbose the request
	// log is already writing to it.
	var line *liveLine
	if hasTerminal() && !s.app.verbose {
		line = &liveLine{out: s.app.printer.Out(), printer: s.app.printer}
	}
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		s.watchTransfers(ctx, share, line)
	}()
	go s.serveKeys(ctx, raw, line, share, hasher, publicURL)

	<-ctx.Done()
	// The progress line goes first and the terminal comes back out of raw
	// mode before anything else is printed. The key loop is not waited for:
	// it may be parked in a prompt.
	<-watched
	raw.close()
	s.reportStats(share)
	return nil
}

// startServer binds the local HTTP server before anything is printed, so a
// port conflict fails before a URL is advertised.
func (s *sharer) startServer(hasher security.Hasher) error {
	signingKey, err := security.NewSecret()
	if err != nil {
		return err
	}

	srv, err := server.New(server.Options{
		Addr:     s.listenAddress(),
		Resolver: s.registry,
		Guards:   sharing.DefaultGuards(),
		Hasher:   hasher,
		Signer:   security.NewHMACSigner(signingKey),
		Logger:   s.app.logger(),
	})
	if err != nil {
		return err
	}
	if err := srv.Listen(); err != nil {
		return err
	}
	s.server = srv

	go func() {
		if err := srv.Serve(); err != nil {
			s.app.printer.Error("%v", err)
			s.shutdown()
		}
	}()
	return nil
}

func (s *sharer) shutdownServer() {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	s.server.Shutdown(ctx)
}

// listenAddress decides where the local server binds.
//
// Loopback is the default so that starting a share never exposes anything to
// the network by accident; --local is the explicit opt-in to the LAN.
func (s *sharer) listenAddress() string {
	if s.opts.listenAddr != "" {
		return s.opts.listenAddr
	}
	port := s.opts.port
	if s.opts.local {
		if port == 0 {
			port = defaultLocalPort
		}
		return ":" + strconv.Itoa(port)
	}
	if port != 0 {
		return "127.0.0.1:" + strconv.Itoa(port)
	}
	if s.app.config.Addr != "" {
		return s.app.config.Addr
	}
	return config.DefaultAddr
}

// startTunnel publishes the local server and returns the share's public URL.
func (s *sharer) startTunnel(ctx context.Context, share *sharing.Share) (string, tunnel.Tunnel, error) {
	name := s.tunnelProvider()
	s.tunnelKind = name

	provider, err := tunnel.Open(name, tunnel.Config{
		Logger:     s.app.logger(),
		RelayURL:   firstNonEmpty(s.opts.relayURL, s.app.config.RelayURL),
		Label:      share.ID(),
		ShareToken: share.Token(),
		Domain:     firstNonEmpty(s.opts.domain, s.app.config.Domain),
		Auth:       firstNonEmpty(s.opts.relayToken, s.app.config.RelayToken),
		Notify:     s.app.printer.Step,
	})
	if err != nil {
		return "", nil, err
	}

	target := "http://" + s.server.Addr()
	base, err := provider.Start(ctx, target)
	if err != nil {
		return "", nil, err
	}
	// `auto` only knows which route it took once it has taken it, and the
	// banner has to name the real one.
	s.tunnelKind = provider.Name()

	// The tunnel only publishes an origin; the share's own path is appended
	// here so that one tunnel could serve several shares.
	return base.Join(server.SharePath(share.Token())), provider, nil
}

// tunnelProvider resolves which provider to use. --local always wins, because
// it is a statement about privacy rather than a preference.
func (s *sharer) tunnelProvider() string {
	if s.opts.local {
		return "local"
	}
	if s.opts.tunnelName != "" {
		return s.opts.tunnelName
	}
	if s.app.config.Tunnel != "" {
		return s.app.config.Tunnel
	}
	return config.DefaultTunnel
}

// startControl exposes this process to the management commands. Failing to do
// so is not fatal: the share works, it just will not show up in `vrok list`.
func (s *sharer) startControl() *control.Server {
	srv := control.NewServer(s, s.app.logger())
	if err := srv.Start(); err != nil {
		s.app.printer.Warn("this share will not appear in `vrok list`: %v", err)
	}
	return srv
}

// reach describes who can open the URL, and how to widen that.
//
// It is decided from the address actually bound, not from the flags, so the
// banner cannot claim more than is true — `--local` on a machine with no
// usable network address falls back to loopback, and that has to show.
func (s *sharer) reach(publicURL string) (reach, hint string) {
	if s.tunnelKind != "local" {
		return "anyone with the link", ""
	}

	host := hostOf(publicURL)
	if host == "" || isLoopback(host) {
		return "this machine only",
			"Only you can open this URL. Add --local to share on your network, " +
				"or --tunnel " + firstHostedProvider() + " to share over the internet."
	}
	return "your local network",
		"Anyone on your network can open this URL. Use --tunnel " +
			firstHostedProvider() + " to share over the internet."
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// firstHostedProvider names a provider to suggest. The list is whatever is
// registered, so a new provider is offered without editing this message.
// "relay" is skipped because it needs a server the user does not have yet,
// and "auto" because suggesting it says nothing about which route to take.
func firstHostedProvider() string {
	for _, name := range tunnel.Available() {
		switch name {
		case "local", "relay", "auto":
			continue
		}
		return name
	}
	return "cloudflare"
}

func (s *sharer) shareView(share *sharing.Share, publicURL string) ui.ShareView {
	spec := share.Spec()
	view := ui.ShareView{
		Name:         spec.Name,
		URL:          publicURL,
		MaxDownloads: spec.MaxDownloads,
		Downloads:    share.Snapshot().Downloads,
		Protected:    spec.Protected(),
		Tunnel:       s.tunnelKind,
	}
	if !spec.ExpiresAt.IsZero() {
		view.TTL = time.Until(spec.ExpiresAt)
	}
	if s.server != nil && s.tunnelKind != "local" {
		view.LocalURL = "http://" + s.server.Addr() + server.SharePath(spec.Token)
	}
	view.Reach, view.Hint = s.reach(publicURL)
	return view
}

func (s *sharer) announce(share *sharing.Share, publicURL string) {
	view := s.shareView(share, publicURL)
	view.Interactive = hasTerminal()
	if view.Interactive {
		if err := ui.Copy(publicURL); err == nil {
			view.Copied = true
		}
	}

	s.app.printer.Started(view)
	if s.opts.qr || s.app.config.QR {
		s.app.printer.QR(publicURL)
	}
}

// watchExpiry ends the process when the share expires or runs out of
// downloads, so a `--ttl 30m` share does not leave a terminal occupied.
func (s *sharer) watchExpiry(ctx context.Context, share *sharing.Share) {
	reaper := sharing.NewReaper(s.registry, sharing.SystemClock{}, time.Second)
	reaper.OnEmpty = func() { s.shutdown() }
	go reaper.Run(ctx)
}

func (s *sharer) reportStats(share *sharing.Share) {
	snap := share.Snapshot()
	s.app.printer.Stopped(ui.Stats{
		Name:       snap.Name,
		Downloads:  snap.Downloads,
		Bytes:      snap.BytesTransferred,
		LastAccess: snap.LastAccess,
		Expired:    sharing.DefaultGuards().Check(snap, time.Now()) != nil,
	})
}

func (s *sharer) shutdown() {
	s.stopOnce.Do(func() {
		if s.stop != nil {
			s.stop()
		}
	})
}

// Shares implements control.Provider.
func (s *sharer) Shares() []control.ShareInfo {
	shares := s.registry.List()
	infos := make([]control.ShareInfo, 0, len(shares))
	for _, share := range shares {
		snap := share.Snapshot()
		infos = append(infos, control.ShareInfo{
			ID:           snap.ID,
			Name:         snap.Name,
			Kind:         snap.Kind.String(),
			Source:       share.Describe(),
			URL:          s.publicURL,
			CreatedAt:    snap.CreatedAt,
			ExpiresAt:    snap.ExpiresAt,
			Downloads:    snap.Downloads,
			MaxDownloads: snap.MaxDownloads,
			Bytes:        snap.BytesTransferred,
			LastAccess:   snap.LastAccess,
			Protected:    snap.Protected(),
			Tunnel:       s.tunnelKind,
		})
	}
	return infos
}

// Revoke implements control.Provider.
func (s *sharer) Revoke(id string) bool {
	if !s.registry.Remove(id) {
		return false
	}
	s.server.Forget(id)
	// This process exists to serve its shares; with none left there is
	// nothing to wait for.
	if s.registry.Len() == 0 {
		s.shutdown()
	}
	return true
}

// Stop implements control.Provider.
func (s *sharer) Stop() { s.shutdown() }

func (s *sharer) resolveTTL() (time.Duration, error) {
	if s.opts.ttl == "" {
		return time.Duration(s.app.config.TTL), nil
	}
	return config.ParseTTL(s.opts.ttl)
}

// resolvePassword obtains the password when --password is given.
func (s *sharer) resolvePassword() (string, error) {
	if !s.opts.password {
		return "", nil
	}
	password, err := readPassword(s.app.printer)
	if err != nil {
		return "", err
	}
	if password == "" {
		return "", errors.New("a password is required when --password is used")
	}
	return password, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
