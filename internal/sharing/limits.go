package sharing

import (
	"errors"
	"time"
)

// Availability failures. The HTTP layer maps all of them to "this share is
// gone" responses; it never reveals which condition tripped beyond what the
// visitor already knows.
var (
	// ErrRevoked means the owner stopped the share explicitly.
	ErrRevoked = errors.New("sharing: share revoked")
	// ErrExpired means the share outlived its TTL.
	ErrExpired = errors.New("sharing: share expired")
	// ErrDownloadLimit means the download allowance is used up.
	ErrDownloadLimit = errors.New("sharing: download limit reached")
	// ErrNotFound means no share matched the supplied token or id.
	ErrNotFound = errors.New("sharing: share not found")
)

// Unavailable reports whether err is one of the terminal availability errors,
// i.e. whether the correct answer to the visitor is "gone".
func Unavailable(err error) bool {
	return errors.Is(err, ErrRevoked) ||
		errors.Is(err, ErrExpired) ||
		errors.Is(err, ErrDownloadLimit) ||
		errors.Is(err, ErrNotFound)
}

// Guard decides whether a share may still be served. Keeping each rule behind
// this interface means new rules (IP allowlists, access windows, quotas) can be
// added without touching the HTTP handlers that enforce them.
type Guard interface {
	Check(s Snapshot, now time.Time) error
}

// GuardFunc adapts a plain function to Guard.
type GuardFunc func(Snapshot, time.Time) error

// Check implements Guard.
func (f GuardFunc) Check(s Snapshot, now time.Time) error { return f(s, now) }

// Guards runs a set of guards in order and reports the first failure.
type Guards []Guard

// Check implements Guard.
func (g Guards) Check(s Snapshot, now time.Time) error {
	for _, guard := range g {
		if err := guard.Check(s, now); err != nil {
			return err
		}
	}
	return nil
}

// NotRevoked rejects shares the owner has stopped.
func NotRevoked() Guard {
	return GuardFunc(func(s Snapshot, _ time.Time) error {
		if s.Revoked {
			return ErrRevoked
		}
		return nil
	})
}

// NotExpired rejects shares past their TTL. A zero ExpiresAt never expires.
func NotExpired() Guard {
	return GuardFunc(func(s Snapshot, now time.Time) error {
		if !s.ExpiresAt.IsZero() && now.After(s.ExpiresAt) {
			return ErrExpired
		}
		return nil
	})
}

// UnderDownloadLimit rejects shares whose download allowance is spent. The
// authoritative check happens in Share.ClaimDownload; this guard exists so
// that browsing pages and listings also stop working once the limit is hit.
func UnderDownloadLimit() Guard {
	return GuardFunc(func(s Snapshot, _ time.Time) error {
		if s.MaxDownloads > 0 && s.Downloads >= s.MaxDownloads {
			return ErrDownloadLimit
		}
		return nil
	})
}

// DefaultGuards returns the availability rules every share is subject to.
func DefaultGuards() Guards {
	return Guards{NotRevoked(), NotExpired(), UnderDownloadLimit()}
}

// RemainingDownloads returns how many downloads are left, and whether a limit
// applies at all.
func RemainingDownloads(s Snapshot) (int, bool) {
	if s.MaxDownloads <= 0 {
		return 0, false
	}
	remaining := s.MaxDownloads - s.Downloads
	if remaining < 0 {
		remaining = 0
	}
	return remaining, true
}

// TimeLeft returns the duration until the share expires. The second result is
// false for shares with no TTL.
func TimeLeft(s Snapshot, now time.Time) (time.Duration, bool) {
	if s.ExpiresAt.IsZero() {
		return 0, false
	}
	left := s.ExpiresAt.Sub(now)
	if left < 0 {
		left = 0
	}
	return left, true
}
