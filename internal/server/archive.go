package server

import (
	"archive/zip"
	"compress/flate"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/AliJabbar034/vrok/internal/security"
	"github.com/AliJabbar034/vrok/internal/sharing"
)

// member is one entry of a streamed archive.
type member struct {
	// Name is the slash-separated path inside the zip. Directories end in "/".
	Name string
	// Path and Root are read through security.OpenShared, exactly as a
	// download of the same file would be.
	Path string
	Root string
	// Modified dates a directory entry; a file's date comes from the file.
	Modified time.Time
}

// memberSource walks the files of an archive, calling add for each. It stops
// at the first error add returns.
type memberSource func(add func(member) error) error

// streamZip sends members as one zip, built while it is sent. Nothing is
// staged on disk and memory stays flat however large the folder is, which is
// the point: a visitor gets a whole folder in one download.
//
// The size is not known up front, so the response has no Content-Length and
// cannot be resumed. Interrupted, it is simply downloaded again.
func (a *assetServer) streamZip(w http.ResponseWriter, r *http.Request, sr *shareRequest, filename string, walk memberSource) {
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	h.Set("X-Content-Type-Options", "nosniff")
	noStore(w)

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	// One archive is one download, claimed before a byte is read, for the
	// same reason a single file is: --downloads stays a hard cap.
	if _, err := sr.Share.ClaimDownload(sr.Now); err != nil {
		h.Del("Content-Disposition")
		a.pages.gone(w, r, err)
		return
	}

	transfer := sr.Share.BeginTransfer()
	defer func() { transfer.End(a.clock.Now()) }()

	cw := &countingWriter{ResponseWriter: w, transfer: transfer}
	cw.WriteHeader(http.StatusOK)

	zw := zip.NewWriter(cw)
	// Speed over ratio: compression must not become the bottleneck on a
	// fast network, and large shares are mostly media that will not shrink.
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestSpeed)
	})

	err := walk(func(m member) error {
		if err := r.Context().Err(); err != nil {
			return err
		}
		return addMember(zw, m)
	})
	if err == nil {
		err = zw.Close()
	}
	if err != nil {
		// The status line has gone out, so the only honest signal left is
		// to cut the connection. Closing the writer instead would hand the
		// visitor a valid-looking zip with files silently missing.
		a.pages.logger.Debug("archive aborted", "error", err)
		panic(http.ErrAbortHandler)
	}
}

// addMember writes one entry. A file that vanished, became unreadable or now
// points outside the share is left out, just as the listing would refuse to
// serve it; only a failure partway through a file aborts the archive.
func addMember(zw *zip.Writer, m member) error {
	if strings.HasSuffix(m.Name, "/") {
		hdr := &zip.FileHeader{Name: m.Name, Method: zip.Store, Modified: m.Modified}
		hdr.SetMode(fs.ModeDir | 0o755)
		_, err := zw.CreateHeader(hdr)
		return err
	}

	f, err := security.OpenShared(m.Path, m.Root)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil
	}

	hdr := &zip.FileHeader{
		Name:     m.Name,
		Modified: info.ModTime(),
		Method:   compressionFor(m.Name),
	}
	hdr.SetMode(info.Mode())
	out, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.CopyBuffer(out, f, make([]byte, 256<<10))
	return err
}

// storedExtensions are formats that are already compressed. Deflating them
// again costs CPU and saves nothing.
var storedExtensions = map[string]bool{
	".zip": true, ".gz": true, ".tgz": true, ".bz2": true, ".xz": true, ".zst": true,
	".7z": true, ".rar": true, ".jar": true, ".apk": true, ".dmg": true, ".whl": true,
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".avif": true, ".heic": true,
	".mp4": true, ".mov": true, ".mkv": true, ".webm": true, ".avi": true, ".m4v": true,
	".mp3": true, ".m4a": true, ".aac": true, ".ogg": true, ".opus": true, ".flac": true,
	".docx": true, ".xlsx": true, ".pptx": true, ".woff2": true,
}

func compressionFor(name string) uint16 {
	if storedExtensions[strings.ToLower(path.Ext(name))] {
		return zip.Store
	}
	return zip.Deflate
}

// directoryMembers walks a directory inside a share. Every path is placed
// under prefix, so the zip unpacks into one folder named after the share.
//
// Symlinked directories are not followed, which keeps a link loop from
// producing an endless archive. Symlinked files are included when they still
// resolve inside the share; OpenShared enforces that per file.
func directoryMembers(abs, root, prefix string) memberSource {
	return func(add func(member) error) error {
		return filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				// An unreadable subfolder is left out rather than failing
				// the whole archive.
				if d != nil && d.IsDir() && p != abs {
					return fs.SkipDir
				}
				return nil
			}
			rel, err := filepath.Rel(abs, p)
			if err != nil {
				return err
			}
			name := path.Join(prefix, filepath.ToSlash(rel))
			switch {
			case d.IsDir():
				m := member{Name: name + "/"}
				if info, err := d.Info(); err == nil {
					m.Modified = info.ModTime()
				}
				return add(m)
			case d.Type().IsRegular() || d.Type()&fs.ModeSymlink != 0:
				return add(member{Name: name, Path: p, Root: root})
			default:
				// Sockets, devices and fifos are not shareable content.
				return nil
			}
		})
	}
}

// entryMembers lists the files of a multi-file share at the top of the zip.
func entryMembers(entries []sharing.Entry) memberSource {
	return func(add func(member) error) error {
		for _, e := range entries {
			if err := add(member{Name: e.Name, Path: e.Path}); err != nil {
				return err
			}
		}
		return nil
	}
}
