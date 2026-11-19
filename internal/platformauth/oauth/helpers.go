package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

const randomValueBytes = 32

// NewState returns an unpredictable OAuth state value.
func NewState() (string, error) { return randomValue() }

// NewNonce returns an unpredictable OIDC nonce value.
func NewNonce() (string, error) { return randomValue() }

// NewVerifier returns an RFC 7636-compatible PKCE verifier.
func NewVerifier() (string, error) { return randomValue() }

// Challenge returns the S256 PKCE challenge for verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomValue() (string, error) {
	b := make([]byte, randomValueBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
