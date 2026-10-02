package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// ErrBadSignature is returned when a signed value fails verification.
var ErrBadSignature = errors.New("security: invalid signature")

// Signer mints and checks short opaque proofs. vrok uses it for the cookie
// that records "this visitor already entered the password", so that the
// password itself never has to be stored client-side or re-sent per request.
type Signer interface {
	Sign(message string) string
	Verify(message, signature string) error
}

// HMACSigner signs with HMAC-SHA256 under a process-local key.
type HMACSigner struct{ key []byte }

// NewHMACSigner returns a Signer using key. Generate the key with NewSecret so
// that it is fresh on every run and signatures do not outlive the process.
func NewHMACSigner(key []byte) *HMACSigner { return &HMACSigner{key: key} }

// Sign implements Signer.
func (s *HMACSigner) Sign(message string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(message))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Verify implements Signer.
func (s *HMACSigner) Verify(message, signature string) error {
	if !ConstantTimeEqual(s.Sign(message), signature) {
		return ErrBadSignature
	}
	return nil
}
