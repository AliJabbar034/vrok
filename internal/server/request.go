package server

import (
	"net/http"
	"time"

	"github.com/AliJabbar034/vrok/internal/sharing"
)

// delivery selects what a file route returns.
type delivery uint8

const (
	// deliverPage returns the HTML preview page.
	deliverPage delivery = iota
	// deliverInline streams the bytes with Content-Disposition: inline.
	deliverInline
	// deliverAttachment streams the bytes as a download.
	deliverAttachment
)

// shareRequest is everything a share handler needs: the share, where inside it
// the visitor is pointing, and how they want it delivered.
//
// It is built once by the router and passed down, so no handler has to re-parse
// the URL or re-validate the token.
type shareRequest struct {
	Share *sharing.Share
	Spec  sharing.Spec
	Snap  sharing.Snapshot
	// Rel is the cleaned, slash-separated path inside the share. It is "" for
	// the share root and is guaranteed not to escape it.
	Rel      string
	Delivery delivery
	Links    Links
	Now      time.Time
}

// parseDelivery reads the ?raw / ?dl flags.
func parseDelivery(r *http.Request) delivery {
	q := r.URL.Query()
	switch {
	case q.Has(downloadParam):
		return deliverAttachment
	case q.Has(rawParam):
		return deliverInline
	default:
		return deliverPage
	}
}

// ShareHandler serves one kind of share. The router dispatches on
// sharing.Kind, so support for a new kind means adding an implementation here
// rather than extending a switch inside an existing handler.
type ShareHandler interface {
	ServeShare(w http.ResponseWriter, r *http.Request, sr *shareRequest)
}
