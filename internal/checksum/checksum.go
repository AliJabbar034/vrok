// Package checksum works out SHA-256 fingerprints of shared files in the
// background. A visitor can compare the fingerprint with their copy to know a
// multi-gigabyte download arrived intact, and the share never waits for the
// hash before it starts answering.
package checksum

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"sync"

	"github.com/AliJabbar034/vrok/internal/security"
)

// State says whether a fingerprint can be shown yet.
type State uint8

const (
	// Pending means the hash is queued or being worked out.
	Pending State = iota
	// Ready means the hash is known.
	Ready
	// Unavailable means the file could not be hashed, or too much work is
	// already queued. Nothing should be shown.
	Unavailable
)

// maxQueued bounds the hashes waiting their turn. A visitor clicking through
// every file of a large folder must not queue unbounded disk reads.
const maxQueued = 64

// Cache remembers fingerprints per file version and computes missing ones
// one at a time, so hashing never competes with itself for the disk.
type Cache struct {
	mu      sync.Mutex
	entries map[key]*entry
	queued  int
	// turn is held by the one goroutine currently reading a file.
	turn chan struct{}
}

// key identifies one version of a file: an edit changes size or mtime, which
// makes the old fingerprint unreachable rather than wrong.
type key struct {
	path string
	size int64
	mod  int64
}

type entry struct {
	state State
	sum   string
}

// New returns an empty cache.
func New() *Cache {
	return &Cache{entries: make(map[key]*entry), turn: make(chan struct{}, 1)}
}

// Lookup returns the SHA-256 of the file at path, as hex, once it is known.
// Until then it starts the work in the background and reports Pending; a
// later call returns the result. root confines the read exactly as it does
// for downloads (see security.OpenShared).
func (c *Cache) Lookup(path, root string, info fs.FileInfo) (string, State) {
	k := key{path: path, size: info.Size(), mod: info.ModTime().UnixNano()}

	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[k]; ok {
		return e.sum, e.state
	}
	if c.queued >= maxQueued {
		return "", Unavailable
	}
	c.entries[k] = &entry{state: Pending}
	c.queued++
	go c.compute(k, root)
	return "", Pending
}

func (c *Cache) compute(k key, root string) {
	c.turn <- struct{}{}
	sum, ok := hashFile(k, root)
	<-c.turn

	c.mu.Lock()
	defer c.mu.Unlock()
	c.queued--
	if ok {
		c.entries[k] = &entry{state: Ready, sum: sum}
	} else {
		c.entries[k] = &entry{state: Unavailable}
	}
}

// hashFile reads the file through the same confinement as a download and
// refuses a result for a file that changed while it was being read.
func hashFile(k key, root string) (string, bool) {
	f, err := security.OpenShared(k.path, root)
	if err != nil {
		return "", false
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, make([]byte, 1<<20)); err != nil {
		return "", false
	}
	after, err := f.Stat()
	if err != nil || after.Size() != k.size || after.ModTime().UnixNano() != k.mod {
		return "", false
	}
	return hex.EncodeToString(h.Sum(nil)), true
}
