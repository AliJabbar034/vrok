// Package inbox writes files that visitors upload into a folder the owner
// chose. Every operation goes through an os.Root, so no name, symlink or
// "../" can reach outside that folder. Uploads arrive in chunks and are kept
// in a hidden part file until the last byte is in, then renamed into place
// without ever replacing a file that is already there.
package inbox

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync"
)

// Errors a caller maps to a response.
var (
	// ErrUnknownUpload means no upload has that id, or it has ended.
	ErrUnknownUpload = errors.New("inbox: no such upload")
	// ErrOffset means a chunk did not start where the upload has got to.
	ErrOffset = errors.New("inbox: chunk does not continue the upload")
	// ErrTooLarge means more bytes arrived than the upload declared.
	ErrTooLarge = errors.New("inbox: more data than the declared size")
	// ErrIncomplete means Finish was called before every byte arrived.
	ErrIncomplete = errors.New("inbox: upload is not complete")
	// ErrNoSpace means accepting the upload would leave too little disk.
	ErrNoSpace = errors.New("inbox: not enough free disk space")
	// ErrBusy means too many uploads are open at once, or this one is
	// already being written by another request.
	ErrBusy = errors.New("inbox: busy")
)

const (
	// partPrefix marks an upload in progress. The leading dot hides it in
	// file managers, and SafeName strips leading dots, so no visitor can
	// name a finished file to look like one.
	partPrefix = ".vrok-"
	partSuffix = ".part"

	// maxOpenUploads bounds the part files held open at once, so a visitor
	// cannot exhaust file descriptors by starting uploads and never sending.
	maxOpenUploads = 64

	// DiskReserve is the free space always left on the disk. Filling a disk
	// to the last byte breaks other programs, not just this one.
	DiskReserve = 512 << 20
)

// Received describes a file that arrived completely.
type Received struct {
	// Name is the file's final name in the inbox, after any renaming to
	// avoid an existing file.
	Name string
	Size int64
}

// Inbox is one folder accepting uploads. It is safe for concurrent use.
type Inbox struct {
	root *os.Root
	dir  string

	// freeSpace reports the bytes available to this process on the disk
	// holding dir, or a negative number when that cannot be known.
	freeSpace func(dir string) int64

	mu      sync.Mutex
	uploads map[string]*upload
	closed  bool
}

type upload struct {
	id      string
	name    string // the safe name it will be given
	part    string // the part file's name inside the inbox
	size    int64
	written int64
	file    *os.File
	busy    bool
}

// Open returns an Inbox writing into dir, which must exist.
func Open(dir string) (*Inbox, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("inbox: open %s: %w", dir, err)
	}
	return &Inbox{root: root, dir: dir, freeSpace: freeBytes, uploads: make(map[string]*upload)}, nil
}

// Dir is the folder files are written into.
func (in *Inbox) Dir() string { return in.dir }

// Begin starts an upload of size bytes that will be called name. It checks
// for disk space up front, so a file that cannot fit is refused before the
// sender spends minutes uploading it.
func (in *Inbox) Begin(name string, size int64) (string, error) {
	if size < 0 {
		return "", fmt.Errorf("inbox: negative size %d", size)
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.closed {
		return "", ErrUnknownUpload
	}
	if len(in.uploads) >= maxOpenUploads {
		return "", ErrBusy
	}
	if !in.room(size) {
		return "", ErrNoSpace
	}

	id, err := newID()
	if err != nil {
		return "", err
	}
	part := partPrefix + id + partSuffix
	// The finished file keeps this mode after the rename, so it is created
	// like any other download and the umask decides who can read it.
	file, err := in.root.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", fmt.Errorf("inbox: create part file: %w", err)
	}
	in.uploads[id] = &upload{id: id, name: SafeName(name), part: part, size: size, file: file}
	return id, nil
}

// Fits reports whether total more bytes would fit next to the uploads
// already open, so a batch that cannot fit is refused before the owner is
// even asked about it. Begin still checks each file as it starts.
func (in *Inbox) Fits(total int64) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	if total < 0 || !in.room(total) {
		return ErrNoSpace
	}
	return nil
}

// room reports whether n more bytes fit on the disk next to the uploads
// already open, with DiskReserve to spare. It subtracts rather than adds, so
// a declared size near the int64 limit cannot wrap around and pass. The
// caller holds in.mu.
func (in *Inbox) room(n int64) bool {
	free := in.freeSpace(in.dir)
	if free < 0 {
		return true
	}
	return n <= free-DiskReserve-in.pending()
}

// pending is the bytes still to arrive for every open upload, which the disk
// has to have room for too. The caller holds in.mu.
func (in *Inbox) pending() int64 {
	var n int64
	for _, u := range in.uploads {
		n += u.size - u.written
	}
	return n
}

// Status reports how many bytes of an upload have arrived, and its size.
// A sender whose connection dropped asks this to know where to resume.
func (in *Inbox) Status(id string) (written, size int64, err error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	u, ok := in.uploads[id]
	if !ok {
		return 0, 0, ErrUnknownUpload
	}
	return u.written, u.size, nil
}

// Write appends one chunk, which must start at offset. It returns how far
// the upload has got afterwards. Bytes that arrived before a connection
// dropped are kept, so the sender resumes from the true position rather
// than resending the whole chunk. progress, when not nil, is told about
// each block written.
func (in *Inbox) Write(id string, offset int64, r io.Reader, progress func(int64)) (int64, error) {
	u, err := in.claim(id)
	if err != nil {
		return 0, err
	}
	defer in.release(u)

	if offset != u.written {
		return u.written, ErrOffset
	}
	room := u.size - u.written
	// One byte past the declared size is read so an oversized body is
	// detected rather than silently cut; countingReader keeps it out of the
	// file.
	n, err := io.Copy(u.file, &countingReader{r: io.LimitReader(r, room+1), seen: progress, limit: room})
	in.mu.Lock()
	u.written += n
	written := u.written
	in.mu.Unlock()

	var tooLarge *tooLargeError
	switch {
	case errors.As(err, &tooLarge):
		return written, ErrTooLarge
	case err != nil:
		return written, err
	}
	return written, nil
}

// Finish moves a complete upload into place under its name, or the first
// free variant of it, and returns what it was called.
func (in *Inbox) Finish(id string) (Received, error) {
	u, err := in.claim(id)
	if err != nil {
		return Received{}, err
	}
	if u.written != u.size {
		in.release(u)
		return Received{}, ErrIncomplete
	}
	if err := u.file.Close(); err != nil {
		in.release(u)
		return Received{}, fmt.Errorf("inbox: close part file: %w", err)
	}

	name, err := in.place(u)
	in.mu.Lock()
	delete(in.uploads, id)
	in.mu.Unlock()
	if err != nil {
		in.root.Remove(u.part)
		return Received{}, err
	}
	return Received{Name: name, Size: u.size}, nil
}

// place renames the part file to the first free name. Each candidate is
// reserved by creating it exclusively before the rename replaces it, so a
// file that appeared in the meantime, from anywhere, is never overwritten.
func (in *Inbox) place(u *upload) (string, error) {
	for n := 0; n < 10000; n++ {
		name := u.name
		if n > 0 {
			name = numbered(u.name, n)
		}
		reserved, err := in.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("inbox: create %s: %w", name, err)
		}
		reserved.Close()
		if err := in.root.Rename(u.part, name); err != nil {
			in.root.Remove(name)
			return "", fmt.Errorf("inbox: move %s into place: %w", name, err)
		}
		return name, nil
	}
	return "", fmt.Errorf("inbox: no free name for %s", u.name)
}

// Abort discards an upload and its part file.
func (in *Inbox) Abort(id string) {
	in.mu.Lock()
	u, ok := in.uploads[id]
	if ok {
		delete(in.uploads, id)
	}
	in.mu.Unlock()
	if ok {
		u.file.Close()
		in.root.Remove(u.part)
	}
}

// Close discards every unfinished upload and releases the folder. Files
// that arrived completely are left where they are.
func (in *Inbox) Close() error {
	in.mu.Lock()
	in.closed = true
	uploads := in.uploads
	in.uploads = map[string]*upload{}
	in.mu.Unlock()
	for _, u := range uploads {
		u.file.Close()
		in.root.Remove(u.part)
	}
	return in.root.Close()
}

// claim marks an upload as being worked on, so two requests can never write
// the same part file at once.
func (in *Inbox) claim(id string) (*upload, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	u, ok := in.uploads[id]
	if !ok {
		return nil, ErrUnknownUpload
	}
	if u.busy {
		return nil, ErrBusy
	}
	u.busy = true
	return u, nil
}

func (in *Inbox) release(u *upload) {
	in.mu.Lock()
	u.busy = false
	in.mu.Unlock()
}

func newID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

type tooLargeError struct{}

func (*tooLargeError) Error() string { return ErrTooLarge.Error() }

// countingReader reports progress and fails once more than limit bytes have
// been read, before the excess reaches the file.
type countingReader struct {
	r     io.Reader
	seen  func(int64)
	limit int64
	read  int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if c.read+int64(n) > c.limit {
		n = int(c.limit - c.read)
		c.read = c.limit
		if c.seen != nil && n > 0 {
			c.seen(int64(n))
		}
		return n, &tooLargeError{}
	}
	c.read += int64(n)
	if c.seen != nil && n > 0 {
		c.seen(int64(n))
	}
	return n, err
}
