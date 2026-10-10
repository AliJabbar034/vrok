package sharing

import (
	"fmt"
	"time"
)

// TokenSource supplies the random identifiers a share needs. The concrete
// implementation lives in internal/security; declaring the interface here
// keeps the domain free of any dependency on a crypto package.
type TokenSource interface {
	NewID() (string, error)
	NewToken() (string, error)
}

// PasswordHasher converts a plaintext password into a storable digest.
type PasswordHasher interface {
	Hash(password string) (string, error)
}

// Options are the per-share settings a user can pass on the command line.
type Options struct {
	// TTL is how long the share lives. Zero means "until the process exits".
	TTL time.Duration
	// MaxDownloads caps completed transfers; zero means unlimited.
	MaxDownloads int
	// Password is plaintext. The factory hashes it immediately and never
	// copies it into the share, so it exists only for the life of this call.
	Password string
	// Name overrides the display name.
	Name string
}

// Factory assembles shares from a Source plus Options. It is the only place
// that mints ids and tokens, which keeps the "no sequential identifiers" rule
// enforceable in one spot.
type Factory struct {
	tokens TokenSource
	hasher PasswordHasher
	clock  Clock
}

// NewFactory returns a Factory. clock may be nil, in which case the system
// clock is used.
func NewFactory(tokens TokenSource, hasher PasswordHasher, clock Clock) *Factory {
	if clock == nil {
		clock = SystemClock{}
	}
	return &Factory{tokens: tokens, hasher: hasher, clock: clock}
}

// Create builds a share from src and opts.
func (f *Factory) Create(src Source, opts Options) (*Share, error) {
	if err := validate(src); err != nil {
		return nil, err
	}

	id, err := f.tokens.NewID()
	if err != nil {
		return nil, err
	}
	token, err := f.tokens.NewToken()
	if err != nil {
		return nil, err
	}

	var hash string
	if opts.Password != "" {
		if f.hasher == nil {
			return nil, fmt.Errorf("sharing: password requested but no hasher configured")
		}
		if hash, err = f.hasher.Hash(opts.Password); err != nil {
			return nil, err
		}
	}

	name := src.Name
	if opts.Name != "" {
		name = opts.Name
	}

	now := f.clock.Now()
	var expires time.Time
	if opts.TTL > 0 {
		expires = now.Add(opts.TTL)
	}

	return New(Spec{
		ID:           id,
		Token:        token,
		Name:         name,
		Kind:         src.Kind,
		Root:         src.Root,
		Entries:      src.Entries,
		Target:       src.Target,
		CreatedAt:    now,
		ExpiresAt:    expires,
		MaxDownloads: opts.MaxDownloads,
		PasswordHash: hash,
	}), nil
}

func validate(src Source) error {
	switch src.Kind {
	case KindDirectory:
		if src.Root == "" {
			return fmt.Errorf("sharing: directory share requires a root")
		}
	case KindReceive:
		if src.Root == "" {
			return fmt.Errorf("sharing: receive share requires an inbox folder")
		}
	case KindFile, KindFiles:
		if len(src.Entries) == 0 {
			return fmt.Errorf("sharing: file share requires at least one file")
		}
	case KindHTTP:
		if src.Target == "" {
			return fmt.Errorf("sharing: http share requires a target")
		}
	default:
		return fmt.Errorf("sharing: unsupported share kind %d", src.Kind)
	}
	return nil
}
