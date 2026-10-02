// Package awake keeps the computer from going to sleep while someone is
// downloading from it. A laptop that dozes off halfway through a large
// transfer is the most common way a share fails without anyone noticing.
//
// Only idle sleep is prevented. Closing the lid or choosing Sleep still
// works: that is the owner deciding, not the machine guessing.
package awake

import (
	"errors"
	"time"
)

// ErrUnsupported means this platform has no way vrok knows to stay awake.
var ErrUnsupported = errors.New("awake: not supported on this system")

// Grace is how long the machine is kept awake after the last transfer ends.
// Video players and download managers often pause between requests, and
// letting the machine sleep in that gap would cut them off.
const Grace = 30 * time.Second

// Keeper holds the machine awake while transfers run. It is driven by
// Update from a single goroutine and is not safe for concurrent use.
type Keeper struct {
	hold     func() (release func(), err error)
	release  func()
	lastBusy time.Time
	// failed stops a platform that refused once from being asked again on
	// every tick.
	failed bool
}

// New returns a Keeper using the platform's sleep inhibitor.
func New() *Keeper { return &Keeper{hold: hold} }

// Update reports whether a transfer is running at now.
func (k *Keeper) Update(busy bool, now time.Time) {
	if busy {
		k.lastBusy = now
	}
	want := !k.lastBusy.IsZero() && now.Sub(k.lastBusy) < Grace

	switch {
	case want && k.release == nil && !k.failed:
		release, err := k.hold()
		if err != nil {
			k.failed = true
			return
		}
		k.release = release
	case !want && k.release != nil:
		k.Close()
	}
}

// Holding reports whether the machine is currently being kept awake.
func (k *Keeper) Holding() bool { return k.release != nil }

// Close lets the machine sleep again.
func (k *Keeper) Close() {
	if k.release != nil {
		k.release()
		k.release = nil
	}
}
