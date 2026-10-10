package update

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CheckInterval is how often vrok asks GitHub for the newest release on its
// own, so the request costs one HEAD a day rather than one per command.
const CheckInterval = 24 * time.Hour

// RemindInterval is how long a shown notice stays quiet for the same release.
// Once a day is enough to be noticed without becoming the thing a user scrolls
// past every time.
const RemindInterval = 24 * time.Hour

// State is what the update notice remembers between runs.
type State struct {
	// Latest is the newest release tag seen, such as "v0.7.0".
	Latest string `json:"latest,omitempty"`
	// CheckedAt is when GitHub was last asked.
	CheckedAt time.Time `json:"checked_at"`
	// NotifiedFor and NotifiedAt record the last notice shown.
	NotifiedFor string    `json:"notified_for,omitempty"`
	NotifiedAt  time.Time `json:"notified_at"`
}

// StatePath returns where State is kept. It is a cache, not configuration:
// deleting it only means the next run checks again.
func StatePath() (string, error) {
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		var err error
		if dir, err = os.UserCacheDir(); err != nil {
			return "", fmt.Errorf("update: locate cache directory: %w", err)
		}
	}
	return filepath.Join(dir, "vrok", "update-check.json"), nil
}

// LoadState reads State. A missing or damaged file is the zero State, which
// simply makes the next run check again.
func LoadState(path string) State {
	var s State
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}
	}
	if json.Unmarshal(data, &s) != nil {
		return State{}
	}
	return s
}

// SaveState writes State through a temporary file, so two vrok processes
// finishing together cannot leave a half-written file behind.
func SaveState(path string, s State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	temp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		os.Remove(temp)
		return err
	}
	return nil
}

// Stale reports whether GitHub should be asked again. A check dated in the
// future means the clock moved, and is treated as stale rather than trusted.
func (s State) Stale(now time.Time) bool {
	return now.Sub(s.CheckedAt) >= CheckInterval || now.Before(s.CheckedAt)
}

// Pending returns the release to announce to someone running current, or ""
// when there is nothing newer or the same release was announced recently.
func (s State) Pending(current string, now time.Time) string {
	if s.Latest == "" {
		return ""
	}
	if newer, err := Newer(s.Latest, current); err != nil || !newer {
		return ""
	}
	if s.NotifiedFor == s.Latest && now.Sub(s.NotifiedAt) < RemindInterval && !now.Before(s.NotifiedAt) {
		return ""
	}
	return s.Latest
}
