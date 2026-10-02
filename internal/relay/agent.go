package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/AliJabbar034/vrok/internal/protocol"
)

// headerTimeout is how long a visitor's request waits for the agent to return
// a status line. The agent is only reading a local file or proxying localhost,
// so anything slower than this means it is wedged.
const headerTimeout = 30 * time.Second

// Agent is one connected CLI: a WebSocket connection plus the set of HTTP
// streams currently in flight over it.
type Agent struct {
	// ID is the hostname label this agent claimed.
	ID string

	conn   *protocol.Conn
	logger *slog.Logger

	mu      sync.Mutex
	streams map[uint64]*stream
	nextID  uint64
	closed  bool

	// done is closed when the agent's read loop exits, so in-flight requests
	// stop waiting the moment the tunnel drops.
	done chan struct{}
}

// stream is one proxied HTTP exchange.
type stream struct {
	id uint64
	// response receives the status and headers exactly once.
	response chan protocol.Response
	// body carries the response payload.
	body *protocol.BodyStream
}

func newAgent(id string, conn *protocol.Conn, logger *slog.Logger) *Agent {
	return &Agent{
		ID:      id,
		conn:    conn,
		logger:  logger.With(slog.String("agent", id)),
		streams: make(map[uint64]*stream),
		done:    make(chan struct{}),
	}
}

// Serve runs the agent's read loop until the connection fails or ctx is
// cancelled. It returns when the agent is finished and should be unregistered.
func (a *Agent) Serve(ctx context.Context) error {
	go a.conn.Keepalive(ctx)

	// A cancelled context must interrupt the blocking read, which only
	// closing the connection can do.
	stop := context.AfterFunc(ctx, func() { a.Close(errors.New("relay: shutting down")) })
	defer stop()

	defer close(a.done)

	for {
		envelope, frame, err := a.conn.Receive()
		if err != nil {
			a.failAll(fmt.Errorf("relay: tunnel closed: %w", err))
			if protocol.IsExpectedClose(err) {
				return nil
			}
			return err
		}

		switch {
		case frame != nil:
			a.onFrame(*frame)
		case envelope != nil:
			if err := a.onControl(*envelope); err != nil {
				a.logger.Warn("bad control message", slog.String("error", err.Error()))
			}
		}
	}
}

func (a *Agent) onControl(e protocol.Envelope) error {
	switch e.Type {
	case protocol.TypeResponse:
		msg, err := protocol.Payload[protocol.Response](e)
		if err != nil {
			return err
		}
		if s := a.stream(msg.Stream); s != nil {
			// Buffered channel of one: the agent sends exactly one response
			// per stream, so this never blocks the read loop.
			select {
			case s.response <- msg:
			default:
			}
		}
		return nil

	case protocol.TypeError:
		msg, err := protocol.Payload[protocol.Error](e)
		if err != nil {
			return err
		}
		if msg.Stream == 0 {
			a.failAll(fmt.Errorf("relay: agent error: %s", msg.Message))
			return nil
		}
		if s := a.stream(msg.Stream); s != nil {
			s.body.Close(fmt.Errorf("relay: agent error: %s", msg.Message))
		}
		return nil

	case protocol.TypeCancel:
		msg, err := protocol.Payload[protocol.Cancel](e)
		if err != nil {
			return err
		}
		if s := a.stream(msg.Stream); s != nil {
			s.body.Close(errors.New("relay: cancelled by agent"))
		}
		return nil

	default:
		return fmt.Errorf("relay: unexpected message type %q", e.Type)
	}
}

func (a *Agent) onFrame(f protocol.Frame) {
	s := a.stream(f.Stream)
	if s == nil {
		// A frame for a stream the visitor already abandoned: dropping it is
		// correct, and the agent will see the cancel we already sent.
		return
	}
	if len(f.Data) > 0 {
		if err := s.body.Push(f.Data); err != nil {
			return
		}
	}
	if f.End {
		s.body.Close(nil)
	}
}

// Forward proxies one visitor request over the tunnel.
func (a *Agent) Forward(w http.ResponseWriter, r *http.Request) {
	s, err := a.open()
	if err != nil {
		http.Error(w, "Share is not connected.", http.StatusBadGateway)
		return
	}
	defer a.release(s)

	hasBody := r.Body != nil && r.ContentLength != 0
	request := protocol.Request{
		Stream:     s.id,
		Method:     r.Method,
		URI:        r.URL.RequestURI(),
		Header:     forwardedHeader(r),
		RemoteAddr: r.RemoteAddr,
		HasBody:    hasBody,
	}
	if err := a.conn.Send(protocol.TypeRequest, request); err != nil {
		http.Error(w, "Share is not connected.", http.StatusBadGateway)
		return
	}

	if hasBody {
		// Upload in the background so the response can start flowing before
		// the request body has finished arriving.
		go func() {
			if err := a.conn.Copy(s.id, r.Body); err != nil {
				a.logger.Debug("request body upload failed", slog.String("error", err.Error()))
			}
		}()
	}

	response, ok := a.awaitResponse(r.Context(), s)
	if !ok {
		return
	}

	copyHeader(w.Header(), response.Header)
	w.WriteHeader(response.Status)
	a.streamBodyTo(w, r, s)
}

// awaitResponse blocks until the agent returns headers, the visitor goes away
// or the tunnel dies. It writes the error response itself when no reply comes.
func (a *Agent) awaitResponse(ctx context.Context, s *stream) (protocol.Response, bool) {
	select {
	case response := <-s.response:
		return response, true

	case <-ctx.Done():
		// The visitor hung up: tell the agent to stop reading the file.
		a.cancel(s.id, "visitor disconnected")
		return protocol.Response{}, false

	case <-a.done:
		return protocol.Response{}, false

	case <-time.After(headerTimeout):
		a.cancel(s.id, "timed out")
		return protocol.Response{}, false
	}
}

// streamBodyTo copies the response payload to the visitor, flushing as it
// goes so that streamed responses are not held in a buffer.
func (a *Agent) streamBodyTo(w http.ResponseWriter, r *http.Request, s *stream) {
	controller := http.NewResponseController(w)
	buf := make([]byte, protocol.MaxFrameData)

	for {
		n, err := s.body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				a.cancel(s.id, "visitor disconnected")
				return
			}
			// Ignoring the flush error is deliberate: not every writer
			// supports flushing, and a response that cannot be flushed is
			// still delivered when the handler returns.
			_ = controller.Flush()
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				a.logger.Debug("response truncated", slog.String("error", err.Error()))
			}
			return
		}
		if r.Context().Err() != nil {
			a.cancel(s.id, "visitor disconnected")
			return
		}
	}
}

// open allocates a stream.
func (a *Agent) open() (*stream, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errors.New("relay: agent disconnected")
	}
	a.nextID++
	s := &stream{
		id:       a.nextID,
		response: make(chan protocol.Response, 1),
		body:     protocol.NewBodyStream(),
	}
	a.streams[s.id] = s
	return s, nil
}

func (a *Agent) release(s *stream) {
	a.mu.Lock()
	delete(a.streams, s.id)
	a.mu.Unlock()
	s.body.Close(nil)
}

func (a *Agent) stream(id uint64) *stream {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.streams[id]
}

func (a *Agent) cancel(id uint64, reason string) {
	if err := a.conn.Send(protocol.TypeCancel, protocol.Cancel{Stream: id, Reason: reason}); err != nil {
		a.logger.Debug("cancel not delivered", slog.String("error", err.Error()))
	}
}

// failAll ends every in-flight stream, which unblocks the handlers waiting on
// them instead of leaving visitors hanging.
func (a *Agent) failAll(err error) {
	a.mu.Lock()
	a.closed = true
	streams := make([]*stream, 0, len(a.streams))
	for _, s := range a.streams {
		streams = append(streams, s)
	}
	a.mu.Unlock()

	for _, s := range streams {
		s.body.Close(err)
	}
}

// Close disconnects the agent.
func (a *Agent) Close(err error) {
	a.failAll(err)
	a.conn.Close()
}
