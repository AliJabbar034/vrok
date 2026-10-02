package sharing

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// ErrDuplicate is returned when a share id or token is already registered.
var ErrDuplicate = errors.New("sharing: share already registered")

// Resolver turns a URL token into a share. The HTTP layer needs nothing more
// than this, so that is all it depends on.
type Resolver interface {
	ByToken(token string) (*Share, error)
}

// Manager is the management surface used by the CLI commands.
type Manager interface {
	Resolver
	ByID(id string) (*Share, error)
	List() []*Share
	Remove(id string) bool
}

// Registry is the in-memory index of live shares. V1 deliberately has no
// database: shares exist only while the process that created them runs, which
// is exactly the lifetime guarantee vrok promises its users.
type Registry struct {
	mu      sync.RWMutex
	byID    map[string]*Share
	byToken map[string]*Share
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		byID:    make(map[string]*Share),
		byToken: make(map[string]*Share),
	}
}

// Add registers a share. Both the id and the token must be unused.
func (r *Registry) Add(s *Share) error {
	if s == nil {
		return errors.New("sharing: nil share")
	}
	spec := s.Spec()
	if spec.ID == "" || spec.Token == "" {
		return errors.New("sharing: share requires an id and a token")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byID[spec.ID]; exists {
		return ErrDuplicate
	}
	if _, exists := r.byToken[spec.Token]; exists {
		return ErrDuplicate
	}
	r.byID[spec.ID] = s
	r.byToken[spec.Token] = s
	return nil
}

// ByToken implements Resolver.
func (r *Registry) ByToken(token string) (*Share, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	// Map lookup is not constant time with respect to the token, but the
	// token is 128 bits of entropy: there is no feasible guessing attack for
	// timing to accelerate.
	s, ok := r.byToken[token]
	if !ok {
		return nil, ErrNotFound
	}
	return s, nil
}

// ByID implements Manager.
func (r *Registry) ByID(id string) (*Share, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	return s, nil
}

// List returns every registered share, oldest first.
func (r *Registry) List() []*Share {
	r.mu.RLock()
	shares := make([]*Share, 0, len(r.byID))
	for _, s := range r.byID {
		shares = append(shares, s)
	}
	r.mu.RUnlock()

	sort.Slice(shares, func(i, j int) bool {
		return shares[i].Spec().CreatedAt.Before(shares[j].Spec().CreatedAt)
	})
	return shares
}

// Len returns the number of registered shares.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byID)
}

// Remove revokes and unregisters the share with the given id. Revoking before
// unregistering closes the window where an in-flight request still holds a
// pointer to the share and would otherwise be allowed to continue.
func (r *Registry) Remove(id string) bool {
	r.mu.Lock()
	s, ok := r.byID[id]
	if ok {
		delete(r.byID, id)
		delete(r.byToken, s.Spec().Token)
	}
	r.mu.Unlock()

	if ok {
		s.Revoke()
	}
	return ok
}

// RemoveAll revokes and unregisters every share, returning what was removed.
func (r *Registry) RemoveAll() []*Share {
	r.mu.Lock()
	removed := make([]*Share, 0, len(r.byID))
	for id, s := range r.byID {
		removed = append(removed, s)
		delete(r.byID, id)
		delete(r.byToken, s.Spec().Token)
	}
	r.mu.Unlock()

	for _, s := range removed {
		s.Revoke()
	}
	return removed
}

// PurgeExpired removes shares that are no longer available at now, returning
// them so the caller can report what went away.
func (r *Registry) PurgeExpired(now time.Time) []*Share {
	r.mu.Lock()
	var expired []*Share
	for id, s := range r.byID {
		if !spent(s.Snapshot(), now) {
			continue
		}
		expired = append(expired, s)
		delete(r.byID, id)
		delete(r.byToken, s.Spec().Token)
	}
	r.mu.Unlock()

	for _, s := range expired {
		s.Revoke()
	}
	return expired
}

// DownloadLimitGrace is how long a share that has used its download allowance
// stays registered after its last transfer goes quiet. A browser playing a
// video pauses its fetch once it has buffered enough and resumes with a new
// ranged request later; without a grace period, that resume would find the
// share gone and the video would stop partway through.
const DownloadLimitGrace = 30 * time.Second

// spent reports whether the reaper should drop a share.
//
// An expired share goes at once: its TTL is a promise to the sharer, and
// in-flight transfers end with it. A share that has used its download
// allowance is different. New downloads are already refused at claim time, so
// keeping it registered only lets the transfers it granted finish. It goes
// once none are running and the last one has been quiet for
// DownloadLimitGrace.
func spent(s Snapshot, now time.Time) bool {
	if NotExpired().Check(s, now) != nil {
		return true
	}
	if UnderDownloadLimit().Check(s, now) == nil {
		return false
	}
	return s.ActiveTransfers == 0 && now.Sub(s.LastAccess) >= DownloadLimitGrace
}
