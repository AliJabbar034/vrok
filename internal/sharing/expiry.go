package sharing

import (
	"context"
	"time"
)

// Clock abstracts the passage of time so expiry logic can be tested without
// sleeping.
type Clock interface {
	Now() time.Time
}

// SystemClock reads the wall clock.
type SystemClock struct{}

// Now implements Clock.
func (SystemClock) Now() time.Time { return time.Now() }

// Reaper periodically drops shares that have expired or exhausted their
// download allowance.
//
// Expiry is also enforced on every request, so the reaper is not what makes a
// share safe; it exists to release memory and to let the CLI exit on its own
// once the last share is gone.
type Reaper struct {
	registry *Registry
	clock    Clock
	interval time.Duration

	// OnExpire is called once per expired share, outside the registry lock.
	OnExpire func(*Share)
	// OnEmpty is called when the reaper removes the final share, which is how
	// `vrok share --ttl` ends without user interaction.
	OnEmpty func()
}

// NewReaper returns a reaper checking the registry every interval.
func NewReaper(reg *Registry, clock Clock, interval time.Duration) *Reaper {
	if clock == nil {
		clock = SystemClock{}
	}
	if interval <= 0 {
		interval = time.Second
	}
	return &Reaper{registry: reg, clock: clock, interval: interval}
}

// Run blocks until ctx is cancelled, purging expired shares as it goes.
func (r *Reaper) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.tick()
		}
	}
}

func (r *Reaper) tick() {
	expired := r.registry.PurgeExpired(r.clock.Now())
	if len(expired) == 0 {
		return
	}
	if r.OnExpire != nil {
		for _, s := range expired {
			r.OnExpire(s)
		}
	}
	if r.OnEmpty != nil && r.registry.Len() == 0 {
		r.OnEmpty()
	}
}
