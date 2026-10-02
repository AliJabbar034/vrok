package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ErrPasswordMismatch is returned when a supplied password does not match a
// stored hash. It carries no detail on purpose.
var ErrPasswordMismatch = errors.New("security: password mismatch")

// Hasher turns a plaintext password into a verifiable digest. The server
// depends on this interface so the hashing cost can be lowered in tests.
type Hasher interface {
	Hash(password string) (string, error)
	Verify(password, encoded string) error
}

// Argon2Params configures the Argon2id cost. The defaults follow the OWASP
// recommendation for interactive logins.
type Argon2Params struct {
	Time    uint32 // iterations
	Memory  uint32 // KiB
	Threads uint8
	KeyLen  uint32
	SaltLen uint32
}

// DefaultArgon2Params returns the cost used by the CLI: 64 MiB and one pass,
// which takes tens of milliseconds and makes offline guessing expensive.
func DefaultArgon2Params() Argon2Params {
	return Argon2Params{Time: 1, Memory: 64 * 1024, Threads: 4, KeyLen: 32, SaltLen: 16}
}

// Argon2Hasher implements Hasher using Argon2id. Plaintext passwords are never
// stored or logged; only the encoded digest below ever leaves this type.
type Argon2Hasher struct{ params Argon2Params }

// NewArgon2Hasher returns a Hasher with the given cost parameters. Zero-valued
// fields fall back to DefaultArgon2Params.
func NewArgon2Hasher(p Argon2Params) *Argon2Hasher {
	d := DefaultArgon2Params()
	if p.Time == 0 {
		p.Time = d.Time
	}
	if p.Memory == 0 {
		p.Memory = d.Memory
	}
	if p.Threads == 0 {
		p.Threads = d.Threads
	}
	if p.KeyLen == 0 {
		p.KeyLen = d.KeyLen
	}
	if p.SaltLen == 0 {
		p.SaltLen = d.SaltLen
	}
	return &Argon2Hasher{params: p}
}

// Hash returns a PHC-formatted Argon2id digest:
// $argon2id$v=19$m=65536,t=1,p=4$<salt>$<key>
func (h *Argon2Hasher) Hash(password string) (string, error) {
	if password == "" {
		return "", errors.New("security: password must not be empty")
	}
	salt := make([]byte, h.params.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("security: generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, h.params.Time, h.params.Memory, h.params.Threads, h.params.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, h.params.Memory, h.params.Time, h.params.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// Verify recomputes the digest with the parameters recorded in encoded and
// compares it in constant time.
func (h *Argon2Hasher) Verify(password, encoded string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return errors.New("security: unrecognised password hash format")
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return errors.New("security: unsupported argon2 version")
	}

	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return errors.New("security: malformed password hash parameters")
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return errors.New("security: malformed password hash salt")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return errors.New("security: malformed password hash key")
	}

	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrPasswordMismatch
	}
	return nil
}
