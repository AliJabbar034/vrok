// Package sharing holds vrok's domain model: what a share is, how long it
// lives and how many times it may be fetched. It knows nothing about HTTP,
// tunnels or the terminal, which keeps the rules testable in isolation.
package sharing

import (
	"fmt"
	"sync"
	"time"
)

// Kind enumerates the things vrok can expose.
type Kind uint8

const (
	// KindFile is a single local file.
	KindFile Kind = iota
	// KindFiles is a set of local files presented through an index page.
	KindFiles
	// KindDirectory is a local directory tree, browsable in place.
	KindDirectory
	// KindHTTP is a local HTTP service, reverse-proxied as-is.
	KindHTTP
)

// String implements fmt.Stringer.
func (k Kind) String() string {
	switch k {
	case KindFile:
		return "file"
	case KindFiles:
		return "files"
	case KindDirectory:
		return "directory"
	case KindHTTP:
		return "http"
	default:
		return "unknown"
	}
}

// Entry is one named file inside a share. Path is an absolute local path that
// was validated when the share was created; Name is the only part a visitor
// ever sees or sends back.
type Entry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Spec is the immutable description of a share. Once a Share exists its Spec
// never changes, so it can be copied and read without synchronisation.
type Spec struct {
	// ID is the short public handle used for management commands and, with a
	// relay, as the hostname label.
	ID string `json:"id"`
	// Token is the 128-bit secret that appears in the URL path. Possession of
	// the token is what authorises access.
	Token string `json:"token"`
	// Name is what the share is called in listings and page titles.
	Name string `json:"name"`
	// Kind selects which handler serves the share.
	Kind Kind `json:"kind"`
	// Root is the confined directory for KindDirectory.
	Root string `json:"root,omitempty"`
	// Entries lists the files for KindFile and KindFiles.
	Entries []Entry `json:"entries,omitempty"`
	// Target is the upstream origin for KindHTTP, e.g. http://127.0.0.1:3000.
	Target string `json:"target,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	// ExpiresAt is when the share stops answering. The zero value means the
	// share lives as long as the process does.
	ExpiresAt time.Time `json:"expires_at"`
	// MaxDownloads caps completed file transfers; zero means unlimited.
	MaxDownloads int `json:"max_downloads"`
	// PasswordHash is an Argon2id digest, or empty for an unprotected share.
	// The plaintext password is never stored anywhere.
	PasswordHash string `json:"-"`
}

// Protected reports whether the share requires a password.
func (s Spec) Protected() bool { return s.PasswordHash != "" }

// Snapshot is a consistent, read-only view of a share at one instant. Guards
// and the CLI consume snapshots so they cannot mutate live state by accident.
type Snapshot struct {
	Spec
	Downloads        int       `json:"downloads"`
	BytesTransferred int64     `json:"bytes_transferred"`
	LastAccess       time.Time `json:"last_access"`
	Revoked          bool      `json:"revoked"`
}

// Share is a live share: an immutable Spec plus mutable access counters.
//
// Every mutable field is guarded by mu. Counters are deliberately not atomics:
// the download limit has to be read and incremented as one step, otherwise two
// simultaneous requests could both slip past the final permitted download.
type Share struct {
	spec Spec

	mu         sync.RWMutex
	downloads  int
	bytes      int64
	lastAccess time.Time
	revoked    bool
}

// New returns a live share for spec.
func New(spec Spec) *Share { return &Share{spec: spec} }

// Spec returns the share's immutable description.
func (s *Share) Spec() Spec { return s.spec }

// ID returns the public handle.
func (s *Share) ID() string { return s.spec.ID }

// Token returns the URL secret.
func (s *Share) Token() string { return s.spec.Token }

// Snapshot returns a consistent view of the share.
func (s *Share) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Snapshot{
		Spec:             s.spec,
		Downloads:        s.downloads,
		BytesTransferred: s.bytes,
		LastAccess:       s.lastAccess,
		Revoked:          s.revoked,
	}
}

// Touch records that a visitor interacted with the share.
func (s *Share) Touch(now time.Time) {
	s.mu.Lock()
	s.lastAccess = now
	s.mu.Unlock()
}

// AddBytes accumulates transferred payload bytes for local statistics.
func (s *Share) AddBytes(n int64) {
	if n <= 0 {
		return
	}
	s.mu.Lock()
	s.bytes += n
	s.mu.Unlock()
}

// ClaimDownload reserves one unit of the download allowance and reports the
// resulting count. It returns ErrDownloadLimit if the allowance is exhausted,
// so the caller can refuse the transfer before writing a single byte.
//
// Claiming up front (rather than counting on completion) is what makes
// --downloads a hard limit under concurrency.
func (s *Share) ClaimDownload(now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked {
		return s.downloads, ErrRevoked
	}
	if s.spec.MaxDownloads > 0 && s.downloads >= s.spec.MaxDownloads {
		return s.downloads, ErrDownloadLimit
	}
	s.downloads++
	s.lastAccess = now
	return s.downloads, nil
}

// ReleaseDownload returns a previously claimed download to the pool. It is
// called when a transfer fails before delivering anything, so a dropped
// connection does not silently consume a visitor's allowance.
func (s *Share) ReleaseDownload() {
	s.mu.Lock()
	if s.downloads > 0 {
		s.downloads--
	}
	s.mu.Unlock()
}

// Revoke permanently disables the share. It is idempotent.
func (s *Share) Revoke() {
	s.mu.Lock()
	s.revoked = true
	s.mu.Unlock()
}

// Describe renders the share's source in a form suitable for the terminal.
func (s *Share) Describe() string {
	switch s.spec.Kind {
	case KindHTTP:
		return s.spec.Target
	case KindDirectory:
		return s.spec.Root
	case KindFile:
		if len(s.spec.Entries) == 1 {
			return s.spec.Entries[0].Path
		}
	case KindFiles:
		return fmt.Sprintf("%d files", len(s.spec.Entries))
	}
	return s.spec.Name
}
