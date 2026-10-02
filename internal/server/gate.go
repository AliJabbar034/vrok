package server

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/AliJabbar034/vrok/internal/security"
	"github.com/AliJabbar034/vrok/internal/sharing"
	"github.com/AliJabbar034/vrok/web/viewer"
)

const (
	// cookiePrefix namespaces the unlock cookie per share, so unlocking one
	// share never grants access to another.
	cookiePrefix = "vrok_"

	// maxFormBytes caps an unlock submission. A password field needs a few
	// dozen bytes; anything larger is an attempt to make the server allocate.
	maxFormBytes = 4 << 10

	// failureDelay slows password guessing. It is not a substitute for a
	// strong password, but combined with the unguessable share token it makes
	// online brute force pointless.
	failureDelay = 400 * time.Millisecond

	// maxConcurrentVerifies bounds password checks in flight. One Argon2id
	// verification allocates 64 MiB, so without a bound anyone holding the
	// URL could POST a few hundred guesses at once and push the sharer's
	// machine into swap. Excess attempts queue, which also slows guessing.
	maxConcurrentVerifies = 2
)

// Gate enforces the optional share password.
//
// A successful unlock is remembered with a signed cookie rather than by
// storing the password client-side. The signing key is generated per process,
// so cookies die with the share — which is the same lifetime promise the URL
// itself makes.
type Gate struct {
	hasher security.Hasher
	signer security.Signer
	pages  *pages
	logger *slog.Logger
	// verifySlots is a semaphore of maxConcurrentVerifies tokens.
	verifySlots chan struct{}
}

// NewGate returns a Gate.
func NewGate(hasher security.Hasher, signer security.Signer, p *pages, logger *slog.Logger) *Gate {
	return &Gate{
		hasher:      hasher,
		signer:      signer,
		pages:       p,
		logger:      logger,
		verifySlots: make(chan struct{}, maxConcurrentVerifies),
	}
}

// Allow reports whether the request may proceed to the share content.
//
// When it returns false it has already written the response: the unlock form,
// or a redirect back to the requested page after a successful unlock.
func (g *Gate) Allow(w http.ResponseWriter, r *http.Request, sr *shareRequest) bool {
	if !sr.Spec.Protected() {
		return true
	}
	if g.authenticated(r, sr) {
		return true
	}

	if r.Method == http.MethodPost {
		g.attempt(w, r, sr)
		return false
	}

	// A visitor who is not authenticated must not reach the content, and must
	// not learn anything about it either: the gate page shows no filename.
	g.form(w, r, sr, "", http.StatusUnauthorized)
	return false
}

// authenticated checks the signed unlock cookie.
func (g *Gate) authenticated(r *http.Request, sr *shareRequest) bool {
	cookie, err := r.Cookie(cookieName(sr.Spec.ID))
	if err != nil {
		return false
	}
	// The cookie proves knowledge of the password for this specific share: it
	// signs the share token, so it cannot be replayed against another share.
	return g.signer.Verify(sr.Spec.Token, cookie.Value) == nil
}

// attempt verifies a submitted password.
func (g *Gate) attempt(w http.ResponseWriter, r *http.Request, sr *shareRequest) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		g.form(w, r, sr, "That submission could not be read.", http.StatusBadRequest)
		return
	}

	password := r.PostFormValue("password")
	if password == "" {
		g.form(w, r, sr, "Enter the password.", http.StatusUnauthorized)
		return
	}

	if err := g.verify(r, password, sr.Spec.PasswordHash); err != nil {
		if ctxErr := r.Context().Err(); ctxErr != nil && errors.Is(err, ctxErr) {
			return // the visitor gave up while queued; there is no one to answer
		}
		if !errors.Is(err, security.ErrPasswordMismatch) {
			g.pages.serverError(w, r, err)
			return
		}
		g.logger.Debug("failed unlock attempt", slog.String("share", sr.Spec.ID))
		time.Sleep(failureDelay)
		g.form(w, r, sr, "Incorrect password.", http.StatusUnauthorized)
		return
	}

	http.SetCookie(w, g.cookie(r, sr))
	// 303 turns the POST into a GET so a refresh does not resubmit the
	// password, and lands the visitor on the page they originally asked for.
	http.Redirect(w, r, sr.Links.Page(sr.Rel), http.StatusSeeOther)
}

// verify checks a password while holding one of the verification slots.
func (g *Gate) verify(r *http.Request, password, encoded string) error {
	select {
	case g.verifySlots <- struct{}{}:
	case <-r.Context().Done():
		return r.Context().Err()
	}
	defer func() { <-g.verifySlots }()
	return g.hasher.Verify(password, encoded)
}

// cookie mints the unlock proof.
func (g *Gate) cookie(r *http.Request, sr *shareRequest) *http.Cookie {
	// Scoped to this share's path. An HTTP share is the exception: the app
	// behind it may load "/assets/app.js", which the router attributes to the
	// share by Referer, and a cookie limited to /s/<token>/ would never reach
	// it. Widening the path does not widen access: the name carries the share
	// id, the value signs this share's token, and the proxy strips vrok's
	// cookies before anything reaches the upstream app.
	path := sr.Links.Root()
	if sr.Spec.Kind == sharing.KindHTTP {
		path = "/"
	}
	return &http.Cookie{
		Name:     cookieName(sr.Spec.ID),
		Value:    g.signer.Sign(sr.Spec.Token),
		Path:     path,
		HttpOnly: true,
		// Lax keeps the cookie on a normal link click while blocking
		// cross-site POSTs back to the share.
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecure(r),
		// Session cookie: closing the browser forgets the unlock.
	}
}

func (g *Gate) form(w http.ResponseWriter, r *http.Request, sr *shareRequest, message string, status int) {
	noStore(w)
	// WWW-Authenticate is deliberately omitted: a browser basic-auth dialog
	// would bypass the styled page and cannot be logged out of.
	data := viewer.PasswordPage{
		Meta: viewer.Meta{
			Title:      "Password required · vrok",
			StaticBase: sr.Links.Static(),
			HomeURL:    sr.Links.Root(),
		},
		ActionURL: sr.Links.Page(sr.Rel),
		Error:     message,
	}
	if err := g.pages.render.Password(w, status, data); err != nil {
		g.pages.serverError(w, r, err)
	}
}

func cookieName(shareID string) string { return cookiePrefix + shareID }

// isSecure reports whether the visitor's connection is HTTPS. Behind a tunnel
// the TLS termination happens upstream, so the forwarded header is the only
// evidence available.
func isSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return r.Header.Get("X-Forwarded-Proto") == "https"
}
