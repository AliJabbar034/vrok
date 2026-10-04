package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AliJabbar034/vrok/web/viewer"
)

func testPages(t *testing.T) *pages {
	t.Helper()
	render, err := viewer.New()
	if err != nil {
		t.Fatal(err)
	}
	return &pages{render: render, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestPanicBeforeResponseRendersStyledPage(t *testing.T) {
	p := testPages(t)
	h := recoverer(p.logger, p.broken)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// A file handler that described its response and then failed.
		w.Header().Set("Content-Disposition", `attachment; filename="cut.mp4"`)
		w.Header().Set("Content-Length", "1048576")
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/s/x/cut.mp4", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want the HTML error page", ct)
	}
	// Left in place, these would save the error page as cut.mp4 or truncate it.
	for _, h := range []string{"Content-Disposition", "Content-Length"} {
		if v := rec.Header().Get(h); v != "" {
			t.Errorf("%s = %q survived onto the error page", h, v)
		}
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Something went wrong") || !strings.Contains(body, "viewer.css") {
		t.Errorf("body is not the styled error page:\n%s", body)
	}
	if strings.Contains(body, "boom") {
		t.Error("the panic value leaked to the visitor")
	}
}

func TestPanicMidResponseAbortsInsteadOfSplicingAPage(t *testing.T) {
	p := testPages(t)
	h := recoverer(p.logger, p.broken)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("first bytes of a file"))
		panic("boom")
	}))

	defer func() {
		if v := recover(); v != http.ErrAbortHandler {
			t.Fatalf("recovered %v, want http.ErrAbortHandler so the connection is cut", v)
		}
	}()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/s/x/cut.mp4", nil))
	t.Fatal("handler returned normally after a mid-response panic")
}
