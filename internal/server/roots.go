package server

import (
	"sync"

	"github.com/AliJabbar034/vrok/internal/security"
	"github.com/AliJabbar034/vrok/internal/sharing"
)

// rootCache provides one confined PathResolver per directory share.
//
// Resolvers are cached because building one canonicalises the root, and
// because caching pins the share to the directory that existed when it was
// created: swapping the root for a symlink afterwards cannot redirect it.
type rootCache struct {
	mu        sync.RWMutex
	resolvers map[string]PathResolver
}

func newRootCache() *rootCache {
	return &rootCache{resolvers: make(map[string]PathResolver)}
}

// Resolver implements RootProvider.
func (c *rootCache) Resolver(spec sharing.Spec) (PathResolver, error) {
	c.mu.RLock()
	resolver, ok := c.resolvers[spec.ID]
	c.mu.RUnlock()
	if ok {
		return resolver, nil
	}

	built, err := security.NewPathResolver(spec.Root)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	// Another request may have won the race; keep the first resolver so every
	// request for this share uses the same canonical root.
	if existing, ok := c.resolvers[spec.ID]; ok {
		return existing, nil
	}
	c.resolvers[spec.ID] = built
	return built, nil
}

// Forget drops a share's resolver once the share is gone.
func (c *rootCache) Forget(shareID string) {
	c.mu.Lock()
	delete(c.resolvers, shareID)
	c.mu.Unlock()
}
