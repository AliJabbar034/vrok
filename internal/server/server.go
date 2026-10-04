// Package server is vrok's HTTP layer: it maps share URLs onto local files,
// directories and upstream applications, and enforces expiry, passwords and
// download limits on every request.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/AliJabbar034/vrok/internal/checksum"
	"github.com/AliJabbar034/vrok/internal/preview"
	"github.com/AliJabbar034/vrok/internal/security"
	"github.com/AliJabbar034/vrok/internal/sharing"
	"github.com/AliJabbar034/vrok/web/viewer"
)

// Options configures a Server. Every dependency is an interface so the server
// can be built against test doubles, and so none of its collaborators need to
// know they are being used by an HTTP server.
type Options struct {
	// Addr is the listen address, e.g. "127.0.0.1:0" to pick a free port.
	Addr string
	// Resolver maps URL tokens to shares.
	Resolver sharing.Resolver
	// Guards decide whether a share is still available. Defaults to
	// sharing.DefaultGuards().
	Guards sharing.Guard
	// Hasher verifies share passwords.
	Hasher security.Hasher
	// Signer signs unlock cookies.
	Signer security.Signer
	// Clock supplies the current time. Defaults to the system clock.
	Clock sharing.Clock
	// Detector classifies files for previews.
	Detector preview.Detector
	// Previews renders file previews.
	Previews *preview.Registry
	// Viewer renders HTML pages.
	Viewer *viewer.Renderer
	// Logger receives request and error logs.
	Logger *slog.Logger
	// Checksums works out the SHA-256 shown on file pages. Defaults to a
	// new cache.
	Checksums *checksum.Cache
}

// Server owns the listener and the HTTP handler for a set of shares.
type Server struct {
	http     *http.Server
	handler  http.Handler
	listener net.Listener
	addr     string
	roots    *rootCache
	sums     *checksum.Cache
	logger   *slog.Logger
}

// New validates the options and assembles the handler graph.
func New(opts Options) (*Server, error) {
	if opts.Resolver == nil {
		return nil, errors.New("server: a share resolver is required")
	}
	if opts.Guards == nil {
		opts.Guards = sharing.DefaultGuards()
	}
	if opts.Clock == nil {
		opts.Clock = sharing.SystemClock{}
	}
	if opts.Detector == nil {
		opts.Detector = preview.NewDetector()
	}
	if opts.Previews == nil {
		opts.Previews = preview.NewRegistry()
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.Hasher == nil {
		opts.Hasher = security.NewArgon2Hasher(security.DefaultArgon2Params())
	}
	if opts.Signer == nil {
		key, err := security.NewSecret()
		if err != nil {
			return nil, err
		}
		opts.Signer = security.NewHMACSigner(key)
	}
	if opts.Checksums == nil {
		opts.Checksums = checksum.New()
	}
	if opts.Viewer == nil {
		rendered, err := viewer.New()
		if err != nil {
			return nil, err
		}
		opts.Viewer = rendered
	}

	pageRenderer := &pages{render: opts.Viewer, logger: opts.Logger}
	downloads := downloadSessions{signer: opts.Signer}
	assets := &assetServer{
		detector:  opts.Detector,
		previews:  opts.Previews,
		pages:     pageRenderer,
		downloads: downloads,
		clock:     opts.Clock,
		checksums: opts.Checksums,
	}
	roots := newRootCache()

	d := &dispatcher{
		resolver:  opts.Resolver,
		guards:    opts.Guards,
		downloads: downloads,
		clock:     opts.Clock,
		gate:      NewGate(opts.Hasher, opts.Signer, pageRenderer, opts.Logger),
		pages:     pageRenderer,
		logger:    opts.Logger,
		handlers: map[sharing.Kind]ShareHandler{
			sharing.KindFile:      SingleFileHandler{assets},
			sharing.KindFiles:     FileSetHandler{assets},
			sharing.KindDirectory: DirectoryHandler{assetServer: assets, roots: roots},
			sharing.KindHTTP:      NewProxyHandler(pageRenderer, opts.Logger),
		},
	}

	handler := Chain(newRouter(d),
		recoverer(opts.Logger, pageRenderer.broken),
		accessLogger(opts.Logger),
	)

	addr := opts.Addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}

	return &Server{
		handler: handler,
		http: &http.Server{
			Addr:    addr,
			Handler: handler,
			// A slow-loris client must not hold a connection open forever,
			// but there is deliberately no WriteTimeout: a legitimate
			// download of a large file can take hours, and a write deadline
			// would cut it off mid-transfer.
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
			ErrorLog:          slog.NewLogLogger(opts.Logger.Handler(), slog.LevelDebug),
		},
		addr:   addr,
		roots:  roots,
		sums:   opts.Checksums,
		logger: opts.Logger,
	}, nil
}

// Handler returns the request handler, so the share routes can be mounted
// elsewhere or exercised directly in tests without binding a port.
func (s *Server) Handler() http.Handler { return s.handler }

// Listen binds the socket without serving yet.
//
// Binding is separate from serving so the caller can learn the real port
// before any URL is printed or any tunnel is started — which is what makes
// "listen on :0" usable.
func (s *Server) Listen() error {
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("server: listen on %s: %w", s.addr, err)
	}
	s.listener = listener
	s.addr = listener.Addr().String()
	return nil
}

// Addr returns the bound address, valid after Listen.
func (s *Server) Addr() string { return s.addr }

// Serve accepts connections until the server is shut down. It calls Listen
// first if the caller has not already done so.
func (s *Server) Serve() error {
	if s.listener == nil {
		if err := s.Listen(); err != nil {
			return err
		}
	}
	if err := s.http.Serve(s.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown stops accepting connections and waits for in-flight requests, up to
// the deadline carried by ctx.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.http.Shutdown(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		// Downloads in progress are not a reason to hang on exit: the share
		// is being stopped, so cutting the connection is the correct outcome.
		return s.http.Close()
	}
	return err
}

// Warm starts working out the checksums of a share's files in the
// background, so a single large file has its fingerprint ready by the time a
// visitor opens the page. Directory shares are hashed per file on first view
// instead: hashing a whole tree up front could read gigabytes nobody asks for.
func (s *Server) Warm(spec sharing.Spec) {
	if spec.Kind != sharing.KindFile && spec.Kind != sharing.KindFiles {
		return
	}
	for _, e := range spec.Entries {
		if info, err := os.Stat(e.Path); err == nil && info.Mode().IsRegular() {
			s.sums.Lookup(e.Path, "", info)
		}
	}
}

// Forget releases cached per-share state. Call it when a share is revoked.
func (s *Server) Forget(shareID string) { s.roots.Forget(shareID) }
