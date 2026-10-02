package preview_test

import (
	"html/template"

	"github.com/AliJabbar034/vrok/internal/preview"
)

// stubRenderer proves that preview support can be extended from outside the
// package, which is the point of the Renderer interface.
type stubRenderer struct{}

func (stubRenderer) Supports(preview.Asset) bool { return true }

func (stubRenderer) Render(preview.Asset) (template.HTML, error) {
	return template.HTML("<custom>"), nil
}
