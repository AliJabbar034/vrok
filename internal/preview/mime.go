// Package preview decides how a shared file should be presented in a browser:
// as an image, a video, rendered markdown, a syntax-highlighted document, an
// archive listing, or a plain download.
package preview

import (
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

// Kind is a presentation category. It is coarser than a MIME type on purpose:
// everything vrok can preview maps onto exactly one renderer.
type Kind string

// The presentation categories vrok understands.
const (
	KindImage    Kind = "image"
	KindVideo    Kind = "video"
	KindAudio    Kind = "audio"
	KindPDF      Kind = "pdf"
	KindText     Kind = "text"
	KindJSON     Kind = "json"
	KindMarkdown Kind = "markdown"
	KindHTML     Kind = "html"
	KindArchive  Kind = "archive"
	KindBinary   Kind = "binary"
)

// Descriptor is the outcome of inspecting a filename: which renderer to use,
// what Content-Type to send and which glyph to show in listings.
type Descriptor struct {
	Kind        Kind
	ContentType string
	Icon        string
}

// Detector classifies files. The viewer depends on this interface so that
// classification can be stubbed or extended without touching the handlers.
type Detector interface {
	Detect(name string) Descriptor
}

// byExtension is the authoritative table. Extensions are matched before the
// system MIME database so vrok behaves identically on every machine, whatever
// is registered in /etc/mime.types.
var byExtension = map[string]Descriptor{
	".jpg":  {KindImage, "image/jpeg", "🖼"},
	".jpeg": {KindImage, "image/jpeg", "🖼"},
	".png":  {KindImage, "image/png", "🖼"},
	".gif":  {KindImage, "image/gif", "🖼"},
	".webp": {KindImage, "image/webp", "🖼"},
	".avif": {KindImage, "image/avif", "🖼"},
	".bmp":  {KindImage, "image/bmp", "🖼"},
	".ico":  {KindImage, "image/x-icon", "🖼"},
	".svg":  {KindImage, "image/svg+xml", "🖼"},

	".mp4":  {KindVideo, "video/mp4", "🎬"},
	".m4v":  {KindVideo, "video/mp4", "🎬"},
	".mov":  {KindVideo, "video/quicktime", "🎬"},
	".webm": {KindVideo, "video/webm", "🎬"},
	".mkv":  {KindVideo, "video/x-matroska", "🎬"},
	".avi":  {KindVideo, "video/x-msvideo", "🎬"},

	".mp3":  {KindAudio, "audio/mpeg", "🎵"},
	".m4a":  {KindAudio, "audio/mp4", "🎵"},
	".wav":  {KindAudio, "audio/wav", "🎵"},
	".ogg":  {KindAudio, "audio/ogg", "🎵"},
	".flac": {KindAudio, "audio/flac", "🎵"},

	".pdf": {KindPDF, "application/pdf", "📕"},

	".md":       {KindMarkdown, "text/markdown; charset=utf-8", "📝"},
	".markdown": {KindMarkdown, "text/markdown; charset=utf-8", "📝"},

	".json":  {KindJSON, "application/json", "🧩"},
	".jsonl": {KindJSON, "application/x-ndjson", "🧩"},

	".html": {KindHTML, "text/html; charset=utf-8", "🌐"},
	".htm":  {KindHTML, "text/html; charset=utf-8", "🌐"},

	".zip": {KindArchive, "application/zip", "🗜"},
	".jar": {KindArchive, "application/java-archive", "🗜"},
	".tar": {KindArchive, "application/x-tar", "📦"},
	".gz":  {KindArchive, "application/gzip", "📦"},
	".tgz": {KindArchive, "application/gzip", "📦"},
	".bz2": {KindArchive, "application/x-bzip2", "📦"},
	".xz":  {KindArchive, "application/x-xz", "📦"},
	".7z":  {KindArchive, "application/x-7z-compressed", "📦"},
	".rar": {KindArchive, "application/vnd.rar", "📦"},
	".iso": {KindArchive, "application/x-iso9660-image", "💿"},
	".dmg": {KindArchive, "application/x-apple-diskimage", "💿"},
}

// textExtensions are rendered as plain text with the correct charset. They are
// listed separately because they all share one Descriptor shape.
var textExtensions = map[string]string{
	".txt": "text/plain", ".log": "text/plain", ".csv": "text/csv",
	".tsv": "text/tab-separated-values", ".yaml": "text/yaml", ".yml": "text/yaml",
	".toml": "text/plain", ".ini": "text/plain", ".conf": "text/plain",
	".env": "text/plain", ".go": "text/plain", ".rs": "text/plain",
	".py": "text/plain", ".js": "text/javascript", ".mjs": "text/javascript",
	".ts": "text/plain", ".tsx": "text/plain", ".jsx": "text/plain",
	".c": "text/plain", ".h": "text/plain", ".cc": "text/plain",
	".cpp": "text/plain", ".hpp": "text/plain", ".java": "text/plain",
	".kt": "text/plain", ".rb": "text/plain", ".php": "text/plain",
	".sh": "text/plain", ".bash": "text/plain", ".zsh": "text/plain",
	".fish": "text/plain", ".sql": "text/plain", ".css": "text/css",
	".scss": "text/plain", ".xml": "text/xml", ".svelte": "text/plain",
	".vue": "text/plain", ".lua": "text/plain", ".pl": "text/plain",
	".swift": "text/plain", ".dart": "text/plain", ".diff": "text/plain",
	".patch": "text/plain",

	// Conventional extensionless filenames, reached through the "." + basename
	// lookup in Detect. They are plainly text, and a project shared as a folder
	// is full of them.
	".dockerfile": "text/plain", ".containerfile": "text/plain",
	".makefile": "text/plain", ".justfile": "text/plain",
	".readme": "text/plain", ".changelog": "text/plain",
	".license": "text/plain", ".licence": "text/plain",
	".copying": "text/plain", ".notice": "text/plain",
	".authors": "text/plain", ".contributors": "text/plain",
	".install": "text/plain", ".news": "text/plain",
	".todo": "text/plain", ".version": "text/plain",
	".gitignore": "text/plain", ".dockerignore": "text/plain",
	".gitattributes": "text/plain", ".editorconfig": "text/plain",
	".npmrc": "text/plain", ".nvmrc": "text/plain",
}

// ExtensionDetector classifies by file extension, falling back to the system
// MIME database and finally to an opaque binary download.
type ExtensionDetector struct{}

// NewDetector returns the default Detector.
func NewDetector() ExtensionDetector { return ExtensionDetector{} }

// Detect implements Detector.
func (ExtensionDetector) Detect(name string) Descriptor {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		// Files like "Dockerfile" or "Makefile" have no extension but are
		// plainly text; match on the whole name instead.
		ext = "." + strings.ToLower(filepath.Base(name))
	}

	if d, ok := byExtension[ext]; ok {
		return d
	}
	if ct, ok := textExtensions[ext]; ok {
		return Descriptor{Kind: KindText, ContentType: ct + "; charset=utf-8", Icon: "📄"}
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return Descriptor{Kind: kindFromContentType(ct), ContentType: ct, Icon: "📄"}
	}
	return Descriptor{Kind: KindBinary, ContentType: "application/octet-stream", Icon: "📄"}
}

func kindFromContentType(ct string) Kind {
	base, _, err := mime.ParseMediaType(ct)
	if err != nil {
		base = ct
	}
	switch {
	case strings.HasPrefix(base, "image/"):
		return KindImage
	case strings.HasPrefix(base, "video/"):
		return KindVideo
	case strings.HasPrefix(base, "audio/"):
		return KindAudio
	case strings.HasPrefix(base, "text/"):
		return KindText
	default:
		return KindBinary
	}
}

// SniffContentType inspects the first bytes of a file when the extension says
// nothing useful. It is only consulted for binary files so that a misleading
// sniff can never upgrade an unknown file into something a browser will run.
func SniffContentType(peek []byte) string {
	return http.DetectContentType(peek)
}

// Inline reports whether the browser should try to display this kind rather
// than save it.
//
// HTML is inline because serving a static site or a test report is a primary
// use case, and those artefacts need their own scripts and stylesheets to
// work. Archives and unknown binaries are always attachments: nothing good
// happens when a browser tries to render a 4 GB disk image.
func (d Descriptor) Inline() bool {
	switch d.Kind {
	case KindArchive, KindBinary:
		return false
	default:
		return true
	}
}
