package relay

import (
	"errors"
	"regexp"
	"sync"
)

// ErrLabelTaken reports that another agent already owns a hostname label.
var ErrLabelTaken = errors.New("relay: hostname already registered")

// ErrAtCapacity reports that the relay is full.
var ErrAtCapacity = errors.New("relay: at capacity")

// labelPattern is what a share id must look like to become a DNS label. The
// relay validates it rather than trusting the agent, because this value ends
// up in a hostname.
var labelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{3,62}$`)

// ValidLabel reports whether id is usable as a hostname label.
func ValidLabel(id string) bool { return labelPattern.MatchString(id) }

// AgentRegistry maps hostname labels to connected agents.
//
// Like the CLI's share registry, this is in memory only: a relay restart drops
// every tunnel, and the agents reconnect. There is no state worth persisting
// because the files live on the agents' machines.
type AgentRegistry struct {
	mu       sync.RWMutex
	agents   map[string]*Agent
	capacity int
}

// NewAgentRegistry returns a registry holding at most capacity agents. A
// capacity of zero means unlimited.
func NewAgentRegistry(capacity int) *AgentRegistry {
	return &AgentRegistry{agents: make(map[string]*Agent), capacity: capacity}
}

// Add registers an agent under its label.
func (r *AgentRegistry) Add(a *Agent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.capacity > 0 && len(r.agents) >= r.capacity {
		return ErrAtCapacity
	}
	if _, taken := r.agents[a.ID]; taken {
		return ErrLabelTaken
	}
	r.agents[a.ID] = a
	return nil
}

// Remove unregisters an agent, but only if it is still the one registered
// under that label. The guard matters when a label is reclaimed by a
// reconnecting agent while the old one is still shutting down.
func (r *AgentRegistry) Remove(a *Agent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.agents[a.ID]; ok && current == a {
		delete(r.agents, a.ID)
	}
}

// Get looks up an agent by label.
func (r *AgentRegistry) Get(label string) (*Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.agents[label]
	return a, ok
}

// Len returns the number of connected agents.
func (r *AgentRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.agents)
}

// CloseAll disconnects every agent.
func (r *AgentRegistry) CloseAll(err error) {
	r.mu.Lock()
	agents := make([]*Agent, 0, len(r.agents))
	for _, a := range r.agents {
		agents = append(agents, a)
	}
	r.agents = make(map[string]*Agent)
	r.mu.Unlock()

	for _, a := range agents {
		a.Close(err)
	}
}
