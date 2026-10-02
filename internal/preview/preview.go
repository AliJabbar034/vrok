package preview

import (
	"html/template"
)

// Renderer produces the HTML fragment that presents one asset.
//
// Each media category is its own Renderer: adding a new preview type means
// adding a type here and registering it, never editing an existing renderer or
// a switch statement in the HTTP layer.
type Renderer interface {
	// Supports reports whether this renderer can present the asset.
	Supports(a Asset) bool
	// Render returns a trusted HTML fragment. Implementations must escape
	// every value that originates from the filesystem or a visitor.
	Render(a Asset) (template.HTML, error)
}

// Registry picks a renderer for an asset. It is immutable once built, so it
// can be shared across requests without locking.
type Registry struct {
	renderers []Renderer
	fallback  Renderer
}

// NewRegistry returns the default set of renderers, ordered from most to least
// specific. The download card is the fallback and always succeeds.
func NewRegistry() *Registry {
	return &Registry{
		renderers: []Renderer{
			ImageRenderer{},
			VideoRenderer{},
			AudioRenderer{},
			PDFRenderer{},
			MarkdownRenderer{},
			JSONRenderer{},
			ArchiveRenderer{},
			HTMLRenderer{},
			TextRenderer{},
		},
		fallback: DownloadRenderer{},
	}
}

// With returns a copy of the registry with extra renderers taking priority.
// Callers can extend previews without modifying this package.
func (r *Registry) With(extra ...Renderer) *Registry {
	merged := make([]Renderer, 0, len(extra)+len(r.renderers))
	merged = append(merged, extra...)
	merged = append(merged, r.renderers...)
	return &Registry{renderers: merged, fallback: r.fallback}
}

// Render presents the asset with the first renderer that supports it.
//
// A renderer failure is never fatal: a truncated archive or unreadable text
// file degrades to the download card rather than a 500, because the visitor's
// goal is the bytes, not the preview.
func (r *Registry) Render(a Asset) template.HTML {
	for _, renderer := range r.renderers {
		if !renderer.Supports(a) {
			continue
		}
		if html, err := renderer.Render(a); err == nil {
			return html
		}
		break
	}
	html, err := r.fallback.Render(a)
	if err != nil {
		return template.HTML("")
	}
	return html
}
