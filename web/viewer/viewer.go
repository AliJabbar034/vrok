// Package viewer renders the HTML a visitor sees and serves the viewer's own
// static assets. Templates and CSS are embedded in the binary so a vrok share
// has no runtime dependency on the filesystem it was started from.
package viewer

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Page names.
const (
	pageFile     = "file.html"
	pageIndex    = "index.html"
	pagePassword = "password.html"
	pageGone     = "gone.html"
	pageUpload   = "upload.html"
)

// Meta is the chrome shared by every page. Each page struct embeds it, which
// is what lets layout.html render the header without knowing the page type.
type Meta struct {
	// Title is the browser tab title.
	Title string
	// StaticBase is the URL prefix the stylesheet is served from.
	StaticBase string
	// HomeURL points at the root of this share.
	HomeURL string
	// Expires is a pre-formatted countdown, or empty when there is no TTL.
	Expires string
	// Downloads is a pre-formatted allowance, or empty when unlimited.
	Downloads string
}

// Crumb is one breadcrumb link.
type Crumb struct {
	Name string
	URL  string
}

// FilePage presents a single file with its rendered preview.
type FilePage struct {
	Meta
	Name        string
	Icon        string
	SizeText    string
	DownloadURL string
	Breadcrumbs []Crumb
	// SHA256 is the file's fingerprint as hex, once it has been worked out.
	SHA256 string
	// ChecksumPending is true while the fingerprint is still being worked
	// out in the background.
	ChecksumPending bool
	// Preview is a trusted fragment produced by internal/preview.
	Preview template.HTML
}

// IndexEntry is one row of a listing.
type IndexEntry struct {
	Name        string
	URL         string
	DownloadURL string
	Icon        string
	SizeText    string
	Modified    string
	IsDir       bool
}

// IndexPage presents a directory or a multi-file share.
type IndexPage struct {
	Meta
	Heading     string
	Current     string
	Breadcrumbs []Crumb
	Entries     []IndexEntry
	// Total is how many entries the directory actually holds, and Truncated
	// reports that Entries is only the first part of it. A visitor is told
	// rather than left to assume the folder is smaller than it is.
	Total     int
	Truncated bool
	// ArchiveURL downloads everything listed, and everything under it, as
	// one zip. Empty when there is nothing to download.
	ArchiveURL string
}

// PasswordPage presents the unlock form.
type PasswordPage struct {
	Meta
	ActionURL string
	Error     string
}

// GonePage presents a terminal state: not found, expired or used up.
type GonePage struct {
	Meta
	Icon    string
	Heading string
	Message string
}

// UploadPage lets a visitor send files into a receive share.
type UploadPage struct {
	Meta
	// UploadURL is the base of the chunked upload API.
	UploadURL string
	// OfferURL is where the page asks the owner to accept a batch of files.
	OfferURL string
	// FilesLeft is how many more files the share accepts, or zero when
	// there is no limit.
	FilesLeft int
	// ScriptVersion is filled in by Upload.
	ScriptVersion string
}

// Renderer renders the viewer pages. It is safe for concurrent use: templates
// are parsed once at construction and never mutated afterwards.
type Renderer struct {
	pages map[string]*template.Template
	// uploadJS fingerprints upload.js. Static assets are cached at a URL
	// shared by every vrok version, and an old script cannot talk to a new
	// server, so its URL changes whenever its contents do.
	uploadJS string
}

// New parses the embedded templates and returns a Renderer. It fails only if
// the embedded assets are corrupt, which means a broken build rather than a
// runtime condition.
func New() (*Renderer, error) {
	names := []string{pageFile, pageIndex, pagePassword, pageGone, pageUpload}
	pages := make(map[string]*template.Template, len(names))

	for _, name := range names {
		// Each page is parsed together with the layout into its own template
		// set, because every page defines a block called "content".
		t, err := template.New("layout.html").ParseFS(templateFS,
			"templates/layout.html", "templates/"+name)
		if err != nil {
			return nil, fmt.Errorf("viewer: parse %s: %w", name, err)
		}
		pages[name] = t
	}
	script, err := staticFS.ReadFile("static/upload.js")
	if err != nil {
		return nil, fmt.Errorf("viewer: read upload.js: %w", err)
	}
	sum := sha256.Sum256(script)
	return &Renderer{pages: pages, uploadJS: hex.EncodeToString(sum[:6])}, nil
}

// File renders the single-file page.
func (r *Renderer) File(w http.ResponseWriter, status int, data FilePage) error {
	return r.render(w, status, pageFile, data)
}

// Index renders a directory or multi-file listing.
func (r *Renderer) Index(w http.ResponseWriter, status int, data IndexPage) error {
	return r.render(w, status, pageIndex, data)
}

// Password renders the unlock form.
func (r *Renderer) Password(w http.ResponseWriter, status int, data PasswordPage) error {
	return r.render(w, status, pagePassword, data)
}

// Gone renders a terminal state page.
func (r *Renderer) Gone(w http.ResponseWriter, status int, data GonePage) error {
	return r.render(w, status, pageGone, data)
}

// Upload renders the page that sends files into a receive share.
func (r *Renderer) Upload(w http.ResponseWriter, status int, data UploadPage) error {
	data.ScriptVersion = r.uploadJS
	return r.render(w, status, pageUpload, data)
}

// render executes a page into memory before touching the response, so a
// template failure can still be reported as a clean error instead of a
// half-written 200.
func (r *Renderer) render(w http.ResponseWriter, status int, page string, data any) error {
	t, ok := r.pages[page]
	if !ok {
		return fmt.Errorf("viewer: unknown page %q", page)
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return fmt.Errorf("viewer: render %s: %w", page, err)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, err := buf.WriteTo(w)
	return err
}

// StaticFS returns the embedded static assets rooted at the directory that
// holds them, ready to hand to http.FileServer.
func StaticFS() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// Unreachable: the path is a compile-time constant of the embed.
		panic(fmt.Sprintf("viewer: embedded static assets missing: %v", err))
	}
	return sub
}
