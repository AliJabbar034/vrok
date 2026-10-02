package protocol

import (
	"errors"
	"io"
	"sync"
)

// bodyBufferChunks is how many body frames a stream may hold before the
// connection's read loop blocks. Buffering absorbs network jitter so one slow
// peer does not stall every other stream sharing the tunnel; the bound is what
// stops a slow peer from consuming memory without limit.
const bodyBufferChunks = 32

// ErrStreamClosed reports a write to a stream nobody will read.
var ErrStreamClosed = errors.New("protocol: stream closed")

// BodyStream is the receiving half of one body stream: a bounded queue of
// frames exposed as an io.Reader.
//
// Both ends of the tunnel need it — the relay to assemble response bodies, the
// agent to assemble request bodies — so it lives with the protocol rather than
// being written twice.
//
// An io.Pipe would also work but has no buffer at all, so the shared
// connection read loop would have to hand over every chunk synchronously and
// would stall on each one.
type BodyStream struct {
	chunks chan []byte
	// done is closed by Close. The chunk channel itself is never closed, so a
	// racing Push can never panic on a closed channel.
	done chan struct{}
	once sync.Once

	mu       sync.Mutex
	closeErr error

	// current is the partially consumed chunk, owned by the single consumer
	// calling Read.
	current []byte
}

// NewBodyStream returns an empty body stream.
func NewBodyStream() *BodyStream {
	return &BodyStream{
		chunks: make(chan []byte, bodyBufferChunks),
		done:   make(chan struct{}),
	}
}

// Push appends a frame's payload. The data is copied because the caller's
// buffer is reused by the next read from the connection.
func (b *BodyStream) Push(data []byte) error {
	// Checked first and on its own: with a non-empty buffer both arms of the
	// select below would be ready, and Go would pick between them at random.
	select {
	case <-b.done:
		return ErrStreamClosed
	default:
	}

	chunk := make([]byte, len(data))
	copy(chunk, data)

	select {
	case b.chunks <- chunk:
		return nil
	case <-b.done:
		return ErrStreamClosed
	}
}

// Close ends the stream. A nil error means a normal end of body; any other
// error surfaces from Read, so the consumer can tell a truncated transfer from
// a complete one.
func (b *BodyStream) Close(err error) {
	b.once.Do(func() {
		b.mu.Lock()
		b.closeErr = err
		b.mu.Unlock()
		close(b.done)
	})
}

// Read implements io.Reader.
func (b *BodyStream) Read(p []byte) (int, error) {
	for len(b.current) == 0 {
		select {
		case chunk := <-b.chunks:
			b.current = chunk
		case <-b.done:
			// Closing does not discard what already arrived: drain the queue
			// before reporting the end, or a complete response could be
			// truncated by its own end-of-stream frame.
			select {
			case chunk := <-b.chunks:
				b.current = chunk
			default:
				return 0, b.endError()
			}
		}
	}

	n := copy(p, b.current)
	b.current = b.current[n:]
	return n, nil
}

func (b *BodyStream) endError() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closeErr != nil {
		return b.closeErr
	}
	return io.EOF
}
