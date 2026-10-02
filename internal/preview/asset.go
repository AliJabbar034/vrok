package preview

import (
	"io"
	"time"
)

// File is the subset of *os.File a renderer needs. ReaderAt is required
// because archive formats are read out of order.
type File interface {
	io.Reader
	io.ReaderAt
	io.Seeker
	io.Closer
}

// Opener lazily provides the bytes of an asset. Renderers that only emit a URL
// (images, video) never call it, so large media is never read by the server.
type Opener interface {
	Open() (File, error)
}

// OpenerFunc adapts a function to Opener.
type OpenerFunc func() (File, error)

// Open implements Opener.
func (f OpenerFunc) Open() (File, error) { return f() }

// Asset is a single previewable item: its metadata plus the URLs through which
// a browser can fetch the real bytes.
type Asset struct {
	// Name is the visitor-facing filename.
	Name string
	// Descriptor is the detected presentation category.
	Descriptor Descriptor
	// Size is the file size in bytes.
	Size int64
	// ModTime is used for cache validators and display.
	ModTime time.Time
	// RawURL serves the bytes inline, with Range support.
	RawURL string
	// DownloadURL serves the bytes as an attachment.
	DownloadURL string
	// Content opens the bytes for server-side rendering.
	Content Opener
}
