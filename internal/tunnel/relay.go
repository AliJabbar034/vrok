package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/AliJabbar034/vrok/internal/protocol"
	"github.com/gorilla/websocket"
)

func init() { Register("relay", newRelay) }

// ErrNoRelayConfigured is returned when the relay provider is selected
// without an address to connect to.
//
// There is deliberately no default. vrok operates no public relay, and
// defaulting to a hostname nobody runs turns a clear configuration error into
// a DNS failure that tells the user nothing about what to do next.
var ErrNoRelayConfigured = errors.New(
	`tunnel: --tunnel relay needs a relay to connect to, and vrok runs no public one.

Either point it at your own relay:
    vrok <path> --tunnel relay --relay-url https://relay.example.com
    vrok config set relay-url https://relay.example.com

Or let auto open a public URL (the default):
    vrok <path>
    vrok <path> --tunnel cloudflare

See docs/architecture.md for how to run a relay of your own.`)

// RelayTunnel is the agent half of vrok's own tunnel.
//
// It holds one WebSocket connection to a relay and serves the HTTP requests
// that arrive over it against the local share server. Nothing is uploaded: a
// visitor's download is read from disk and streamed through this connection
// while they wait.
type RelayTunnel struct {
	endpoint string
	shareID  string
	token    string
	auth     string
	logger   *slog.Logger

	// client talks to vrok's own local server, so it needs no proxy, no
	// redirect handling and no response buffering.
	client *http.Client

	mu      sync.Mutex
	conn    *protocol.Conn
	cancel  context.CancelFunc
	target  *url.URL
	streams map[uint64]*protocol.BodyStream
	stopped chan struct{}
}

func newRelay(cfg Config) (Tunnel, error) {
	if cfg.RelayURL == "" {
		return nil, ErrNoRelayConfigured
	}
	wsURL, err := agentEndpoint(cfg.RelayURL)
	if err != nil {
		return nil, err
	}
	if cfg.Label == "" {
		return nil, errors.New("tunnel: the relay provider needs a share id to claim a hostname")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	return &RelayTunnel{
		endpoint: wsURL,
		shareID:  cfg.Label,
		token:    cfg.ShareToken,
		auth:     cfg.Auth,
		logger:   logger.With(slog.String("tunnel", "relay")),
		client: &http.Client{
			// Redirects must reach the visitor's browser unchanged, so the
			// agent never follows them itself.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Transport: &http.Transport{
				DisableCompression: true,
				MaxIdleConns:       32,
			},
		},
		streams: make(map[uint64]*protocol.BodyStream),
		stopped: make(chan struct{}),
	}, nil
}

// Name implements Tunnel.
func (t *RelayTunnel) Name() string { return "relay" }

// Start opens the tunnel and returns the public URL the relay assigned.
func (t *RelayTunnel) Start(ctx context.Context, target string) (PublicURL, error) {
	parsed, err := url.Parse(target)
	if err != nil {
		return "", fmt.Errorf("tunnel: parse target %q: %w", target, err)
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 20 * time.Second,
		ReadBufferSize:   protocol.MaxFrameData,
		WriteBufferSize:  protocol.MaxFrameData,
	}
	ws, resp, err := dialer.DialContext(ctx, t.endpoint, nil)
	if err != nil {
		if resp != nil {
			return "", fmt.Errorf("tunnel: relay refused the connection (%s): %w", resp.Status, err)
		}
		return "", fmt.Errorf("tunnel: connect to relay %s: %w", t.endpoint, err)
	}

	conn := protocol.NewConn(ws)
	registration := protocol.Register{
		Version: protocol.Version,
		ShareID: t.shareID,
		Token:   t.token,
		Auth:    t.auth,
		Agent:   "vrok",
	}
	if err := conn.Send(protocol.TypeRegister, registration); err != nil {
		conn.Close()
		return "", err
	}

	confirmation, err := awaitRegistration(conn)
	if err != nil {
		conn.Close()
		return "", err
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	t.mu.Lock()
	t.conn, t.cancel, t.target = conn, cancel, parsed
	t.mu.Unlock()

	go t.run(runCtx)
	return PublicURL(confirmation.URL), nil
}

// awaitRegistration reads the relay's answer to a registration.
func awaitRegistration(conn *protocol.Conn) (protocol.Registered, error) {
	envelope, _, err := conn.Receive()
	if err != nil {
		return protocol.Registered{}, fmt.Errorf("tunnel: relay did not answer: %w", err)
	}
	if envelope == nil {
		return protocol.Registered{}, errors.New("tunnel: relay sent a body frame before registering")
	}

	switch envelope.Type {
	case protocol.TypeRegistered:
		return protocol.Payload[protocol.Registered](*envelope)
	case protocol.TypeError:
		msg, err := protocol.Payload[protocol.Error](*envelope)
		if err != nil {
			return protocol.Registered{}, err
		}
		return protocol.Registered{}, fmt.Errorf("tunnel: relay refused the share: %s", msg.Message)
	default:
		return protocol.Registered{}, fmt.Errorf("tunnel: unexpected relay message %q", envelope.Type)
	}
}

// run is the agent's read loop.
func (t *RelayTunnel) run(ctx context.Context) {
	defer close(t.stopped)

	conn := t.connection()
	if conn == nil {
		return
	}
	go conn.Keepalive(ctx)

	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	for {
		envelope, frame, err := conn.Receive()
		if err != nil {
			if !protocol.IsExpectedClose(err) && ctx.Err() == nil {
				t.logger.Warn("tunnel connection lost", slog.String("error", err.Error()))
			}
			t.failStreams(err)
			return
		}

		switch {
		case frame != nil:
			t.onFrame(*frame)
		case envelope.Type == protocol.TypeRequest:
			msg, err := protocol.Payload[protocol.Request](*envelope)
			if err != nil {
				t.logger.Warn("bad request message", slog.String("error", err.Error()))
				continue
			}
			// The body frames follow the request on this same loop, so the
			// stream that collects them is registered here, before they can
			// arrive and be dropped as belonging to no stream.
			var body *protocol.BodyStream
			if msg.HasBody {
				body = protocol.NewBodyStream()
				t.addStream(msg.Stream, body)
			}
			// One goroutine per request: a large download must not stop the
			// loop from reading the next request or a cancellation.
			go t.serve(ctx, msg, body)
		case envelope.Type == protocol.TypeCancel:
			msg, err := protocol.Payload[protocol.Cancel](*envelope)
			if err == nil {
				t.closeStream(msg.Stream, errors.New("tunnel: cancelled by relay"))
			}
		case envelope.Type == protocol.TypeError:
			msg, _ := protocol.Payload[protocol.Error](*envelope)
			t.logger.Warn("relay reported an error", slog.String("message", msg.Message))
		}
	}
}

// serve answers one proxied request from the local share server.
func (t *RelayTunnel) serve(ctx context.Context, msg protocol.Request, stream *protocol.BodyStream) {
	var body io.Reader
	if stream != nil {
		defer t.closeStream(msg.Stream, nil)
		body = stream
	}

	conn := t.connection()
	if conn == nil {
		return
	}

	t.mu.Lock()
	target := t.target
	t.mu.Unlock()

	local, err := localURL(target, msg.URI)
	if err != nil {
		t.reportStreamError(conn, msg.Stream, err)
		return
	}
	request, err := http.NewRequestWithContext(ctx, msg.Method, local, body)
	if err != nil {
		t.reportStreamError(conn, msg.Stream, err)
		return
	}
	for name, values := range msg.Header {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	// Host is a field, not a header, in Go's client.
	if host := msg.Header["X-Forwarded-Host"]; len(host) > 0 {
		request.Host = host[0]
	}

	response, err := t.client.Do(request)
	if err != nil {
		t.reportStreamError(conn, msg.Stream, err)
		return
	}
	defer response.Body.Close()

	header := make(map[string][]string, len(response.Header))
	for name, values := range response.Header {
		header[name] = values
	}
	reply := protocol.Response{Stream: msg.Stream, Status: response.StatusCode, Header: header}
	if err := conn.Send(protocol.TypeResponse, reply); err != nil {
		return
	}

	if err := conn.Copy(msg.Stream, response.Body); err != nil && !protocol.IsExpectedClose(err) {
		t.logger.Debug("response stream ended early", slog.String("error", err.Error()))
	}
}

// localURL places a relay-supplied request URI onto the local share server.
//
// The URI comes off the network, so it is parsed rather than concatenated:
// joined as a string, "@169.254.169.254/" would turn the local origin into
// userinfo and send the agent to a host of the relay's choosing. Only
// origin-form paths are accepted, and only the path and query are taken from
// them; the scheme and host are always the local server's.
func localURL(target *url.URL, uri string) (string, error) {
	if !strings.HasPrefix(uri, "/") || strings.HasPrefix(uri, "//") {
		return "", fmt.Errorf("tunnel: refusing request URI %q: not an origin-form path", uri)
	}
	parsed, err := url.ParseRequestURI(uri)
	if err != nil {
		return "", fmt.Errorf("tunnel: refusing request URI %q: %w", uri, err)
	}
	local := *target
	local.Path, local.RawPath, local.RawQuery = parsed.Path, parsed.RawPath, parsed.RawQuery
	local.User, local.Fragment = nil, ""
	return local.String(), nil
}

func (t *RelayTunnel) reportStreamError(conn *protocol.Conn, stream uint64, err error) {
	t.logger.Debug("local request failed", slog.String("error", err.Error()))
	conn.Send(protocol.TypeError, protocol.Error{
		Stream:  stream,
		Code:    "local_request_failed",
		Message: "the shared server did not answer",
	})
}

func (t *RelayTunnel) onFrame(f protocol.Frame) {
	t.mu.Lock()
	stream := t.streams[f.Stream]
	t.mu.Unlock()
	if stream == nil {
		return
	}
	if len(f.Data) > 0 {
		stream.Push(f.Data)
	}
	if f.End {
		stream.Close(nil)
	}
}

func (t *RelayTunnel) addStream(id uint64, s *protocol.BodyStream) {
	t.mu.Lock()
	t.streams[id] = s
	t.mu.Unlock()
}

func (t *RelayTunnel) closeStream(id uint64, err error) {
	t.mu.Lock()
	stream := t.streams[id]
	delete(t.streams, id)
	t.mu.Unlock()
	if stream != nil {
		stream.Close(err)
	}
}

func (t *RelayTunnel) failStreams(err error) {
	t.mu.Lock()
	streams := make([]*protocol.BodyStream, 0, len(t.streams))
	for id, s := range t.streams {
		streams = append(streams, s)
		delete(t.streams, id)
	}
	t.mu.Unlock()

	for _, s := range streams {
		s.Close(err)
	}
}

func (t *RelayTunnel) connection() *protocol.Conn {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.conn
}

// Stop closes the tunnel.
func (t *RelayTunnel) Stop(ctx context.Context) error {
	t.mu.Lock()
	cancel, conn := t.cancel, t.conn
	t.cancel, t.conn = nil, nil
	t.mu.Unlock()

	if cancel == nil {
		return nil
	}
	cancel()
	if conn != nil {
		conn.Close()
	}

	select {
	case <-t.stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return errors.New("tunnel: relay connection did not close")
	}
}

// agentEndpoint turns a relay base URL into its WebSocket agent endpoint.
func agentEndpoint(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("tunnel: parse relay URL %q: %w", base, err)
	}
	switch u.Scheme {
	case "https", "wss":
		u.Scheme = "wss"
	case "http", "ws":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("tunnel: relay URL %q must be http(s) or ws(s)", base)
	}
	if u.Host == "" {
		return "", fmt.Errorf("tunnel: relay URL %q has no host", base)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + protocol.AgentPath
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}
