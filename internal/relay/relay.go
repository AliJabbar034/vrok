// Package relay is the server side of vrok's own tunnel.
//
// It is deliberately a router and nothing else: requests arriving for
// <label>.<domain> are forwarded over the WebSocket connection held open by
// the CLI that claimed that label, and the response is streamed straight back.
// No file is ever written to, or read by, the relay.
package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/AliJabbar034/vrok/internal/protocol"
	"github.com/AliJabbar034/vrok/internal/security"
	"github.com/gorilla/websocket"
)

// Options configures a Relay.
type Options struct {
	// Addr is the listen address, e.g. ":8787".
	Addr string
	// Domain is the zone shares appear under: a share with id "a82kd9"
	// becomes a82kd9.<Domain>. A wildcard DNS record must point at the relay.
	Domain string
	// Scheme is the scheme the relay is reachable on from the internet. It is
	// https in any real deployment, where TLS is terminated here or by the
	// load balancer in front.
	Scheme string
	// AuthToken, when set, is required from every agent. Leave it empty for a
	// relay that anyone may connect to.
	AuthToken string
	// MaxAgents caps concurrent tunnels; zero means unlimited.
	MaxAgents int
	// TLSCert and TLSKey are a certificate and key for the wildcard domain.
	// Set both to have the relay terminate TLS itself, which is one fewer
	// moving part than a reverse proxy in front of it. Leave both empty when
	// something upstream already terminates TLS.
	//
	// The certificate has to cover *.<Domain>, so it comes from a DNS-01
	// issuance the operator runs separately; the relay does not acquire it.
	TLSCert string
	TLSKey  string
	// Logger receives relay diagnostics.
	Logger *slog.Logger
}

// servesTLS reports whether the relay terminates TLS itself.
func (o Options) servesTLS() bool { return o.TLSCert != "" && o.TLSKey != "" }

// Relay accepts agent tunnels and routes visitor requests into them.
type Relay struct {
	opts     Options
	agents   *AgentRegistry
	upgrader websocket.Upgrader
	http     *http.Server
	logger   *slog.Logger
}

// New validates options and returns a Relay.
func New(opts Options) (*Relay, error) {
	if opts.Domain == "" {
		return nil, errors.New("relay: a domain is required")
	}
	if opts.Addr == "" {
		opts.Addr = ":8787"
	}
	if opts.Scheme == "" {
		opts.Scheme = "https"
	}
	// A half-configured pair is a deployment that would silently serve plain
	// HTTP on the port a browser will try to speak TLS to.
	if (opts.TLSCert == "") != (opts.TLSKey == "") {
		return nil, errors.New("relay: TLS needs both a certificate and a key")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}

	r := &Relay{
		opts:   opts,
		agents: NewAgentRegistry(opts.MaxAgents),
		logger: opts.Logger,
		upgrader: websocket.Upgrader{
			HandshakeTimeout: 15 * time.Second,
			ReadBufferSize:   protocol.MaxFrameData,
			WriteBufferSize:  protocol.MaxFrameData,
			// Agents are CLIs, not browsers, so there is no origin to check
			// and no cookie that could be abused cross-site.
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}

	r.http = &http.Server{
		Addr:              opts.Addr,
		Handler:           r.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: the relay streams large downloads and long-lived
		// tunnel connections, both of which a write deadline would sever.
		ErrorLog: slog.NewLogLogger(opts.Logger.Handler(), slog.LevelDebug),
	}
	return r, nil
}

// Handler returns the relay's HTTP handler.
func (r *Relay) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(protocol.AgentPath, r.serveAgent)
	// Liveness only. The connected agent count used to be reported here, but
	// /healthz is reachable by anyone, and on a public relay that number says
	// how many people are currently sharing. Operators get it from the logs.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/", r.serveVisitor)
	return mux
}

// ListenAndServe runs the relay until ctx is cancelled.
func (r *Relay) ListenAndServe(ctx context.Context) error {
	listener, err := net.Listen("tcp", r.opts.Addr)
	if err != nil {
		return fmt.Errorf("relay: listen on %s: %w", r.opts.Addr, err)
	}
	r.logger.Info("relay listening",
		slog.String("addr", listener.Addr().String()),
		slog.String("domain", r.opts.Domain),
		slog.Bool("tls", r.opts.servesTLS()))
	if r.opts.AuthToken == "" {
		// Anyone who can reach an open relay can publish content under its
		// domain. That is a legitimate choice, but never a silent one.
		r.logger.Warn("relay is open: any agent may register a hostname; set -token to restrict it")
	}

	errs := make(chan error, 1)
	go func() {
		err := r.serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		return r.Shutdown(context.WithoutCancel(ctx))
	}
}

// serve runs the HTTP server, terminating TLS when a certificate is
// configured. ServeTLS reads the files at startup, so a missing or unreadable
// certificate fails here rather than on a visitor's first request.
func (r *Relay) serve(listener net.Listener) error {
	if r.opts.servesTLS() {
		return r.http.ServeTLS(listener, r.opts.TLSCert, r.opts.TLSKey)
	}
	return r.http.Serve(listener)
}

// Shutdown stops the relay and disconnects every agent.
func (r *Relay) Shutdown(ctx context.Context) error {
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	r.agents.CloseAll(errors.New("relay: shutting down"))
	return r.http.Shutdown(shutdownCtx)
}

// serveAgent accepts a tunnel from a CLI.
func (r *Relay) serveAgent(w http.ResponseWriter, req *http.Request) {
	ws, err := r.upgrader.Upgrade(w, req, nil)
	if err != nil {
		// Upgrade has already written an error response.
		r.logger.Debug("agent upgrade failed", slog.String("error", err.Error()))
		return
	}

	conn := protocol.NewConn(ws)
	agent, err := r.register(conn)
	if err != nil {
		r.logger.Warn("agent registration refused", slog.String("error", err.Error()))
		conn.Send(protocol.TypeError, protocol.Error{Message: err.Error()})
		conn.Close()
		return
	}
	defer r.agents.Remove(agent)

	r.logger.Info("tunnel open",
		slog.String("agent", agent.ID),
		slog.Int("agents", r.agents.Len()))

	if err := agent.Serve(req.Context()); err != nil {
		r.logger.Debug("tunnel ended", slog.String("agent", agent.ID), slog.String("error", err.Error()))
	}
	r.logger.Info("tunnel closed", slog.String("agent", agent.ID))
}

// register performs the handshake: the agent's first message must be a valid
// registration, and the label it claims must be free.
func (r *Relay) register(conn *protocol.Conn) (*Agent, error) {
	envelope, frame, err := conn.Receive()
	if err != nil {
		return nil, fmt.Errorf("relay: read registration: %w", err)
	}
	if frame != nil || envelope.Type != protocol.TypeRegister {
		return nil, errors.New("relay: first message must be a registration")
	}

	msg, err := protocol.Payload[protocol.Register](*envelope)
	if err != nil {
		return nil, err
	}
	if msg.Version != protocol.Version {
		return nil, fmt.Errorf("relay: unsupported protocol version %d (relay speaks %d)", msg.Version, protocol.Version)
	}
	if r.opts.AuthToken != "" && !security.ConstantTimeEqual(msg.Auth, r.opts.AuthToken) {
		return nil, errors.New("relay: invalid relay token")
	}
	if !ValidLabel(msg.ShareID) {
		return nil, fmt.Errorf("relay: %q is not a usable hostname label", msg.ShareID)
	}

	agent := newAgent(msg.ShareID, conn, r.logger)
	if err := r.agents.Add(agent); err != nil {
		return nil, err
	}

	hostname := msg.ShareID + "." + r.opts.Domain
	confirmation := protocol.Registered{
		URL:      r.opts.Scheme + "://" + hostname,
		Hostname: hostname,
	}
	if err := conn.Send(protocol.TypeRegistered, confirmation); err != nil {
		r.agents.Remove(agent)
		return nil, err
	}
	return agent, nil
}

// serveVisitor routes a public request to the agent that owns its hostname.
func (r *Relay) serveVisitor(w http.ResponseWriter, req *http.Request) {
	label, ok := r.hostLabel(req.Host)
	if !ok {
		r.notFound(w, "No share here", "Open a share link to view a shared file.")
		return
	}

	agent, ok := r.agents.Get(label)
	if !ok {
		// An unknown or disconnected label is reported the same way, so a
		// visitor cannot learn which shares exist.
		r.notFound(w, "Share not available", "This share has been stopped or has expired.")
		return
	}

	if isUpgrade(req) {
		http.Error(w, "The vrok relay does not forward protocol upgrades yet.", http.StatusNotImplemented)
		return
	}

	agent.Forward(w, req)
}

// hostLabel extracts the share label from a request's Host header.
func (r *Relay) hostLabel(host string) (string, bool) {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))

	suffix := "." + strings.ToLower(r.opts.Domain)
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}
	label := strings.TrimSuffix(host, suffix)
	// Only a single label is accepted: nested labels are not shares, and
	// accepting them would let one share impersonate another's subdomain.
	if label == "" || strings.Contains(label, ".") {
		return "", false
	}
	return label, true
}

func (r *Relay) notFound(w http.ResponseWriter, heading, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	fmt.Fprintf(w, notFoundHTML, heading, message)
}

// notFoundHTML is self-contained: the relay serves no assets of its own, so
// this page has to carry its own styling.
const notFoundHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%[1]s</title>
<style>
  body{margin:0;min-height:100vh;display:grid;place-items:center;background:#0e1116;color:#e6edf3;
       font:15px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",Inter,sans-serif;text-align:center}
  h1{margin:0 0 8px;font-size:22px}
  p{margin:0;color:#8b98a5}
  .mark{color:#4c8dff;font-size:13px;letter-spacing:.3em;margin-bottom:18px}
  @media(prefers-color-scheme:light){body{background:#fff;color:#1f2328}p{color:#636c76}}
</style></head>
<body><main><div class="mark">VROK</div><h1>%[1]s</h1><p>%[2]s</p></main></body></html>
`
