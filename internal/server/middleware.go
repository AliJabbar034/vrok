package server

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// Middleware decorates a handler. Keeping cross-cutting concerns in this shape
// lets the router compose them in one readable place.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware so that the first argument is the outermost layer.
func Chain(h http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// recoverer converts a panic in any handler into a 500 instead of killing the
// process. A share server is often the only thing standing between a demo and
// an audience; one bad file must not take it down.
//
// The visitor gets the styled error page from failed, but only if nothing has
// been sent yet. Once a response has started, appending a page would splice
// HTML into the middle of a file, so the connection is cut instead and the
// browser reports an interrupted download honestly.
func recoverer(logger *slog.Logger, failed func(http.ResponseWriter)) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &recorder{ResponseWriter: w, status: http.StatusOK}
			defer func() {
				if v := recover(); v != nil {
					// ErrAbortHandler is how net/http signals a deliberate
					// abort (a client that vanished mid-stream); it is noise.
					if v == http.ErrAbortHandler {
						panic(v)
					}
					logger.Error("panic serving request",
						slog.String("path", r.URL.Path),
						slog.Any("panic", v),
						slog.String("stack", string(debug.Stack())))
					if rec.started {
						panic(http.ErrAbortHandler)
					}
					failed(w)
				}
			}()
			next.ServeHTTP(rec, r)
		})
	}
}

// accessLogger records one line per request at debug level. vrok is a
// foreground tool, so request logs are off unless the user asks for them.
func accessLogger(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &recorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			logger.Debug("request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int64("bytes", rec.written),
				slog.Duration("took", time.Since(start)))
		})
	}
}

// recorder observes the status code and payload size of a response.
//
// It forwards Unwrap so that http.ResponseController can still reach the
// underlying writer for flushing and connection hijacking — without this, the
// reverse proxy could not stream or upgrade WebSocket connections.
type recorder struct {
	http.ResponseWriter
	status  int
	written int64
	started bool // a status line has gone out; the response can't change
}

func (r *recorder) WriteHeader(status int) {
	// 1xx responses are interim; the real status is still to come.
	if status >= 200 {
		r.started = true
	}
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.started = true
	n, err := r.ResponseWriter.Write(b)
	r.written += int64(n)
	return n, err
}

// Unwrap exposes the wrapped writer to http.ResponseController.
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
