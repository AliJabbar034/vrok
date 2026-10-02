package server

import (
	"net/url"
	"strings"
)

// Route prefixes owned by vrok itself. They live outside /s/ so they can never
// collide with a share token.
const (
	sharePrefix  = "/s/"
	staticPrefix = "/_vrok/static"
)

// Query flags that select how a file route responds. A flag, rather than a
// path segment, keeps the URL of a file identical whether you are looking at
// its preview or fetching its bytes — which matters for directory shares where
// the rest of the path belongs to the user's own files.
const (
	rawParam      = "raw"
	downloadParam = "dl"
)

// SharePath returns the path a share is served under, for callers that need
// to build an absolute URL from a public origin.
func SharePath(token string) string { return NewLinks(token).Root() }

// Links builds every URL a share page needs. Centralising this means path
// escaping is done once, correctly, instead of at each call site.
type Links struct {
	base string // "/s/<token>", never with a trailing slash
}

// NewLinks returns a link builder for a share token.
func NewLinks(token string) Links {
	return Links{base: sharePrefix + url.PathEscape(token)}
}

// Root is the share's landing URL.
func (l Links) Root() string { return l.base + "/" }

// Page returns the viewer page for a path relative to the share root.
func (l Links) Page(rel string) string { return l.join(rel, "") }

// Raw returns the URL that streams the bytes inline, with Range support.
func (l Links) Raw(rel string) string { return l.join(rel, rawParam) }

// Download returns the URL that streams the bytes as an attachment.
func (l Links) Download(rel string) string { return l.join(rel, downloadParam) }

// Static is the URL prefix for the viewer's own assets.
func (l Links) Static() string { return staticPrefix }

func (l Links) join(rel, flag string) string {
	u := url.URL{Path: l.base + "/" + strings.TrimPrefix(rel, "/")}
	if flag != "" {
		u.RawQuery = flag + "=1"
	}
	return u.String()
}

// Breadcrumbs splits a relative path into cumulative links, excluding the
// final element, which the page renders as plain text.
func (l Links) Breadcrumbs(root, rel string) []Crumb {
	crumbs := []Crumb{{Name: root, URL: l.Root()}}
	parts := splitPath(rel)
	for i := 0; i < len(parts)-1; i++ {
		crumbs = append(crumbs, Crumb{
			Name: parts[i],
			URL:  l.Page(strings.Join(parts[:i+1], "/") + "/"),
		})
	}
	return crumbs
}

// Crumb is one breadcrumb entry. It mirrors viewer.Crumb so the server can
// build navigation without the viewer package leaking into every handler.
type Crumb struct {
	Name string
	URL  string
}

func splitPath(rel string) []string {
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return nil
	}
	return strings.Split(rel, "/")
}
