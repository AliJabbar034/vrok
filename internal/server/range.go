package server

import (
	"net/http"
	"strconv"
	"strings"
)

// vrok does not implement Range itself: http.ServeContent already handles
// Range, If-Range, If-Modified-Since, If-None-Match, 206 responses, multipart
// ranges and 416 errors correctly. Re-implementing that is how video seeking
// and resumable downloads get subtly broken.
//
// What this file does is interpret the Range header for one purpose only:
// deciding whether a request counts against the download allowance.

// isInitialFetch reports whether the request asks for the beginning of the
// content, i.e. whether it is the start of a new transfer rather than the
// continuation of one.
//
// This is what makes --downloads meaningful for media: a browser seeking
// through a video issues many ranged requests, and only the first one — the
// one that starts at byte zero — is counted as a download.
func isInitialFetch(r *http.Request) bool {
	// HEAD only asks for metadata, so it never consumes an allowance.
	if r.Method == http.MethodHead {
		return false
	}

	spec := r.Header.Get("Range")
	if spec == "" {
		return true
	}

	start, ok := firstRangeStart(spec)
	if !ok {
		// Unparseable or suffix-relative ("bytes=-500"): let ServeContent
		// decide what to do with it, and do not count it.
		return false
	}
	return start == 0
}

// firstRangeStart extracts the first byte position of the first range in a
// Range header. It reports false for suffix ranges and malformed input.
func firstRangeStart(spec string) (int64, bool) {
	const prefix = "bytes="
	if !strings.HasPrefix(spec, prefix) {
		return 0, false
	}
	first, _, _ := strings.Cut(strings.TrimPrefix(spec, prefix), ",")
	startText, _, found := strings.Cut(strings.TrimSpace(first), "-")
	if !found || startText == "" {
		return 0, false
	}
	start, err := strconv.ParseInt(startText, 10, 64)
	if err != nil || start < 0 {
		return 0, false
	}
	return start, true
}

// countingWriter tallies the payload bytes written through it so a share can
// report how much traffic it has served.
type countingWriter struct {
	http.ResponseWriter
	n int64
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	c.n += int64(n)
	return n, err
}

// Unwrap keeps http.ResponseController working through the wrapper.
func (c *countingWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }
