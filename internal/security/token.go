// Package security provides the cryptographic and filesystem-confinement
// primitives used by vrok: unguessable identifiers, password hashing and
// path traversal protection.
package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"fmt"
)

const (
	// idBytes is the entropy behind a public share handle. The handle is only
	// a routing key: it is always paired with a full-strength token, so 40
	// bits is enough to make collisions and enumeration impractical while
	// keeping the handle short enough to read out loud.
	idBytes = 5

	// tokenBytes is the entropy behind the secret part of a share URL. 16
	// bytes is 128 bits, which is the floor for a value that is the only
	// thing standing between the internet and a local file.
	tokenBytes = 16

	// secretBytes is the entropy behind process-local signing keys.
	secretBytes = 32
)

// idEncoding produces lowercase alphanumeric handles without padding so they
// survive copy/paste, DNS labels and double-click selection.
var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// TokenSource hands out the random values a share needs. The server and
// sharing layers depend on this interface rather than on crypto/rand directly
// so tests can supply deterministic values.
type TokenSource interface {
	// NewID returns a short, human-quotable share handle.
	NewID() (string, error)
	// NewToken returns the high-entropy secret embedded in a share URL.
	NewToken() (string, error)
}

// CryptoTokenSource is the production TokenSource, backed by crypto/rand.
type CryptoTokenSource struct{}

// NewCryptoTokenSource returns a TokenSource suitable for production use.
func NewCryptoTokenSource() CryptoTokenSource { return CryptoTokenSource{} }

// NewID implements TokenSource.
func (CryptoTokenSource) NewID() (string, error) {
	b, err := randomBytes(idBytes)
	if err != nil {
		return "", err
	}
	return idEncoding.EncodeToString(b), nil
}

// NewToken implements TokenSource.
func (CryptoTokenSource) NewToken() (string, error) {
	b, err := randomBytes(tokenBytes)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewSecret returns a 256-bit key for signing process-local artefacts such as
// authentication cookies. Secrets are never persisted: restarting vrok
// invalidates every cookie it ever issued, which is the behaviour we want for
// a tool whose shares die with the process.
func NewSecret() ([]byte, error) { return randomBytes(secretBytes) }

// ConstantTimeEqual reports whether two secrets match without leaking their
// contents through timing. Use it for every token and signature comparison.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("security: read random bytes: %w", err)
	}
	return b, nil
}
