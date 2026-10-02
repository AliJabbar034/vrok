package server

import (
	"net/http"

	"github.com/AliJabbar034/vrok/internal/security"
	"github.com/AliJabbar034/vrok/internal/sharing"
)

// downloadCookieName names the cookie that marks a download as already
// counted. One name per share; each file gets its own cookie through the
// cookie's Path, so a session for one file is never sent with a request for
// another.
func downloadCookieName(shareID string) string { return cookiePrefix + "dl_" + shareID }

// downloadSessions ties a file's follow-up requests to the download that paid
// for them.
//
// A browser playing a video, or a download manager resuming a transfer, asks
// for the same file many times with different Range headers. Counting every
// one would spend --downloads before a single video finished. Not counting
// ranged requests at all would make the limit meaningless: "Range: bytes=-N"
// returns the whole file. So the request that is counted also receives a
// signed cookie scoped to that one file, and only requests presenting it are
// free. A client without it, whatever range it asks for, is a new download.
//
// The cookie is stateless and the same for every visitor of a given file. A
// visitor who passes it on could let others fetch the file uncounted, but
// that visitor already holds the file and could pass that on instead.
type downloadSessions struct {
	signer security.Signer
}

// key is the per-file identity a session is bound to. A single-file share
// answers at its root and under its own name, and both spellings are the
// same download.
func (d downloadSessions) key(spec sharing.Spec, rel string) string {
	if spec.Kind == sharing.KindFile {
		return ""
	}
	return rel
}

func (d downloadSessions) message(spec sharing.Spec, rel string) string {
	// Prefixed so a download proof can never be presented as an unlock proof,
	// which signs the bare token.
	return "download:" + spec.Token + ":" + d.key(spec, rel)
}

// Holds reports whether r continues a download of rel that was already
// counted.
func (d downloadSessions) Holds(r *http.Request, spec sharing.Spec, rel string) bool {
	message := d.message(spec, rel)
	for _, c := range r.CookiesNamed(downloadCookieName(spec.ID)) {
		if d.signer.Verify(message, c.Value) == nil {
			return true
		}
	}
	return false
}

// cookie mints the session for one file.
func (d downloadSessions) cookie(r *http.Request, sr *shareRequest, rel string) *http.Cookie {
	return &http.Cookie{
		Name:     downloadCookieName(sr.Spec.ID),
		Value:    d.signer.Sign(d.message(sr.Spec, rel)),
		Path:     sr.Links.Page(d.key(sr.Spec, rel)),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecure(r),
	}
}

// sessionWriter attaches the download session to a response that actually
// starts delivering the file. A 304, 412 or 416 delivers nothing, so it must
// not hand out a free pass for later ranged requests.
type sessionWriter struct {
	http.ResponseWriter
	cookie *http.Cookie
	wrote  bool
	issued bool
}

func (s *sessionWriter) WriteHeader(status int) {
	if !s.wrote {
		s.wrote = true
		if status == http.StatusOK || status == http.StatusPartialContent {
			http.SetCookie(s.ResponseWriter, s.cookie)
			s.issued = true
		}
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *sessionWriter) Write(b []byte) (int, error) {
	if !s.wrote {
		s.WriteHeader(http.StatusOK)
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap keeps http.ResponseController working through the wrapper.
func (s *sessionWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
