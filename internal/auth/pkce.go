// Package auth implements OAuth 2.0 PKCE loopback login and (in Task 4)
// Device Authorization Grant flows against the Cyoda Cloud Auth0 tenant.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// PKCEVerifier is the high-entropy random string the client keeps secret and
// later presents at the token endpoint to prove possession (RFC 7636).
type PKCEVerifier string

// PKCEChallenge is the SHA-256 + base64url derivative of the verifier sent on
// the front channel.
type PKCEChallenge string

// NewPKCEVerifier returns a fresh 32-byte random verifier encoded as
// base64url-without-padding. The result is 43 characters, comfortably within
// RFC 7636's 43..128 range.
func NewPKCEVerifier() (PKCEVerifier, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return PKCEVerifier(base64.RawURLEncoding.EncodeToString(b)), nil
}

// Challenge returns the S256 challenge derived from the verifier.
func (v PKCEVerifier) Challenge() PKCEChallenge {
	sum := sha256.Sum256([]byte(v))
	return PKCEChallenge(base64.RawURLEncoding.EncodeToString(sum[:]))
}
