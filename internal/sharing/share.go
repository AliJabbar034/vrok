// Package sharing holds vrok's domain model: what a share is, how long it
// lives and how many times it may be fetched. It knows nothing about HTTP,
// tunnels or the terminal, which keeps the rules testable in isolation.
package sharing

import (
	"fmt"
	"slices"
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
	// KindReceive is an inbox folder that visitors upload files into. It
	// reuses the download counters: each file received claims one unit of
	// the allowance, so MaxDownloads caps how many files it accepts.
	KindReceive
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
	case KindReceive:
		return "receive"
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

// Spec is the description of a share. Identity fields (ID, Token, Kind, Root,
// Entries, Target) are fixed at creation so the URL never rotates. Lifetime
// fields (ExpiresAt, MaxDownloads, PasswordHash) may be updated in place
// under the share lock, which is how a running share grows a password or
// becomes one-time without minting a new link.
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
	// Root is the confined directory for KindDirectory, and the inbox that
	// uploads are written into for KindReceive.
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
	// ActiveTransfers counts file bodies being streamed right now. A share
	// that has used its download allowance is kept until this reaches zero,
	// so the last permitted download is never cut off mid-transfer.
	ActiveTransfers int `json:"active_transfers"`
	// Transfers is the progress of each body being streamed, oldest first.
	Transfers []TransferProgress `json:"transfers,omitempty"`
}

// TransferProgress is how far one in-flight transfer has got.
type TransferProgress struct {
	// Sent is the payload bytes delivered so far.
	Sent int64 `json:"sent"`
	// Total is the expected body size, or -1 when it is not known up front,
	// as with a folder streamed as a zip.
	Total int64 `json:"total"`
}

// Share is a live share: a Spec plus mutable access counters.
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
	// transfers holds the bodies streaming right now, keyed by Transfer.id.
	transfers map[uint64]*TransferProgress
	nextID    uint64
}

// New returns a live share for spec.
func New(spec Spec) *Share { return &Share{spec: spec} }

// Spec returns a copy of the share's description.
func (s *Share) Spec() Spec {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.spec
}

// ID returns the public handle.
func (s *Share) ID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.spec.ID
}

// Token returns the URL secret.
func (s *Share) Token() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.spec.Token
}

// SetExpiresAt updates when the share stops answering. The zero time means
// the share lives until the process exits. The URL does not change.
func (s *Share) SetExpiresAt(t time.Time) {
	s.mu.Lock()
	s.spec.ExpiresAt = t
	s.mu.Unlock()
}

// SetMaxDownloads updates the download cap. Zero means unlimited. Setting 1
// turns the share into a one-time link without minting a new URL.
func (s *Share) SetMaxDownloads(n int) {
	if n < 0 {
		n = 0
	}
	s.mu.Lock()
	s.spec.MaxDownloads = n
	s.mu.Unlock()
}

// AllowOneMore caps the share at one further download, counting from now,
// which is what makes a running share a one-time link. Downloads already
// served do not use up the new allowance.
func (s *Share) AllowOneMore() {
	s.mu.Lock()
	s.spec.MaxDownloads = s.downloads + 1
	s.mu.Unlock()
}

// SetPasswordHash stores a new Argon2id digest. An empty hash removes the
// password. The plaintext never enters the share.
func (s *Share) SetPasswordHash(hash string) {
	s.mu.Lock()
	s.spec.PasswordHash = hash
	s.mu.Unlock()
}

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
		ActiveTransfers:  len(s.transfers),
		Transfers:        s.transferProgress(),
	}
}

// transferProgress copies the in-flight transfers in the order they began.
// The caller holds mu.
func (s *Share) transferProgress() []TransferProgress {
	if len(s.transfers) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(s.transfers))
	for id := range s.transfers {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := make([]TransferProgress, len(ids))
	for i, id := range ids {
		out[i] = *s.transfers[id]
	}
	return out
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

// Transfer is one file body being streamed. Bytes are reported as they are
// written, so the owner sees a multi-gigabyte download progress live rather
// than only once it ends.
type Transfer struct {
	share *Share
	id    uint64
}

// BeginTransfer records that a file body has started streaming. Every call
// must be paired with End on the returned Transfer.
func (s *Share) BeginTransfer() *Transfer {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transfers == nil {
		s.transfers = make(map[uint64]*TransferProgress)
	}
	s.nextID++
	s.transfers[s.nextID] = &TransferProgress{Total: -1}
	return &Transfer{share: s, id: s.nextID}
}

// Resume records that sent bytes of this transfer were delivered earlier,
// by a previous connection. It moves the progress without counting them
// again in the share's total, so a resumed upload neither restarts its
// progress bar at zero nor inflates the bytes transferred.
func (t *Transfer) Resume(sent int64) {
	t.share.mu.Lock()
	if p, ok := t.share.transfers[t.id]; ok {
		p.Sent = sent
	}
	t.share.mu.Unlock()
}

// SetTotal records the expected body size once the response headers say it.
func (t *Transfer) SetTotal(n int64) {
	t.share.mu.Lock()
	if p, ok := t.share.transfers[t.id]; ok {
		p.Total = n
	}
	t.share.mu.Unlock()
}

// Add counts n payload bytes as delivered, both for this transfer and for
// the share's running total.
func (t *Transfer) Add(n int64) {
	if n <= 0 {
		return
	}
	t.share.mu.Lock()
	t.share.bytes += n
	if p, ok := t.share.transfers[t.id]; ok {
		p.Sent += n
	}
	t.share.mu.Unlock()
}

// End records that the transfer finished, successfully or not. The end of a
// long download is the share's most recent activity, so it also counts as an
// access.
func (t *Transfer) End(now time.Time) {
	s := t.share
	s.mu.Lock()
	delete(s.transfers, t.id)
	if now.After(s.lastAccess) {
		s.lastAccess = now
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
	spec := s.Spec()
	switch spec.Kind {
	case KindHTTP:
		return spec.Target
	case KindDirectory, KindReceive:
		return spec.Root
	case KindFile:
		if len(spec.Entries) == 1 {
			return spec.Entries[0].Path
		}
	case KindFiles:
		return fmt.Sprintf("%d files", len(spec.Entries))
	}
	return spec.Name
}
