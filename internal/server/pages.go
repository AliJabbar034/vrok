package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/AliJabbar034/vrok/internal/humanize"
	"github.com/AliJabbar034/vrok/internal/sharing"
	"github.com/AliJabbar034/vrok/web/viewer"
)

// pages turns domain state into rendered HTML. Handlers go through it so that
// every page gets the same header, the same cache rules and the same error
// handling.
type pages struct {
	render *viewer.Renderer
	logger *slog.Logger
}

// meta builds the page chrome from a share snapshot.
func (p *pages) meta(sr *shareRequest, title string) viewer.Meta {
	m := viewer.Meta{
		Title:      title,
		StaticBase: sr.Links.Static(),
		HomeURL:    sr.Links.Root(),
	}
	if left, limited := sharing.TimeLeft(sr.Snap, sr.Now); limited {
		m.Expires = humanize.Duration(left)
	}
	if remaining, limited := sharing.RemainingDownloads(sr.Snap); limited {
		m.Downloads = fmt.Sprintf("%d of %d left", remaining, sr.Snap.MaxDownloads)
	}
	return m
}

// crumbs converts server breadcrumbs into viewer breadcrumbs.
func crumbs(in []Crumb) []viewer.Crumb {
	out := make([]viewer.Crumb, len(in))
	for i, c := range in {
		out[i] = viewer.Crumb{Name: c.Name, URL: c.URL}
	}
	return out
}

// noStore marks a response as uncacheable. Share pages reflect live state
// (countdown, remaining downloads) and must not be served from a cache after
// the share is gone.
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

// gone renders a terminal state. Every reason a share cannot be served —
// wrong token, expired, revoked, limit reached — produces a 404 with the same
// shape, so a visitor cannot probe for which tokens exist.
func (p *pages) gone(w http.ResponseWriter, r *http.Request, reason error) {
	heading, message, icon := "Not found", "This link is not valid.", "🔍"
	switch {
	case errors.Is(reason, sharing.ErrExpired):
		heading, message, icon = "Share expired", "This link has passed its expiry time.", "⌛"
	case errors.Is(reason, sharing.ErrDownloadLimit):
		heading, message, icon = "Share expired", "This link reached its download limit.", "⛔"
	case errors.Is(reason, sharing.ErrRevoked):
		heading, message, icon = "Share stopped", "The owner stopped sharing this.", "🚫"
	}

	noStore(w)
	data := viewer.GonePage{
		Meta:    viewer.Meta{Title: heading + " · vrok", StaticBase: staticPrefix},
		Icon:    icon,
		Heading: heading,
		Message: message,
	}
	if err := p.render.Gone(w, http.StatusNotFound, data); err != nil {
		p.fail(w)
	}
}

// rejected answers a request whose path does not name anything the share
// exposes, whatever the underlying reason: a typo, an escape attempt, a
// symlink loop, an unreadable file.
//
// The visitor always gets the same 404 that an unknown token gets. Reporting
// an escape differently from a missing file would tell a prober which paths
// exist outside the share, which is the disclosure the confinement is there
// to prevent. The reason is logged at debug level, because probes are
// expected traffic and should not fill an operator's terminal.
func (p *pages) rejected(w http.ResponseWriter, r *http.Request, err error) {
	p.logger.Debug("path rejected",
		slog.String("path", r.URL.Path),
		slog.String("error", err.Error()))
	p.gone(w, r, nil)
}

// serverError reports an internal failure without leaking its detail to the
// visitor; the operator sees it in the terminal instead.
func (p *pages) serverError(w http.ResponseWriter, r *http.Request, err error) {
	p.logger.Error("request failed",
		slog.String("path", r.URL.Path),
		slog.String("error", err.Error()))
	p.fail(w)
}

// fail is the last resort, used when even rendering a page failed.
func (p *pages) fail(w http.ResponseWriter) {
	http.Error(w, "Something went wrong.", http.StatusInternalServerError)
}
