package preview

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/AliJabbar034/vrok/internal/humanize"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

// maxInlineBytes caps how much of a file the server will read in order to
// render it. Anything larger is offered as a download instead: a preview must
// never become a way to make vrok allocate an arbitrary amount of memory.
const maxInlineBytes = 2 << 20 // 2 MiB

// maxArchiveEntries caps how many archive members are listed, for the same
// reason: a zip bomb has thousands of entries and we only need an overview.
const maxArchiveEntries = 2000

// errTooLarge is returned by renderers that would have to read too much.
var errTooLarge = errors.New("preview: file too large to render inline")

var fragments = template.Must(template.New("fragments").Funcs(template.FuncMap{
	"bytes": humanize.Bytes,
}).Parse(`
{{define "image"}}
<figure class="stage stage--image">
  <img src="{{.RawURL}}" alt="{{.Name}}" loading="lazy">
</figure>
{{end}}

{{define "video"}}
<figure class="stage stage--video">
  <video src="{{.RawURL}}" controls preload="metadata" playsinline></video>
</figure>
{{end}}

{{define "audio"}}
<figure class="stage stage--audio">
  <audio src="{{.RawURL}}" controls preload="metadata"></audio>
</figure>
{{end}}

{{define "pdf"}}
<div class="stage stage--pdf">
  <object data="{{.RawURL}}" type="application/pdf">
    <p>Your browser cannot display this PDF inline.
       <a href="{{.DownloadURL}}">Download it instead</a>.</p>
  </object>
</div>
{{end}}

{{define "html"}}
<div class="stage stage--html">
  <div class="stage__note">Rendered in an isolated sandbox. Scripts are disabled.</div>
  <iframe src="{{.RawURL}}" sandbox referrerpolicy="no-referrer" title="{{.Name}}"></iframe>
</div>
{{end}}

{{define "download"}}
<div class="stage stage--download">
  <div class="card">
    <span class="card__icon">{{.Icon}}</span>
    <div class="card__meta">
      <span class="card__name">{{.Name}}</span>
      <span class="card__size">{{bytes .Size}}</span>
    </div>
    <a class="button" href="{{.DownloadURL}}" download>Download</a>
  </div>
</div>
{{end}}

{{define "text"}}
<div class="stage stage--text">
  {{if .Truncated}}<div class="stage__note">Showing the first {{bytes .Shown}} of {{bytes .Size}}.</div>{{end}}
  <pre class="code"><code>{{.Body}}</code></pre>
</div>
{{end}}

{{define "markdown"}}
<article class="stage stage--markdown markdown">{{.Body}}</article>
{{end}}

{{define "json"}}
<div class="stage stage--json">
  {{if .Invalid}}<div class="stage__note">Not valid JSON; showing raw text.</div>{{end}}
  <pre class="code code--json"><code>{{.Body}}</code></pre>
</div>
{{end}}

{{define "archive"}}
<div class="stage stage--archive">
  <div class="stage__note">{{.Count}} entries{{if .Truncated}} (first {{.Count}} shown){{end}} &middot; {{bytes .Size}} compressed</div>
  <table class="listing">
    <thead><tr><th>Name</th><th>Size</th><th>Modified</th></tr></thead>
    <tbody>
    {{range .Entries}}
      <tr>
        <td class="listing__name">{{if .Dir}}📁{{else}}📄{{end}} {{.Name}}</td>
        <td class="listing__size">{{if .Dir}}&mdash;{{else}}{{bytes .Size}}{{end}}</td>
        <td class="listing__time">{{.Modified}}</td>
      </tr>
    {{end}}
    </tbody>
  </table>
</div>
{{end}}
`))

func renderFragment(name string, data any) (template.HTML, error) {
	var buf bytes.Buffer
	if err := fragments.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

// ImageRenderer displays images inline.
type ImageRenderer struct{}

// Supports implements Renderer.
func (ImageRenderer) Supports(a Asset) bool { return a.Descriptor.Kind == KindImage }

// Render implements Renderer.
func (ImageRenderer) Render(a Asset) (template.HTML, error) { return renderFragment("image", a) }

// VideoRenderer embeds a seekable video player. Seeking works because the raw
// route answers Range requests.
type VideoRenderer struct{}

// Supports implements Renderer.
func (VideoRenderer) Supports(a Asset) bool { return a.Descriptor.Kind == KindVideo }

// Render implements Renderer.
func (VideoRenderer) Render(a Asset) (template.HTML, error) { return renderFragment("video", a) }

// AudioRenderer embeds an audio player.
type AudioRenderer struct{}

// Supports implements Renderer.
func (AudioRenderer) Supports(a Asset) bool { return a.Descriptor.Kind == KindAudio }

// Render implements Renderer.
func (AudioRenderer) Render(a Asset) (template.HTML, error) { return renderFragment("audio", a) }

// PDFRenderer hands the file to the browser's built-in PDF viewer.
type PDFRenderer struct{}

// Supports implements Renderer.
func (PDFRenderer) Supports(a Asset) bool { return a.Descriptor.Kind == KindPDF }

// Render implements Renderer.
func (PDFRenderer) Render(a Asset) (template.HTML, error) { return renderFragment("pdf", a) }

// HTMLRenderer shows HTML documents inside a fully sandboxed iframe, so a
// shared page cannot script against vrok's origin or reach other shares.
type HTMLRenderer struct{}

// Supports implements Renderer.
func (HTMLRenderer) Supports(a Asset) bool { return a.Descriptor.Kind == KindHTML }

// Render implements Renderer.
func (HTMLRenderer) Render(a Asset) (template.HTML, error) { return renderFragment("html", a) }

// DownloadRenderer is the fallback for anything vrok cannot display.
type DownloadRenderer struct{}

// Supports implements Renderer.
func (DownloadRenderer) Supports(Asset) bool { return true }

// Render implements Renderer.
func (DownloadRenderer) Render(a Asset) (template.HTML, error) {
	return renderFragment("download", struct {
		Name        string
		Icon        string
		Size        int64
		DownloadURL string
	}{a.Name, a.Descriptor.Icon, a.Size, a.DownloadURL})
}

// TextRenderer shows text files as escaped, preformatted content.
type TextRenderer struct{}

// Supports implements Renderer.
func (TextRenderer) Supports(a Asset) bool {
	return a.Descriptor.Kind == KindText && a.Size <= maxInlineBytes
}

// Render implements Renderer.
func (TextRenderer) Render(a Asset) (template.HTML, error) {
	body, truncated, err := readLimited(a)
	if err != nil {
		return "", err
	}
	return renderFragment("text", struct {
		Body      string
		Size      int64
		Shown     int64
		Truncated bool
	}{string(body), a.Size, int64(len(body)), truncated})
}

// MarkdownRenderer renders CommonMark plus GitHub tables and task lists.
//
// Raw HTML inside the document is discarded rather than passed through:
// a shared README is untrusted input as far as the viewer page is concerned.
type MarkdownRenderer struct{}

// Supports implements Renderer.
func (MarkdownRenderer) Supports(a Asset) bool {
	return a.Descriptor.Kind == KindMarkdown && a.Size <= maxInlineBytes
}

// Render implements Renderer.
func (MarkdownRenderer) Render(a Asset) (template.HTML, error) {
	source, _, err := readLimited(a)
	if err != nil {
		return "", err
	}

	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithRendererOptions(html.WithHardWraps()),
	)
	var out bytes.Buffer
	if err := md.Convert(source, &out); err != nil {
		return "", err
	}
	return renderFragment("markdown", struct{ Body template.HTML }{template.HTML(out.String())})
}

// JSONRenderer pretty-prints JSON and wraps its tokens in spans so the
// stylesheet can colour them. Highlighting happens server-side, which keeps
// the viewer free of third-party scripts and CDN dependencies.
type JSONRenderer struct{}

// Supports implements Renderer.
func (JSONRenderer) Supports(a Asset) bool {
	return a.Descriptor.Kind == KindJSON && a.Size <= maxInlineBytes
}

// Render implements Renderer.
func (JSONRenderer) Render(a Asset) (template.HTML, error) {
	source, _, err := readLimited(a)
	if err != nil {
		return "", err
	}

	var indented bytes.Buffer
	if err := json.Indent(&indented, source, "", "  "); err != nil {
		// Invalid JSON still deserves to be readable, so fall back to text.
		return renderFragment("json", struct {
			Body    template.HTML
			Invalid bool
		}{template.HTML(template.HTMLEscapeString(string(source))), true})
	}
	return renderFragment("json", struct {
		Body    template.HTML
		Invalid bool
	}{highlightJSON(indented.String()), false})
}

// ArchiveRenderer lists the contents of a zip file without extracting it.
type ArchiveRenderer struct{}

// Supports implements Renderer.
func (ArchiveRenderer) Supports(a Asset) bool {
	if a.Descriptor.Kind != KindArchive {
		return false
	}
	// Only zip is inspected: tar and friends would have to be streamed in
	// full, which defeats the point of a cheap listing.
	name := strings.ToLower(a.Name)
	return strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".jar")
}

type archiveEntry struct {
	Name     string
	Size     int64
	Dir      bool
	Modified string
}

// Render implements Renderer.
func (ArchiveRenderer) Render(a Asset) (template.HTML, error) {
	if a.Content == nil {
		return "", errors.New("preview: archive has no content opener")
	}
	f, err := a.Content.Open()
	if err != nil {
		return "", err
	}
	defer f.Close()

	zr, err := zip.NewReader(f, a.Size)
	if err != nil {
		return "", err
	}

	truncated := len(zr.File) > maxArchiveEntries
	members := zr.File
	if truncated {
		members = members[:maxArchiveEntries]
	}

	entries := make([]archiveEntry, 0, len(members))
	for _, m := range members {
		name := m.Name
		dir := strings.HasSuffix(name, "/")
		modified := ""
		if t := m.Modified; !t.IsZero() {
			modified = t.Local().Format(time.DateTime)
		}
		entries = append(entries, archiveEntry{
			Name:     strings.TrimSuffix(name, "/"),
			Size:     int64(m.UncompressedSize64),
			Dir:      dir,
			Modified: modified,
		})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

	return renderFragment("archive", struct {
		Entries   []archiveEntry
		Count     int
		Size      int64
		Truncated bool
	}{entries, len(entries), a.Size, truncated})
}

// readLimited reads at most maxInlineBytes of the asset and reports whether
// the file was cut short.
func readLimited(a Asset) ([]byte, bool, error) {
	if a.Content == nil {
		return nil, false, errors.New("preview: asset has no content opener")
	}
	if a.Size > maxInlineBytes {
		return nil, false, errTooLarge
	}
	f, err := a.Content.Open()
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	// Read one byte past the cap so truncation is detectable.
	body, err := io.ReadAll(io.LimitReader(f, maxInlineBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(body) > maxInlineBytes {
		return body[:maxInlineBytes], true, nil
	}
	return body, false, nil
}
