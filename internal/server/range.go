package server

import (
	"net/http"
	"strconv"

	"github.com/AliJabbar034/vrok/internal/sharing"
)

// vrok does not implement Range itself: http.ServeContent already handles
// Range, If-Range, If-Modified-Since, If-None-Match, 206 responses, multipart
// ranges and 416 errors correctly. Re-implementing that is how video seeking
// and resumable downloads get subtly broken.
//
// Whether a ranged request counts against --downloads is decided by
// downloadSessions, not by the Range header: a header is whatever the client
// chose to send, so it cannot be what decides whether the client pays.

// countingWriter tallies the payload bytes written through it so a share can
// report how much traffic it has served. With a transfer attached, each write
// is reported as it happens, which is what drives live progress.
type countingWriter struct {
	http.ResponseWriter
	n        int64
	transfer *sharing.Transfer
}

func (c *countingWriter) WriteHeader(status int) {
	if c.transfer != nil {
		if n, err := strconv.ParseInt(c.Header().Get("Content-Length"), 10, 64); err == nil {
			c.transfer.SetTotal(n)
		}
	}
	c.ResponseWriter.WriteHeader(status)
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	c.n += int64(n)
	if c.transfer != nil {
		c.transfer.Add(int64(n))
	}
	return n, err
}

// Unwrap keeps http.ResponseController working through the wrapper.
func (c *countingWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }
